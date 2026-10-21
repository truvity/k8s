package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

var t0 = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

// world builds a cluster for one test: namespaces with or without a
// NetworkPolicy, Deployments with their ReplicaSet, and pods.
type world struct {
	objs []runtime.Object
	dyn  []runtime.Object
}

func (w *world) netpol(ns string) {
	w.objs = append(w.objs, &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "default-deny", Namespace: ns}})
}

// node adds a node, replacing one of the same name.
func (w *world) node(name string, age time.Duration) {
	w.objs = slices.DeleteFunc(w.objs, func(o runtime.Object) bool {
		n, ok := o.(*corev1.Node)
		return ok && n.Name == name
	})
	w.objs = append(w.objs, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(t0.Add(-age))}})
}

// deployment adds a Deployment's ReplicaSet and returns the controller
// reference its pods carry.
func (w *world) deployment(ns, name string) metav1.OwnerReference {
	rsUID := types.UID(ns + "-" + name + "-rs")
	w.objs = append(w.objs, &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: name + "-abc", Namespace: ns, UID: rsUID,
		OwnerReferences: []metav1.OwnerReference{ctrlRef("Deployment", name, types.UID(ns+"-"+name))},
	}})
	return ctrlRef("ReplicaSet", name+"-abc", rsUID)
}

func ctrlRef(kind, name string, uid types.UID) metav1.OwnerReference {
	yes := true
	return metav1.OwnerReference{APIVersion: "apps/v1", Kind: kind, Name: name, UID: uid, Controller: &yes}
}

type podSpec struct {
	ready         bool
	readySince    time.Duration // how long ago the Ready condition last changed
	started       time.Duration // how long ago the pod started
	restarts      int32
	labels        map[string]string
	terminating   bool
	node          string
	termMessage   string
	noController  bool
	ownerOverride *metav1.OwnerReference
}

func (w *world) pod(ns, name string, ref metav1.OwnerReference, s podSpec) {
	if s.started == 0 {
		s.started = 24 * time.Hour
	}
	if s.readySince == 0 {
		s.readySince = s.started
	}
	if s.node == "" {
		s.node = "node-a"
	}
	status := corev1.ConditionFalse
	if s.ready {
		status = corev1.ConditionTrue
	}
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID(ns + "-" + name), Labels: s.labels},
		Spec:       corev1.PodSpec{NodeName: s.node},
		Status: corev1.PodStatus{
			Phase:     corev1.PodRunning,
			PodIP:     "10.0.0." + fmt.Sprint(len(w.objs)),
			StartTime: &metav1.Time{Time: t0.Add(-s.started)},
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodReady, Status: status, LastTransitionTime: metav1.NewTime(t0.Add(-s.readySince)),
			}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app", RestartCount: s.restarts}},
		},
	}
	if s.termMessage != "" {
		p.Status.ContainerStatuses[0].LastTerminationState.Terminated = &corev1.ContainerStateTerminated{
			ExitCode: 1, Reason: "Error", Message: s.termMessage, FinishedAt: metav1.NewTime(t0.Add(-time.Minute)),
		}
	}
	switch {
	case s.ownerOverride != nil:
		p.OwnerReferences = []metav1.OwnerReference{*s.ownerOverride}
	case !s.noController:
		p.OwnerReferences = []metav1.OwnerReference{ref}
	}
	if s.terminating {
		now := metav1.NewTime(t0)
		p.DeletionTimestamp = &now
		p.Finalizers = []string{"example.com/hold"}
	}
	w.objs = append(w.objs, p)
}

type harness struct {
	r     *Repairer
	kube  *fake.Clientset
	dyn   *dynamicfake.FakeDynamicClient
	reg   *prometheus.Registry
	clock time.Time
	logs  *bytes.Buffer
}

func (w *world) start(t *testing.T, mutate func(*Config)) *harness {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Self = "netpol-repair/self"
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	h := &harness{kube: fake.NewClientset(w.objs...), reg: prometheus.NewRegistry(), clock: t0, logs: &bytes.Buffer{}}
	h.dyn = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		policyEndpointsGVR: "PolicyEndpointList",
		nodeDiagnosticsGVR: "NodeDiagnosticList",
	}, w.dyn...)
	log := slog.New(slog.NewJSONHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h.r = NewRepairer(cfg, h.kube, h.dyn, log, NewMetrics(h.reg), func() time.Time { return h.clock })
	return h
}

func (h *harness) tick(t *testing.T) {
	t.Helper()
	if err := h.r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) advance(d time.Duration) { h.clock = h.clock.Add(d) }

func (h *harness) exists(t *testing.T, ns, name string) bool {
	t.Helper()
	_, err := h.kube.CoreV1().Pods(ns).Get(context.Background(), name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true
}

func (h *harness) deletions() []string {
	var out []string
	for _, a := range h.kube.Actions() {
		if a.GetVerb() == "delete" && a.GetResource().Resource == "pods" {
			out = append(out, a.GetNamespace()+"/"+a.(interface{ GetName() string }).GetName())
		}
	}
	return out
}

func (h *harness) actions(ns, ownerName, reason string, mode Mode) float64 {
	return testutil.ToFloat64(h.r.metrics.actions.WithLabelValues(ns, "Deployment", ownerName, reason, string(mode)))
}

func (h *harness) candidateCount() int { return testutil.CollectAndCount(h.r.metrics.candidates) }

func (h *harness) events(t *testing.T, ns string) []corev1.Event {
	t.Helper()
	l, err := h.kube.CoreV1().Events(ns).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return l.Items
}

// crashLooping is a pod whose containers restarted 4 times since it started
// 5 minutes ago and that is NotReady: the shape of the incident.
func crashLooping() podSpec {
	return podSpec{started: 5 * time.Minute, readySince: 5 * time.Minute, restarts: 4}
}

func healthyPod() podSpec { return podSpec{ready: true} }

func TestReportModeDeletesNothing(t *testing.T) {
	w := &world{}
	w.netpol("app")
	w.node("node-a", 2*time.Hour)
	ref := w.deployment("app", "web")
	w.pod("app", "web-1", ref, crashLooping())
	w.pod("app", "web-2", ref, healthyPod())
	h := w.start(t, nil)

	h.tick(t)

	if !h.exists(t, "app", "web-1") {
		t.Fatal("report mode deleted the pod")
	}
	if got := h.deletions(); len(got) != 0 {
		t.Fatalf("deletions in report mode: %v", got)
	}
	if got := h.actions("app", "web", reasonRestarts, ModeReport); got != 1 {
		t.Fatalf("report actions = %v, want 1", got)
	}
	if got := h.actions("app", "web", reasonRestarts, ModeAct); got != 0 {
		t.Fatalf("act actions = %v, want 0", got)
	}
	if got := h.candidateCount(); got != 1 {
		t.Fatalf("candidate series = %d, want 1", got)
	}
	evs := h.events(t, "app")
	if len(evs) != 1 || evs[0].Reason != "NetpolStuckPodCandidate" || evs[0].InvolvedObject.Kind != "Deployment" || evs[0].InvolvedObject.Name != "web" {
		t.Fatalf("events = %+v", evs)
	}
	if !strings.Contains(h.logs.String(), "WOULD repair") {
		t.Fatalf("no WOULD repair log line:\n%s", h.logs)
	}
}

func TestActModeDeletesOnceWithEvidence(t *testing.T) {
	w := &world{}
	w.netpol("app")
	w.node("node-a", 2*time.Hour)
	ref := w.deployment("app", "web")
	s := crashLooping()
	s.termMessage = `dial tcp 172.20.0.1:443: i/o timeout`
	w.pod("app", "web-1", ref, s)
	w.pod("app", "web-2", ref, healthyPod())
	w.dyn = append(w.dyn, policyEndpoint("app", "web-egress-x1", "web-1"), policyEndpoint("app", "other-y2", "unrelated"))
	h := w.start(t, func(c *Config) { c.Mode = ModeAct })

	h.tick(t)

	if h.exists(t, "app", "web-1") {
		t.Fatal("act mode did not delete the candidate")
	}
	if !h.exists(t, "app", "web-2") {
		t.Fatal("act mode deleted the healthy replica")
	}
	if got := h.actions("app", "web", reasonRestarts, ModeAct); got != 1 {
		t.Fatalf("act actions = %v, want 1", got)
	}
	evs := h.events(t, "app")
	if len(evs) != 1 || evs[0].Reason != "NetpolStuckPodRepaired" {
		t.Fatalf("events = %+v", evs)
	}
	for _, want := range []string{"web-egress-x1", "i/o timeout", "node-a", "aws/aws-network-policy-agent#676"} {
		if !strings.Contains(evs[0].Message, want) {
			t.Errorf("event message lacks %q: %s", want, evs[0].Message)
		}
	}
	if strings.Contains(evs[0].Message, "other-y2") {
		t.Errorf("event names a PolicyEndpoint that does not select the pod: %s", evs[0].Message)
	}
	if !strings.Contains(h.logs.String(), `"nodeAge":"2h0m0s"`) {
		t.Errorf("log lacks the node age:\n%s", h.logs)
	}
}

func policyEndpoint(ns, name, podName string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "networking.k8s.aws/v1alpha1",
		"kind":       "PolicyEndpoint",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec": map[string]any{
			"podSelectorEndpoints": []any{map[string]any{"name": podName, "namespace": ns, "podIP": "192.0.2.1", "hostIP": "192.0.2.100"}},
		},
	}}
}

func TestExclusions(t *testing.T) {
	stsRef := ctrlRef("StatefulSet", "db", "db-uid")
	jobRef := ctrlRef("Job", "migrate", "job-uid")
	dsRef := ctrlRef("DaemonSet", "agent", "ds-uid")
	cases := []struct {
		name   string
		setup  func(w *world)
		mutate func(*Config)
	}{
		{name: "namespace without a NetworkPolicy", setup: func(w *world) {
			ref := w.deployment("open", "web")
			w.pod("open", "web-1", ref, crashLooping())
			w.pod("open", "web-2", ref, healthyPod())
		}},
		{name: "StatefulSet pod", setup: func(w *world) {
			w.netpol("app")
			s := crashLooping()
			s.ownerOverride = &stsRef
			w.pod("app", "db-0", metav1.OwnerReference{}, s)
		}},
		{name: "Job pod", setup: func(w *world) {
			w.netpol("app")
			s := crashLooping()
			s.ownerOverride = &jobRef
			w.pod("app", "migrate-x", metav1.OwnerReference{}, s)
		}},
		{name: "DaemonSet pod", setup: func(w *world) {
			w.netpol("app")
			s := crashLooping()
			s.ownerOverride = &dsRef
			w.pod("app", "agent-x", metav1.OwnerReference{}, s)
		}},
		{name: "bare pod", setup: func(w *world) {
			w.netpol("app")
			s := crashLooping()
			s.noController = true
			w.pod("app", "bare", metav1.OwnerReference{}, s)
		}},
		{name: "CloudNativePG instance", setup: func(w *world) {
			w.netpol("app")
			ref := w.deployment("app", "web")
			s := crashLooping()
			s.labels = map[string]string{"cnpg.io/cluster": "pg"}
			w.pod("app", "web-1", ref, s)
			w.pod("app", "web-2", ref, healthyPod())
		}},
		{name: "terminating pod", setup: func(w *world) {
			w.netpol("app")
			ref := w.deployment("app", "web")
			s := crashLooping()
			s.terminating = true
			w.pod("app", "web-1", ref, s)
			w.pod("app", "web-2", ref, healthyPod())
		}},
		{name: "excluded namespace", mutate: func(c *Config) { c.ExcludeNamespaces = []string{"app"} }, setup: func(w *world) {
			w.netpol("app")
			ref := w.deployment("app", "web")
			w.pod("app", "web-1", ref, crashLooping())
			w.pod("app", "web-2", ref, healthyPod())
		}},
		{name: "namespace not in the allow list", mutate: func(c *Config) { c.Namespaces = []string{"other"} }, setup: func(w *world) {
			w.netpol("app")
			ref := w.deployment("app", "web")
			w.pod("app", "web-1", ref, crashLooping())
			w.pod("app", "web-2", ref, healthyPod())
		}},
		{name: "this controller's own pod", setup: func(w *world) {
			w.netpol("netpol-repair")
			ref := w.deployment("netpol-repair", "repair")
			w.pod("netpol-repair", "self", ref, crashLooping())
			w.pod("netpol-repair", "repair-2", ref, healthyPod())
		}},
		{name: "too few restarts and not NotReady long enough", setup: func(w *world) {
			w.netpol("app")
			ref := w.deployment("app", "web")
			w.pod("app", "web-1", ref, podSpec{restarts: 2, started: 5 * time.Minute, readySince: 9 * time.Minute})
			w.pod("app", "web-2", ref, healthyPod())
		}},
		{name: "restarts long ago (pod older than the window, no history)", setup: func(w *world) {
			w.netpol("app")
			ref := w.deployment("app", "web")
			w.pod("app", "web-1", ref, podSpec{ready: true, restarts: 40, started: 48 * time.Hour})
			w.pod("app", "web-2", ref, healthyPod())
		}},
		{name: "pending pod", setup: func(w *world) {
			w.netpol("app")
			ref := w.deployment("app", "web")
			w.pod("app", "web-1", ref, crashLooping())
			w.pod("app", "web-2", ref, healthyPod())
			p := w.objs[len(w.objs)-2].(*corev1.Pod)
			p.Status.Phase = corev1.PodPending
		}},
		{name: "every replica NotReady (no proof the owner works)", setup: func(w *world) {
			w.netpol("app")
			ref := w.deployment("app", "web")
			w.pod("app", "web-1", ref, crashLooping())
			w.pod("app", "web-2", ref, crashLooping())
		}},
		{name: "the owner's last Ready replica, restarting", setup: func(w *world) {
			w.netpol("app")
			ref := w.deployment("app", "web")
			w.pod("app", "web-1", ref, podSpec{ready: true, restarts: 5, started: 5 * time.Minute})
			w.pod("app", "web-2", ref, podSpec{readySince: time.Minute})
		}},
		{name: "the only pod, stuck less than the single-replica threshold", setup: func(w *world) {
			w.netpol("app")
			ref := w.deployment("app", "web")
			w.pod("app", "web-1", ref, podSpec{restarts: 4, started: 5 * time.Minute, readySince: 5 * time.Minute})
		}},
		{name: "node older than max-node-age", mutate: func(c *Config) { c.MaxNodeAge = time.Hour }, setup: func(w *world) {
			w.netpol("app")
			w.node("node-a", 3*time.Hour)
			ref := w.deployment("app", "web")
			w.pod("app", "web-1", ref, crashLooping())
			w.pod("app", "web-2", ref, healthyPod())
		}},
		{name: "no timeout evidence when it is required", mutate: func(c *Config) { c.RequireTimeoutEvidence = true }, setup: func(w *world) {
			w.netpol("app")
			ref := w.deployment("app", "web")
			s := crashLooping()
			s.termMessage = "panic: nil map"
			w.pod("app", "web-1", ref, s)
			w.pod("app", "web-2", ref, healthyPod())
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &world{}
			w.node("node-a", time.Hour)
			tc.setup(w)
			h := w.start(t, func(c *Config) {
				c.Mode = ModeAct
				if tc.mutate != nil {
					tc.mutate(c)
				}
			})
			h.tick(t)
			if got := h.deletions(); len(got) != 0 {
				t.Fatalf("deleted %v", got)
			}
			if got := h.candidateCount(); got != 0 {
				t.Fatalf("candidate series = %d, want 0\n%s", got, h.logs)
			}
		})
	}
}

func TestInclusions(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		setup  func(w *world)
		mutate func(*Config)
	}{
		{name: "NotReady for 10 minutes beside a Ready replica", reason: reasonNotReady, setup: func(w *world) {
			ref := w.deployment("app", "web")
			w.pod("app", "web-1", ref, podSpec{readySince: 10 * time.Minute})
			w.pod("app", "web-2", ref, healthyPod())
		}},
		{name: "the only pod, NotReady for 10 minutes", reason: reasonNotReady, setup: func(w *world) {
			ref := w.deployment("app", "web")
			w.pod("app", "web-1", ref, podSpec{readySince: 10 * time.Minute})
		}},
		{name: "the only pod, crash looping for 12 minutes", reason: reasonRestarts, setup: func(w *world) {
			ref := w.deployment("app", "web")
			w.pod("app", "web-1", ref, podSpec{restarts: 6, started: 12 * time.Minute, readySince: 12 * time.Minute})
		}},
		{name: "a ReplicaSet without a Deployment", reason: reasonRestarts, setup: func(w *world) {
			rs := ctrlRef("ReplicaSet", "plain", "plain-rs")
			w.objs = append(w.objs, &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "plain", Namespace: "app", UID: "plain-rs"}})
			w.pod("app", "plain-1", rs, crashLooping())
			w.pod("app", "plain-2", rs, healthyPod())
		}},
		{name: "a young node under max-node-age", reason: reasonRestarts, mutate: func(c *Config) { c.MaxNodeAge = 2 * time.Hour }, setup: func(w *world) {
			ref := w.deployment("app", "web")
			w.pod("app", "web-1", ref, crashLooping())
			w.pod("app", "web-2", ref, healthyPod())
		}},
		{name: "timeout evidence in the termination message", reason: reasonRestarts, mutate: func(c *Config) { c.RequireTimeoutEvidence = true },
			setup: func(w *world) {
				ref := w.deployment("app", "web")
				s := crashLooping()
				s.termMessage = "Get \"https://172.20.0.1:443/api\": dial tcp 172.20.0.1:443: connect: connection timed out"
				w.pod("app", "web-1", ref, s)
				w.pod("app", "web-2", ref, healthyPod())
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &world{}
			w.netpol("app")
			w.node("node-a", time.Hour)
			tc.setup(w)
			h := w.start(t, func(c *Config) {
				c.Mode = ModeAct
				if tc.mutate != nil {
					tc.mutate(c)
				}
			})
			h.tick(t)
			got := h.deletions()
			if len(got) != 1 || !strings.HasSuffix(got[0], "-1") {
				t.Fatalf("deletions = %v, want the -1 pod\n%s", got, h.logs)
			}
			if !strings.Contains(h.logs.String(), `"reason":"`+tc.reason+`"`) {
				t.Fatalf("reason %q not logged:\n%s", tc.reason, h.logs)
			}
		})
	}
}

// A pod older than the window is a candidate once its restarts climb
// within the window, which needs history across ticks.
func TestRestartHistory(t *testing.T) {
	w := &world{}
	w.netpol("app")
	ref := w.deployment("app", "web")
	w.pod("app", "web-1", ref, podSpec{ready: true, restarts: 10, started: 48 * time.Hour})
	w.pod("app", "web-2", ref, healthyPod())
	h := w.start(t, func(c *Config) { c.Mode = ModeAct })

	bump := func(n int32) {
		p, err := h.kube.CoreV1().Pods("app").Get(context.Background(), "web-1", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		p.Status.ContainerStatuses[0].RestartCount = n
		if _, err := h.kube.CoreV1().Pods("app").UpdateStatus(context.Background(), p, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	h.tick(t) // baseline 10
	h.advance(5 * time.Minute)
	bump(12)
	h.tick(t)
	if len(h.deletions()) != 0 {
		t.Fatal("2 restarts made a candidate")
	}
	// Restarts spread over more than the window never add up.
	h.advance(16 * time.Minute)
	bump(13)
	h.tick(t)
	if len(h.deletions()) != 0 {
		t.Fatal("restarts outside the window were counted")
	}
	h.advance(5 * time.Minute)
	bump(16)
	h.tick(t)
	if got := h.deletions(); len(got) != 1 {
		t.Fatalf("3 restarts within the window: deletions = %v\n%s", got, h.logs)
	}
}

func TestOwnerCooldown(t *testing.T) {
	w := &world{}
	w.netpol("app")
	ref := w.deployment("app", "web")
	w.pod("app", "web-1", ref, crashLooping())
	w.pod("app", "web-2", ref, crashLooping())
	w.pod("app", "web-3", ref, healthyPod())
	h := w.start(t, func(c *Config) { c.Mode = ModeAct })

	h.tick(t)
	if got := h.deletions(); len(got) != 1 {
		t.Fatalf("first tick: deletions = %v", got)
	}
	for range 29 {
		h.advance(time.Minute)
		h.tick(t)
	}
	if got := h.deletions(); len(got) != 1 {
		t.Fatalf("within the cooldown: deletions = %v", got)
	}
	h.advance(time.Minute)
	h.tick(t)
	if got := h.deletions(); len(got) != 2 {
		t.Fatalf("after the cooldown: deletions = %v", got)
	}
}

func TestRateLimit(t *testing.T) {
	w := &world{}
	w.netpol("app")
	for _, name := range []string{"a", "b", "c"} {
		ref := w.deployment("app", name)
		w.pod("app", name+"-1", ref, crashLooping())
		w.pod("app", name+"-2", ref, healthyPod())
	}
	h := w.start(t, func(c *Config) { c.Mode = ModeAct })

	h.tick(t)
	if got := h.deletions(); len(got) != 1 {
		t.Fatalf("one tick: deletions = %v, want 1", got)
	}
	h.advance(30 * time.Second)
	h.tick(t)
	if got := h.deletions(); len(got) != 1 {
		t.Fatalf("30 s later: deletions = %v, want still 1", got)
	}
	// A tick that fires a moment early is not held.
	h.advance(30*time.Second - 500*time.Millisecond)
	h.tick(t)
	if got := h.deletions(); len(got) != 2 {
		t.Fatalf("a minute later: deletions = %v, want 2", got)
	}
	if got := h.candidateCount(); got != 2 {
		t.Fatalf("candidate series = %d, want 2 (both remaining owners)", got)
	}
}

func TestSelfStop(t *testing.T) {
	w := &world{}
	w.netpol("app")
	for i := range 7 {
		name := fmt.Sprintf("d%d", i)
		ref := w.deployment("app", name)
		w.pod("app", name+"-1", ref, crashLooping())
		w.pod("app", name+"-2", ref, healthyPod())
	}
	h := w.start(t, func(c *Config) { c.Mode = ModeAct })

	for i := range 7 {
		if i > 0 {
			h.advance(time.Minute)
		}
		h.tick(t)
	}
	if got := h.deletions(); len(got) != 5 {
		t.Fatalf("deletions = %v, want exactly 5 before the self-stop", got)
	}
	if !h.r.Halted() {
		t.Fatal("not halted after the limit")
	}
	if got := testutil.ToFloat64(h.r.metrics.halted); got != 1 {
		t.Fatalf("netpol_repair_halted = %v, want 1", got)
	}
	// Halted, it keeps reporting.
	var reported float64
	for i := range 7 {
		reported += h.actions("app", fmt.Sprintf("d%d", i), reasonRestarts, ModeReport)
	}
	if reported != 2 {
		t.Fatalf("report actions while halted = %v, want 2", reported)
	}
	// The latch holds after the window has passed: only a restart clears it.
	h.advance(2 * time.Hour)
	h.tick(t)
	if got := h.deletions(); len(got) != 5 {
		t.Fatalf("deleted after the window while halted: %v", got)
	}
	if !strings.Contains(h.logs.String(), "HALTING") {
		t.Fatalf("no HALTING log line:\n%s", h.logs)
	}
}

// Report mode never halts: the halt is about deletions.
func TestReportModeNeverHalts(t *testing.T) {
	w := &world{}
	w.netpol("app")
	for i := range 8 {
		name := fmt.Sprintf("d%d", i)
		ref := w.deployment("app", name)
		w.pod("app", name+"-1", ref, crashLooping())
		w.pod("app", name+"-2", ref, healthyPod())
	}
	h := w.start(t, nil)
	for i := range 8 {
		if i > 0 {
			h.advance(time.Minute)
		}
		h.tick(t)
	}
	if h.r.Halted() {
		t.Fatal("report mode halted")
	}
	if got := len(h.deletions()); got != 0 {
		t.Fatalf("report mode deleted %d pods", got)
	}
}

func TestNodeDiagnostic(t *testing.T) {
	w := &world{}
	w.netpol("app")
	ref := w.deployment("app", "web")
	w.pod("app", "web-1", ref, crashLooping())
	w.pod("app", "web-2", ref, healthyPod())
	h := w.start(t, func(c *Config) { c.Mode = ModeAct; c.NodeDiagnostic = true })

	h.tick(t)

	nd, err := h.dyn.Resource(nodeDiagnosticsGVR).Get(context.Background(), "node-a", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("no NodeDiagnostic for the node: %v", err)
	}
	dest, _, _ := unstructured.NestedString(nd.Object, "spec", "logCapture", "destination")
	if dest != "node" {
		t.Fatalf("destination = %q, want node", dest)
	}
}

func TestNoNodeDiagnosticByDefault(t *testing.T) {
	w := &world{}
	w.netpol("app")
	ref := w.deployment("app", "web")
	w.pod("app", "web-1", ref, crashLooping())
	w.pod("app", "web-2", ref, healthyPod())
	h := w.start(t, func(c *Config) { c.Mode = ModeAct })

	h.tick(t)

	for _, a := range h.dyn.Actions() {
		if a.GetVerb() == "create" {
			t.Fatalf("created %s without -node-diagnostic", a.GetResource().Resource)
		}
	}
}

func TestConfigValidate(t *testing.T) {
	ok := DefaultConfig()
	if err := ok.Validate(); err != nil {
		t.Fatalf("default config refused: %v", err)
	}
	if ok.Mode != ModeReport {
		t.Fatalf("default mode = %s, want report", ok.Mode)
	}
	bad := []func(*Config){
		func(c *Config) { c.Mode = "delete" },
		func(c *Config) { c.MaxRepairs = 0 },
		func(c *Config) { c.OwnerCooldown = 0 },
		func(c *Config) { c.MinDeleteInterval = 0 },
		func(c *Config) { c.RestartThreshold = 0 },
		func(c *Config) { c.RequireTimeoutEvidence = true; c.TimeoutPatterns = nil },
		func(c *Config) { c.NodeDiagnostic = true; c.NodeDiagnosticDestination = "" },
		func(c *Config) { c.MaxNodeAge = -time.Second },
	}
	for i, mutate := range bad {
		c := DefaultConfig()
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("case %d: accepted %+v", i, c)
		}
	}
}

func TestParseFlags(t *testing.T) {
	o, err := parseFlags([]string{"-mode=act", "-exclude-namespaces=kube-system, ops", "-max-node-age=6h"})
	if err != nil {
		t.Fatal(err)
	}
	if o.cfg.Mode != ModeAct || len(o.cfg.ExcludeNamespaces) != 2 || o.cfg.ExcludeNamespaces[1] != "ops" || o.cfg.MaxNodeAge != 6*time.Hour {
		t.Fatalf("parsed %+v", o.cfg)
	}
	if o.cfg.MaxRepairs != 5 || o.cfg.OwnerCooldown != 30*time.Minute {
		t.Fatalf("defaults lost: %+v", o.cfg)
	}
	if _, err := parseFlags([]string{"-mode=nuke"}); err == nil {
		t.Fatal("an unknown mode was accepted")
	}
}
