// Command netpol-stuck-pod-repair deletes Deployment pods that the EKS
// network policy agent left with an empty eBPF egress map.
//
// TEMPORARY WORKAROUND. Remove it once the fix is in the node image.
//
// THE BUG. The AWS network policy agent can lose a race between a pod's CNI
// ADD and the reconcile of its PolicyEndpoint. The pod is then Running, its
// PolicyEndpoints are correct, but the eBPF map its attached program reads is
// empty: every egress connection times out (`dial tcp ...: i/o timeout`).
// It hits pods started in bursts on a fresh node and same-node replacements.
// Restarting the containers does not help (same pod, same IP, same maps);
// deleting the pod does (a new pod gets a new IP and fresh maps).
//
//   - upstream issue: https://github.com/aws/aws-network-policy-agent/issues/676
//   - upstream fix:   https://github.com/aws/aws-network-policy-agent/pull/705
//
// On EKS Auto Mode the agent is part of the node image, so there is nothing
// to upgrade until AWS ships an image whose agent contains #705.
//
// REMOVAL CONDITION: uninstall this when the EKS Auto Mode node image ships
// an aws-network-policy-agent release that contains #705; verify with a node
// drift roll followed by two weeks with zero repairs
// (netpol_repair_actions_total{mode="act"} unchanged).
//
// WHAT IT DOES. Every -interval it lists the pods of the namespaces that hold
// at least one NetworkPolicy, picks the ReplicaSet-owned pods that restart
// repeatedly or stay NotReady, and, within strict limits (README.md, section
// netpol-stuck-pod-repair), records evidence and deletes them. The default
// mode is report: it logs and counts what it would delete and deletes nothing.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "netpol-stuck-pod-repair: %v\n", err)
		os.Exit(1)
	}
}

// options are the command line: the Config plus how to reach the cluster
// and serve metrics.
type options struct {
	cfg         Config
	kubeconfig  string
	metricsAddr string
	once        bool
	logLevel    string
}

func parseFlags(args []string) (options, error) {
	var o options
	d := DefaultConfig()
	fs := flag.NewFlagSet("netpol-stuck-pod-repair", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mode := fs.String("mode", string(d.Mode), "report (log and count what would be deleted) or act (delete)")
	fs.DurationVar(&o.cfg.Interval, "interval", d.Interval, "how often the cluster is examined")
	fs.IntVar(&o.cfg.RestartThreshold, "restart-threshold", d.RestartThreshold, "container restarts within -restart-window that make a pod a candidate")
	fs.DurationVar(&o.cfg.RestartWindow, "restart-window", d.RestartWindow, "window over which restarts are counted")
	fs.DurationVar(&o.cfg.NotReadyAfter, "not-ready-after", d.NotReadyAfter, "how long a Running pod must stay NotReady to be a candidate")
	fs.DurationVar(&o.cfg.SingleReplicaStuckAfter, "single-replica-stuck-after", d.SingleReplicaStuckAfter,
		"how long the only pod of its owner must have been stuck before it is deleted")
	fs.DurationVar(&o.cfg.OwnerCooldown, "owner-cooldown", d.OwnerCooldown, "no second repair of the same owner within this")
	fs.DurationVar(&o.cfg.MinDeleteInterval, "min-delete-interval", d.MinDeleteInterval, "at most one deletion per this, cluster-wide")
	fs.IntVar(&o.cfg.MaxRepairs, "max-repairs", d.MaxRepairs, "repairs within -halt-window after which the next candidate halts acting until restart")
	fs.DurationVar(&o.cfg.HaltWindow, "halt-window", d.HaltWindow, "window of -max-repairs")
	namespaces := fs.String("namespaces", "", "comma-separated namespaces to consider; empty: every namespace")
	exclude := fs.String("exclude-namespaces", "", "comma-separated namespaces never considered")
	fs.DurationVar(&o.cfg.MaxNodeAge, "max-node-age", 0, "consider only pods on nodes younger than this; 0: off (same-node replacements happen on old nodes too)")
	fs.BoolVar(&o.cfg.RequireTimeoutEvidence, "require-timeout-evidence", false,
		"consider only pods whose last termination message or recent log contains one of -timeout-patterns")
	patterns := fs.String("timeout-patterns", strings.Join(d.TimeoutPatterns, ","), "comma-separated substrings that are the bug's evidence")
	fs.Int64Var(&o.cfg.LogTailLines, "log-tail-lines", d.LogTailLines, "log lines read per container when looking for -timeout-patterns")
	fs.BoolVar(&o.cfg.NodeDiagnostic, "node-diagnostic", false, "create an EKS Auto Mode NodeDiagnostic for the node before a repair")
	fs.StringVar(&o.cfg.NodeDiagnosticDestination, "node-diagnostic-destination", d.NodeDiagnosticDestination,
		`the NodeDiagnostic's logCapture.destination: "node" or a presigned S3 URL`)
	fs.StringVar(&o.cfg.Self, "self", os.Getenv("POD_NAMESPACE")+"/"+os.Getenv("POD_NAME"), "namespace/name of this pod, never touched")
	fs.StringVar(&o.kubeconfig, "kubeconfig", "", "kubeconfig file; empty: in-cluster, else the default loading rules")
	fs.StringVar(&o.metricsAddr, "metrics-address", ":8080", "address of /metrics, /healthz and /readyz; empty: none")
	fs.BoolVar(&o.once, "once", false, "examine the cluster once and exit")
	fs.StringVar(&o.logLevel, "log-level", "info", "debug, info, warn or error")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	o.cfg.Mode = Mode(*mode)
	o.cfg.Namespaces = splitList(*namespaces)
	o.cfg.ExcludeNamespaces = splitList(*exclude)
	o.cfg.TimeoutPatterns = splitList(*patterns)
	return o, o.cfg.Validate()
}

func run(ctx context.Context, args []string, stderr io.Writer) error {
	o, err := parseFlags(args)
	if err != nil {
		return err
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(o.logLevel)); err != nil {
		return fmt.Errorf("-log-level: %w", err)
	}
	log := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: level}))

	rc, err := restConfig(o.kubeconfig)
	if err != nil {
		return err
	}
	rc.UserAgent = "netpol-stuck-pod-repair"
	kube, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return err
	}
	dyn, err := dynamic.NewForConfig(rc)
	if err != nil {
		return err
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	r := NewRepairer(o.cfg, kube, dyn, log, NewMetrics(reg), time.Now)

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.InfoContext(ctx, "starting: a TEMPORARY workaround for aws/aws-network-policy-agent#676 (fix: #705)",
		slog.String("mode", string(o.cfg.Mode)), slog.String("interval", o.cfg.Interval.String()),
		slog.Any("namespaces", o.cfg.Namespaces), slog.Any("excludeNamespaces", o.cfg.ExcludeNamespaces),
		slog.String("maxNodeAge", o.cfg.MaxNodeAge.String()), slog.Bool("requireTimeoutEvidence", o.cfg.RequireTimeoutEvidence),
		slog.Bool("nodeDiagnostic", o.cfg.NodeDiagnostic))

	if o.once {
		return r.Tick(ctx)
	}

	if o.metricsAddr != "" {
		srv := &http.Server{Addr: o.metricsAddr, Handler: r.Handler(reg), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("metrics server stopped", slog.String("error", err.Error()))
				stop()
			}
		}()
		defer func() {
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdown)
		}()
	}

	ticker := time.NewTicker(o.cfg.Interval)
	defer ticker.Stop()
	for {
		if err := r.Tick(ctx); err != nil && ctx.Err() == nil {
			log.ErrorContext(ctx, "examination failed", slog.String("error", err.Error()))
		}
		select {
		case <-ctx.Done():
			log.Info("stopping")
			return nil
		case <-ticker.C:
		}
	}
}

func restConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig == "" {
		if rc, err := rest.InClusterConfig(); err == nil {
			return rc, nil
		}
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = kubeconfig
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}
