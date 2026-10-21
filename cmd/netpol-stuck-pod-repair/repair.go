package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// cnpgClusterLabel marks a CloudNativePG instance pod. Those are never
// touched: the operator owns their lifecycle and a deletion is a failover.
const cnpgClusterLabel = "cnpg.io/cluster"

// Reasons a pod is a candidate (the `reason` label).
const (
	reasonRestarts = "restarts"
	reasonNotReady = "not-ready"
)

// owner is the workload a pod belongs to: the Deployment of its
// ReplicaSet, or the ReplicaSet itself when no Deployment owns it.
type owner struct {
	Namespace string
	Kind      string // Deployment or ReplicaSet
	Name      string
	UID       types.UID
}

func (o owner) key() string { return o.Namespace + "/" + o.Kind + "/" + o.Name }

// candidate is a pod the rules select, before the safety limits.
type candidate struct {
	pod        *corev1.Pod
	owner      owner
	reason     string
	restarts   int32 // restarts within the window
	stuckFor   time.Duration
	otherReady int
	onlyPod    bool
	node       *corev1.Node // nil when it could not be read
}

type sample struct {
	at       time.Time
	restarts int32
}

// Repairer is the controller. Tick is one examination; everything it
// remembers between ticks (restart history, cooldowns, the halt latch) is
// in memory and starts empty on restart, by design: a restart is how an
// operator clears the halt.
type Repairer struct {
	cfg     Config
	kube    kubernetes.Interface
	dyn     dynamic.Interface
	log     *slog.Logger
	metrics *Metrics
	now     func() time.Time

	history    map[types.UID][]sample  // container restart samples per pod
	badSince   map[types.UID]time.Time // first tick a pod met a trigger, while it keeps meeting one
	lastAction map[string]time.Time    // owner key -> last repair (or would-be repair in report mode)
	repairs    []time.Time             // act-mode deletions within HaltWindow
	lastDelete time.Time
	halted     bool
	lastOK     atomic.Int64 // unix seconds of the last successful tick
	started    time.Time
}

// NewRepairer wires a Repairer. now is the clock (time.Now outside tests).
func NewRepairer(cfg Config, kube kubernetes.Interface, dyn dynamic.Interface, log *slog.Logger, m *Metrics, now func() time.Time) *Repairer {
	return &Repairer{
		cfg: cfg, kube: kube, dyn: dyn, log: log, metrics: m, now: now,
		history:    map[types.UID][]sample{},
		badSince:   map[types.UID]time.Time{},
		lastAction: map[string]time.Time{},
		started:    now(),
	}
}

// Halted reports whether the self-stop has latched.
func (r *Repairer) Halted() bool { return r.halted }

// Handler serves /metrics, /healthz (the process is alive) and /readyz
// (an examination succeeded within three intervals).
func (r *Repairer) Handler(reg *prometheus.Registry) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		last := time.Unix(r.lastOK.Load(), 0)
		if r.lastOK.Load() == 0 || r.now().Sub(last) > 3*r.cfg.Interval {
			http.Error(w, "no successful examination recently", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux
}

// Tick examines the cluster once and repairs at most one pod.
func (r *Repairer) Tick(ctx context.Context) error {
	now := r.now()

	nps, err := r.kube.NetworkingV1().NetworkPolicies(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list NetworkPolicies: %w", err)
	}
	enforced := map[string]bool{}
	for i := range nps.Items {
		enforced[nps.Items[i].Namespace] = true
	}

	pods, err := r.kube.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list Pods: %w", err)
	}
	byNamespace := map[string][]*corev1.Pod{}
	for i := range pods.Items {
		p := &pods.Items[i]
		if enforced[p.Namespace] && r.namespaceAllowed(p.Namespace) {
			byNamespace[p.Namespace] = append(byNamespace[p.Namespace], p)
		}
	}

	r.observeRestarts(now, byNamespace)

	cands := r.candidates(ctx, now, byNamespace)
	r.metrics.setCandidates(cands)
	r.metrics.halted.Set(boolFloat(r.halted))

	// Longest stuck first, so a burst is worked through oldest-first.
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].stuckFor > cands[j].stuckFor })
	for i := range cands {
		if r.handle(ctx, now, &cands[i]) {
			break // at most one decision per tick: the cluster-wide rate limit
		}
	}
	r.metrics.halted.Set(boolFloat(r.halted))
	r.metrics.lastRun.Set(float64(now.Unix()))
	r.lastOK.Store(now.Unix())
	return nil
}

func (r *Repairer) namespaceAllowed(ns string) bool {
	if slices.Contains(r.cfg.ExcludeNamespaces, ns) {
		return false
	}
	return len(r.cfg.Namespaces) == 0 || slices.Contains(r.cfg.Namespaces, ns)
}

// observeRestarts records each pod's restart count and forgets pods that
// are gone.
func (r *Repairer) observeRestarts(now time.Time, byNamespace map[string][]*corev1.Pod) {
	seen := map[types.UID]bool{}
	for _, pods := range byNamespace {
		for _, p := range pods {
			seen[p.UID] = true
			// Keep the samples inside the window; the oldest is the
			// baseline the window's restarts are counted from.
			r.history[p.UID] = append(r.history[p.UID], sample{at: now, restarts: containerRestarts(p)})
			r.history[p.UID] = slices.DeleteFunc(r.history[p.UID], func(s sample) bool { return now.Sub(s.at) > r.cfg.RestartWindow })
		}
	}
	for uid := range r.history {
		if !seen[uid] {
			delete(r.history, uid)
			delete(r.badSince, uid)
		}
	}
}

// restartsInWindow is how many times the pod's containers restarted within
// RestartWindow, as far as can be told: from the samples, or, for a pod
// younger than the window, every restart since it started (so a fresh
// controller does not need a window of history to see a fresh crash loop).
func (r *Repairer) restartsInWindow(now time.Time, p *corev1.Pod) int32 {
	cur := containerRestarts(p)
	var n int32
	if h := r.history[p.UID]; len(h) > 0 {
		n = cur - h[0].restarts
	}
	if p.Status.StartTime != nil && now.Sub(p.Status.StartTime.Time) <= r.cfg.RestartWindow && cur > n {
		n = cur
	}
	return n
}

// candidates applies the selection rules. It never deletes anything.
func (r *Repairer) candidates(ctx context.Context, now time.Time, byNamespace map[string][]*corev1.Pod) []candidate {
	var out []candidate
	namespaces := make([]string, 0, len(byNamespace))
	for ns := range byNamespace {
		namespaces = append(namespaces, ns)
	}
	sort.Strings(namespaces)
	for _, ns := range namespaces {
		pods := byNamespace[ns]
		var owners map[types.UID]owner // ReplicaSet UID -> owner, read once per namespace when needed
		for _, p := range pods {
			reason, restarts, notReadyFor := r.trigger(now, p)
			if reason == "" {
				delete(r.badSince, p.UID)
				continue
			}
			if _, ok := r.badSince[p.UID]; !ok {
				r.badSince[p.UID] = now
			}
			stuckFor := now.Sub(r.badSince[p.UID])
			if notReadyFor > stuckFor {
				stuckFor = notReadyFor
			}

			if skip := r.excluded(p); skip != "" {
				r.log.DebugContext(ctx, "not a candidate", podAttrs(p), slog.String("why", skip))
				continue
			}
			if owners == nil {
				var err error
				if owners, err = r.replicaSetOwners(ctx, ns); err != nil {
					r.log.WarnContext(ctx, "cannot resolve owners", slog.String("namespace", ns), slog.String("error", err.Error()))
					break
				}
			}
			o, ok := ownerOf(p, owners)
			if !ok {
				continue
			}
			c := candidate{pod: p, owner: o, reason: reason, restarts: restarts, stuckFor: stuckFor}
			c.otherReady, c.onlyPod = siblings(p, o, pods, owners)
			if why := r.ineligible(&c); why != "" {
				r.log.DebugContext(ctx, "not a candidate", podAttrs(p), slog.String("why", why))
				continue
			}
			if r.cfg.MaxNodeAge > 0 || p.Spec.NodeName != "" {
				c.node = r.node(ctx, p.Spec.NodeName)
			}
			if r.cfg.MaxNodeAge > 0 {
				if c.node == nil || now.Sub(c.node.CreationTimestamp.Time) > r.cfg.MaxNodeAge {
					r.log.DebugContext(ctx, "not a candidate", podAttrs(p), slog.String("why", "node older than max-node-age, or unreadable"))
					continue
				}
			}
			if r.cfg.RequireTimeoutEvidence {
				if ev := r.timeoutEvidence(ctx, p); ev == "" {
					r.log.DebugContext(ctx, "not a candidate", podAttrs(p), slog.String("why", "no timeout evidence"))
					continue
				}
			}
			out = append(out, c)
		}
	}
	return out
}

// trigger says why the pod is a candidate, if it is one: restarts within
// the window, or Running and NotReady for NotReadyAfter. It also returns
// how long the pod has been NotReady (0 when Ready).
func (r *Repairer) trigger(now time.Time, p *corev1.Pod) (reason string, restarts int32, notReadyFor time.Duration) {
	if p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning {
		return "", 0, 0
	}
	restarts = r.restartsInWindow(now, p)
	notReadyFor = notReadyDuration(now, p)
	switch {
	case restarts >= int32(r.cfg.RestartThreshold):
		return reasonRestarts, restarts, notReadyFor
	case notReadyFor >= r.cfg.NotReadyAfter:
		return reasonNotReady, restarts, notReadyFor
	}
	return "", restarts, notReadyFor
}

// excluded are the rules that depend on the pod alone.
func (r *Repairer) excluded(p *corev1.Pod) string {
	if p.Namespace+"/"+p.Name == r.cfg.Self {
		return "this controller's own pod"
	}
	if _, ok := p.Labels[cnpgClusterLabel]; ok {
		return "a CloudNativePG instance"
	}
	ref := metav1.GetControllerOf(p)
	if ref == nil {
		return "no controller (bare pod)"
	}
	if ref.Kind != "ReplicaSet" {
		return "owned by a " + ref.Kind
	}
	return ""
}

// ineligible are the rules that depend on the pod's siblings.
func (r *Repairer) ineligible(c *candidate) string {
	ready := isReady(c.pod)
	switch {
	case c.otherReady >= 1:
		return ""
	case ready:
		return "the owner's last Ready replica"
	case !c.onlyPod:
		return "the owner has other pods and none of them is Ready"
	case c.stuckFor < r.cfg.SingleReplicaStuckAfter:
		return fmt.Sprintf("the owner's only pod, stuck %s of %s", c.stuckFor.Round(time.Second), r.cfg.SingleReplicaStuckAfter)
	}
	return ""
}

// clockSlack absorbs the jitter between ticks: a deletion one interval ago
// must not hold the next one because the tick fired a millisecond early.
const clockSlack = 2 * time.Second

// handle applies the safety limits to one candidate and reports or acts.
// It returns true when it made a decision (repaired, would have, or was
// held by the cluster-wide limits), which ends the tick.
func (r *Repairer) handle(ctx context.Context, now time.Time, c *candidate) bool {
	attrs := []any{podAttrs(c.pod), ownerAttrs(c.owner), slog.String("reason", c.reason)}
	if last, ok := r.lastAction[c.owner.key()]; ok && now.Sub(last) < r.cfg.OwnerCooldown {
		r.log.InfoContext(ctx, "candidate in its owner's cooldown", append(attrs, slog.Time("lastRepair", last))...)
		return false
	}
	if now.Sub(r.lastDelete) < r.cfg.MinDeleteInterval-clockSlack {
		r.log.InfoContext(ctx, "candidate held by the cluster-wide rate limit", attrs...)
		return true
	}

	// The self-stop: acting stops for good (until a restart) once MaxRepairs
	// deletions fall within HaltWindow and another candidate turns up. A
	// burst that size is not the race this works around. Reporting goes on.
	mode := r.cfg.Mode
	if mode == ModeAct {
		r.repairs = slices.DeleteFunc(r.repairs, func(t time.Time) bool { return now.Sub(t) >= r.cfg.HaltWindow })
		if !r.halted && len(r.repairs) >= r.cfg.MaxRepairs {
			r.halted = true
			r.metrics.halted.Set(1)
			r.log.ErrorContext(ctx, "HALTING: the repair limit is reached; a burst this size is not the race this tool works around. "+
				"Reporting only from now on. Investigate, then restart the controller to resume",
				append(attrs, slog.Int("repairs", len(r.repairs)), slog.String("window", r.cfg.HaltWindow.String()))...)
		}
		if r.halted {
			mode = ModeReport
		}
	}
	attrs = append(attrs, slog.String("mode", string(mode)), slog.Bool("halted", r.halted))

	ev := r.evidence(ctx, now, c)
	r.recordEvent(ctx, now, c, mode, ev)
	if r.cfg.NodeDiagnostic {
		r.nodeDiagnostic(ctx, c)
	}
	r.lastAction[c.owner.key()] = now
	r.lastDelete = now

	if mode == ModeReport {
		r.metrics.action(c, ModeReport)
		r.log.WarnContext(ctx, "WOULD repair (report only): a pod the network policy agent race may have stranded", append(attrs, ev.attrs()...)...)
		return true
	}

	uid := c.pod.UID
	err := r.kube.CoreV1().Pods(c.pod.Namespace).Delete(ctx, c.pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
		r.log.ErrorContext(ctx, "repair failed: cannot delete the pod", append(attrs, slog.String("error", err.Error()))...)
		return true
	}
	r.repairs = append(r.repairs, now)
	r.metrics.action(c, ModeAct)
	r.log.WarnContext(ctx, "REPAIRED: deleted a pod the network policy agent race may have stranded", append(attrs, ev.attrs()...)...)
	return true
}

// replicaSetOwners maps every ReplicaSet of the namespace to its owner.
func (r *Repairer) replicaSetOwners(ctx context.Context, ns string) (map[types.UID]owner, error) {
	rss, err := r.kube.AppsV1().ReplicaSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make(map[types.UID]owner, len(rss.Items))
	for i := range rss.Items {
		rs := &rss.Items[i]
		out[rs.UID] = replicaSetOwner(rs)
	}
	return out, nil
}

func replicaSetOwner(rs *appsv1.ReplicaSet) owner {
	if ref := metav1.GetControllerOf(rs); ref != nil && ref.Kind == "Deployment" {
		return owner{Namespace: rs.Namespace, Kind: "Deployment", Name: ref.Name, UID: ref.UID}
	}
	return owner{Namespace: rs.Namespace, Kind: "ReplicaSet", Name: rs.Name, UID: rs.UID}
}

func ownerOf(p *corev1.Pod, owners map[types.UID]owner) (owner, bool) {
	ref := metav1.GetControllerOf(p)
	if ref == nil || ref.Kind != "ReplicaSet" {
		return owner{}, false
	}
	o, ok := owners[ref.UID]
	return o, ok
}

// siblings counts the owner's other Ready pods and says whether the pod is
// the owner's only (non-terminating) pod.
func siblings(p *corev1.Pod, o owner, pods []*corev1.Pod, owners map[types.UID]owner) (otherReady int, onlyPod bool) {
	onlyPod = true
	for _, s := range pods {
		if s.UID == p.UID || s.DeletionTimestamp != nil {
			continue
		}
		if so, ok := ownerOf(s, owners); !ok || so.key() != o.key() {
			continue
		}
		onlyPod = false
		if isReady(s) {
			otherReady++
		}
	}
	return otherReady, onlyPod
}

func (r *Repairer) node(ctx context.Context, name string) *corev1.Node {
	if name == "" {
		return nil
	}
	n, err := r.kube.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		r.log.DebugContext(ctx, "cannot read the node", slog.String("node", name), slog.String("error", err.Error()))
		return nil
	}
	return n
}

func containerRestarts(p *corev1.Pod) int32 {
	var n int32
	for i := range p.Status.ContainerStatuses {
		n += p.Status.ContainerStatuses[i].RestartCount
	}
	return n
}

func isReady(p *corev1.Pod) bool {
	for i := range p.Status.Conditions {
		if p.Status.Conditions[i].Type == corev1.PodReady {
			return p.Status.Conditions[i].Status == corev1.ConditionTrue
		}
	}
	return false
}

// notReadyDuration is how long the pod's Ready condition has not been
// True (0 when it is). A pod with no Ready condition yet counts from its
// start.
func notReadyDuration(now time.Time, p *corev1.Pod) time.Duration {
	for i := range p.Status.Conditions {
		c := &p.Status.Conditions[i]
		if c.Type != corev1.PodReady {
			continue
		}
		if c.Status == corev1.ConditionTrue {
			return 0
		}
		if !c.LastTransitionTime.IsZero() {
			return now.Sub(c.LastTransitionTime.Time)
		}
	}
	if p.Status.StartTime != nil {
		return now.Sub(p.Status.StartTime.Time)
	}
	return 0
}

func podAttrs(p *corev1.Pod) slog.Attr {
	return slog.Group("pod", slog.String("namespace", p.Namespace), slog.String("name", p.Name),
		slog.String("uid", string(p.UID)), slog.String("node", p.Spec.NodeName), slog.String("ip", p.Status.PodIP))
}

func ownerAttrs(o owner) slog.Attr {
	return slog.Group("owner", slog.String("kind", o.Kind), slog.String("name", o.Name))
}

func boolFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
