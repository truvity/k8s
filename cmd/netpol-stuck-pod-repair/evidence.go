package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// The AWS network policy controller's per-policy endpoint lists. The agent
// on each node programs its eBPF maps from these; in the bug they are
// correct while the node's map is empty.
var policyEndpointsGVR = schema.GroupVersionResource{Group: "networking.k8s.aws", Version: "v1alpha1", Resource: "policyendpoints"}

// The EKS Auto Mode node diagnostic: a cluster-scoped object named after
// the node that makes the node package its logs (the network policy
// agent's among them). https://docs.aws.amazon.com/eks/latest/userguide/auto-get-logs.html
var nodeDiagnosticsGVR = schema.GroupVersionResource{Group: "eks.amazonaws.com", Version: "v1alpha1", Resource: "nodediagnostics"}

const maxMessage = 512

// evidence is what a repair records about the pod before deleting it: what
// AWS asks for in aws/aws-network-policy-agent#676, and what a person needs
// to tell the race from an application that is simply broken.
type evidence struct {
	node             string
	nodeAge          time.Duration // 0 when unknown
	restartsTotal    int32
	restartsInWindow int32
	stuckFor         time.Duration
	otherReady       int
	terminations     []string // per container: reason, exit code, message of the last termination
	policyEndpoints  []string // PolicyEndpoints whose podSelectorEndpoints list the pod
	timeoutMatch     string   // the first -timeout-patterns hit, with where it was found
}

func (e *evidence) attrs() []any {
	return []any{slog.Group("evidence",
		slog.String("node", e.node), slog.String("nodeAge", e.nodeAge.Round(time.Second).String()),
		slog.Int("restartsTotal", int(e.restartsTotal)), slog.Int("restartsInWindow", int(e.restartsInWindow)),
		slog.String("stuckFor", e.stuckFor.Round(time.Second).String()), slog.Int("otherReadyReplicas", e.otherReady),
		slog.Any("lastTerminations", e.terminations), slog.Any("policyEndpoints", e.policyEndpoints),
		slog.String("timeoutEvidence", e.timeoutMatch))}
}

func (r *Repairer) evidence(ctx context.Context, now time.Time, c *candidate) evidence {
	e := evidence{
		node:             c.pod.Spec.NodeName,
		restartsTotal:    containerRestarts(c.pod),
		restartsInWindow: c.restarts,
		stuckFor:         c.stuckFor,
		otherReady:       c.otherReady,
		terminations:     terminations(c.pod),
		policyEndpoints:  r.policyEndpoints(ctx, c.pod),
		timeoutMatch:     r.timeoutEvidence(ctx, c.pod),
	}
	if c.node != nil {
		e.nodeAge = now.Sub(c.node.CreationTimestamp.Time)
	}
	return e
}

func terminations(p *corev1.Pod) []string {
	var out []string
	for i := range p.Status.ContainerStatuses {
		cs := &p.Status.ContainerStatuses[i]
		t := cs.LastTerminationState.Terminated
		if t == nil {
			continue
		}
		out = append(out, fmt.Sprintf("%s: %s exit %d at %s: %s", cs.Name, t.Reason, t.ExitCode,
			t.FinishedAt.UTC().Format(time.RFC3339), truncate(strings.TrimSpace(t.Message), maxMessage)))
	}
	return out
}

// policyEndpoints names the PolicyEndpoints of the pod's namespace that
// select the pod. Best effort: a cluster without the CRD yields none.
func (r *Repairer) policyEndpoints(ctx context.Context, p *corev1.Pod) []string {
	if r.dyn == nil {
		return nil
	}
	list, err := r.dyn.Resource(policyEndpointsGVR).Namespace(p.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		r.log.DebugContext(ctx, "cannot list PolicyEndpoints", slog.String("namespace", p.Namespace), slog.String("error", err.Error()))
		return nil
	}
	var out []string
	for i := range list.Items {
		pe := &list.Items[i]
		eps, _, _ := unstructured.NestedSlice(pe.Object, "spec", "podSelectorEndpoints")
		for _, ep := range eps {
			m, ok := ep.(map[string]any)
			if !ok {
				continue
			}
			if m["name"] == p.Name || (p.Status.PodIP != "" && m["podIP"] == p.Status.PodIP) {
				out = append(out, pe.GetName())
				break
			}
		}
	}
	slices.Sort(out)
	return out
}

// timeoutEvidence looks for a -timeout-patterns hit in the containers'
// last termination messages, then in the tail of their logs (the previous
// instance's for a container that restarted). Empty: none found.
func (r *Repairer) timeoutEvidence(ctx context.Context, p *corev1.Pod) string {
	for i := range p.Status.ContainerStatuses {
		cs := &p.Status.ContainerStatuses[i]
		if t := cs.LastTerminationState.Terminated; t != nil {
			if pat := matchAny(t.Message, r.cfg.TimeoutPatterns); pat != "" {
				return fmt.Sprintf("%q in container %s's termination message", pat, cs.Name)
			}
		}
	}
	if r.cfg.LogTailLines == 0 {
		return ""
	}
	for i := range p.Status.ContainerStatuses {
		cs := &p.Status.ContainerStatuses[i]
		previous := cs.RestartCount > 0
		tail, limit := r.cfg.LogTailLines, int64(64*1024)
		req := r.kube.CoreV1().Pods(p.Namespace).GetLogs(p.Name, &corev1.PodLogOptions{
			Container: cs.Name, Previous: previous, TailLines: &tail, LimitBytes: &limit,
		})
		rc, err := req.Stream(ctx)
		if err != nil {
			r.log.DebugContext(ctx, "cannot read the log", podAttrs(p), slog.String("container", cs.Name), slog.String("error", err.Error()))
			continue
		}
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, io.LimitReader(rc, limit))
		_ = rc.Close()
		if pat := matchAny(buf.String(), r.cfg.TimeoutPatterns); pat != "" {
			which := "current"
			if previous {
				which = "previous"
			}
			return fmt.Sprintf("%q in container %s's %s log", pat, cs.Name, which)
		}
	}
	return ""
}

func matchAny(s string, patterns []string) string {
	for _, p := range patterns {
		if p != "" && strings.Contains(s, p) {
			return p
		}
	}
	return ""
}

// recordEvent puts a Warning Event on the owner, which outlives the pod.
func (r *Repairer) recordEvent(ctx context.Context, now time.Time, c *candidate, mode Mode, e evidence) {
	reason, verb := "NetpolStuckPodRepaired", "deleted"
	if mode == ModeReport {
		reason, verb = "NetpolStuckPodCandidate", "would delete (report only)"
	}
	apiVersion := "apps/v1"
	msg := fmt.Sprintf("netpol-stuck-pod-repair %s pod %s on node %s (%s; restarts in window %d, stuck %s, other Ready replicas %d; "+
		"PolicyEndpoints %v; timeout evidence %q). TEMPORARY workaround for aws/aws-network-policy-agent#676.",
		verb, c.pod.Name, e.node, c.reason, e.restartsInWindow, e.stuckFor.Round(time.Second), e.otherReady,
		e.policyEndpoints, e.timeoutMatch)
	ts := metav1.NewTime(now)
	ev := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{GenerateName: c.owner.Name + ".netpol-repair.", Namespace: c.owner.Namespace},
		InvolvedObject: corev1.ObjectReference{
			APIVersion: apiVersion, Kind: c.owner.Kind, Namespace: c.owner.Namespace, Name: c.owner.Name, UID: c.owner.UID,
		},
		Reason:              reason,
		Message:             truncate(msg, 1024),
		Type:                corev1.EventTypeWarning,
		Source:              corev1.EventSource{Component: "netpol-stuck-pod-repair"},
		ReportingController: "truvity.io/netpol-stuck-pod-repair",
		FirstTimestamp:      ts,
		LastTimestamp:       ts,
		Count:               1,
	}
	if _, err := r.kube.CoreV1().Events(c.owner.Namespace).Create(ctx, ev, metav1.CreateOptions{}); err != nil {
		r.log.WarnContext(ctx, "cannot record the Event", ownerAttrs(c.owner), slog.String("error", err.Error()))
	}
}

// nodeDiagnostic asks EKS Auto Mode to capture the node's logs (the network
// policy agent's among them) before the evidence is gone with the pod.
// One object per node: an existing one is left alone.
func (r *Repairer) nodeDiagnostic(ctx context.Context, c *candidate) {
	node := c.pod.Spec.NodeName
	if node == "" || r.dyn == nil {
		return
	}
	nd := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "eks.amazonaws.com/v1alpha1",
		"kind":       "NodeDiagnostic",
		"metadata": map[string]any{
			"name": node,
			"labels": map[string]any{
				"app.kubernetes.io/managed-by": "netpol-stuck-pod-repair",
			},
			"annotations": map[string]any{
				"netpol-stuck-pod-repair.truvity.io/pod": c.pod.Namespace + "/" + c.pod.Name,
			},
		},
		"spec": map[string]any{
			"logCapture": map[string]any{
				"destination": r.cfg.NodeDiagnosticDestination,
				"categories":  []any{"All"},
			},
		},
	}}
	_, err := r.dyn.Resource(nodeDiagnosticsGVR).Create(ctx, nd, metav1.CreateOptions{})
	switch {
	case err == nil:
		r.log.InfoContext(ctx, "NodeDiagnostic created", slog.String("node", node))
	case apierrors.IsAlreadyExists(err):
		r.log.InfoContext(ctx, "NodeDiagnostic already exists for the node; not replaced", slog.String("node", node))
	default:
		r.log.WarnContext(ctx, "cannot create the NodeDiagnostic", slog.String("node", node), slog.String("error", err.Error()))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
