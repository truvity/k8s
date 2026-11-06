#!/usr/bin/env bash
# The guardrails-projects admission policies on a real API server. cel-go in
# tests/admission proves what an expression decides; only a Kubernetes API
# server proves that it is accepted (type-checked, within the CEL cost budget)
# and that Warn and Deny do what the binding says. Called by
# hack/conformance.sh against the cluster it created, and runnable alone against
# any scratch cluster that serves ValidatingAdmissionPolicy (Kubernetes 1.31+,
# for image volumes): it creates the namespaces iap-* and removes its policies.
#
#   0. the control: a policy over the budget is seen to fail;
#   1. a 20-row, 8-prefix image allow-list is accepted, type-checked;
#   2. with [Deny, Audit] an off-list image is refused and an on-list one is
#      admitted, for containers, init containers, image volumes and ephemeral
#      containers, and the worst-case pod (100 + 100 containers) is judged by
#      the list, never by the cost budget;
#   3. with [Warn, Audit] the same off-list pod is admitted with a warning.
set -euo pipefail

AGNHOST="${AGNHOST:-registry.k8s.io/e2e-test-images/agnhost:2.66.1}"
root="$(cd "$(dirname "$0")/.." && pwd)"
work="${work:-$(mktemp -d)}"
name=pod-image-allow-list

step() { printf '\n--- %s\n' "$*"; }
fail() { echo "CONFORMANCE FAILURE: $*" >&2; exit 1; }

cleanup() {
  kubectl delete validatingadmissionpolicybinding,validatingadmissionpolicy "$name" conformance-over-budget \
    --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete namespace iap-control iap-3 iap-free --ignore-not-found --wait=false >/dev/null 2>&1 || true
}
trap cleanup EXIT

# until <what> <seconds> <command...>: the API server picks a new policy up
# within seconds; poll for it instead of sleeping a guessed time.
until_ok() {
  local what="$1" limit="$2"
  shift 2
  for _ in $(seq 1 "$limit"); do
    if "$@" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  fail "timed out waiting for $what"
}

# pod <namespace> <name> <image> [extra spec lines, two-space indented]
pod() {
  cat <<YAML
apiVersion: v1
kind: Pod
metadata: {name: $2, namespace: $1}
spec:
  containers: [{name: c, image: "$3"}]
${4:-}
YAML
}

# dry <namespace> <name> <image> [extra spec]: the API server's verdict, with
# its warnings, on creating the pod. Never persists anything.
dry() { pod "$@" | kubectl create --dry-run=server -f - 2>&1 || true; }

# manyPods <namespace> <name> <containers> <init containers>: a pod of
# off-list images, to make the evaluation as long as it can be.
many() {
  python3 - "$@" <<'PY'
import sys
ns, name, n, m = sys.argv[1], sys.argv[2], int(sys.argv[3]), int(sys.argv[4])
print(f"apiVersion: v1\nkind: Pod\nmetadata: {{name: {name}, namespace: {ns}}}\nspec:\n  containers:")
for i in range(n):
    print(f"    - {{name: c{i}, image: \"worst.example.test/team/some/deep/path/image-{i}:1.2.3\"}}")
if m:
    print("  initContainers:")
    for i in range(m):
        print(f"    - {{name: i{i}, image: \"worst.example.test/team/some/deep/path/init-{i}:1.2.3\"}}")
PY
}

step "0. the control: a policy over the CEL cost budget is seen to fail"
control_refused() {
  local out
  out="$(many iap-control big 100 0 | kubectl create --dry-run=server -f - 2>&1 || true)"
  grep -q "cost limit exceeded" <<<"$out"
}
kubectl create namespace iap-control >/dev/null
kubectl apply -f "$root/tests/conformance/guardrails/over-budget.yaml" >/dev/null
until_ok "a 100-container pod to exceed the control policy's budget" 90 control_refused
echo "an over-budget policy fails with: actual cost limit exceeded"

step "1. a 20-row, 8-prefix image allow-list is accepted by the API server"
python3 - > "$work/iap-values.yaml" <<'PY'
print("imageAllowList:\n  enabled: true\n  kinds: [product]\n  common:")
for i in range(4):
    print(f"    - mesh{i}.example.test/common/")
print("  sets:\n    registry:")
for i in range(7):
    print(f"      - registry{i}.example.test/team/")
print("      - registry.k8s.io/e2e-test-images/")
print("projects:")
for i in range(20):
    print(f"  - {{name: iap-{i}, kind: product, images: {{sets: [registry], allow: [\"row{i}.example.test/app/api\", \"row{i}.example.test/app/worker\"]}}}}")
PY
render() { # <actions json>
  helm template iap "$root/charts/guardrails-projects" -f "$work/iap-values.yaml" \
    --set-json "imageAllowList.validationActions=$1" -s templates/image-allow-list.yaml
}
render '["Deny","Audit"]' > "$work/iap-deny.yaml"
kubectl create namespace iap-3 >/dev/null
kubectl create namespace iap-free >/dev/null
kubectl apply -f "$work/iap-deny.yaml" >/dev/null || fail "the API server refuses the rendered policy"
until_ok "the policy to be type-checked" 60 \
  kubectl wait --for=jsonpath='{.status.observedGeneration}'=1 "validatingadmissionpolicy/$name" --timeout=1s
warnings="$(kubectl get validatingadmissionpolicy "$name" -o jsonpath='{.status.typeChecking.expressionWarnings}')"
[ -z "$warnings" ] || fail "the API server type-checks the policy with warnings: $warnings"
until_ok "the default service account of iap-3" 60 kubectl -n iap-3 get serviceaccount default
echo "accepted; no type-checking warnings"

step "2. [Deny, Audit]: refused and admitted by the list, on the API server"
# The binding takes a moment to reach the admission chain: wait until an
# off-list image is refused, then judge everything else strictly.
probe_refused() { grep -q "denied request: .* is not on the allow-list" <<<"$(dry iap-3 probe probe.example.test/x)"; }
until_ok "an off-list image to be refused" 90 probe_refused

expect_refused() { # <what> <image named in the message> <output>
  grep -q "denied request: image $2 is not on the allow-list of namespace iap-3" <<<"$3" || fail "$1: expected a refusal naming $2, got: $3"
  echo "refused: $1"
}
expect_admitted() { # <what> <output>
  grep -q "created (server dry run)" <<<"$2" && ! grep -qi "denied\|warning" <<<"$2" || fail "$1: expected it admitted, got: $2"
  echo "admitted: $1"
}

expect_admitted "a path prefix of the shared set" "$(dry iap-3 p registry2.example.test/team/api:1)"
expect_admitted "an image of the shared set, by digest" "$(dry iap-3 p "registry.k8s.io/e2e-test-images/agnhost@sha256:$(printf 'a%.0s' $(seq 64))")"
expect_admitted "an exact repository of the row" "$(dry iap-3 p row3.example.test/app/api:7)"
expect_admitted "a common prefix" "$(dry iap-3 p mesh1.example.test/common/proxy:2)"
expect_refused "another row's repository" row4.example.test/app/api:1 "$(dry iap-3 p row4.example.test/app/api:1)"
expect_refused "a repository that only starts like the row's" row3.example.test/app/api2:1 "$(dry iap-3 p row3.example.test/app/api2:1)"
expect_refused "a prefix lookalike" registry2.example.test/team-evil/x:1 "$(dry iap-3 p registry2.example.test/team-evil/x:1)"
expect_refused "an unqualified image" nginx "$(dry iap-3 p nginx)"
expect_refused "an init container" init.example.test/i "$(dry iap-3 p registry2.example.test/team/api:1 '  initContainers: [{name: i, image: init.example.test/i}]')"
expect_refused "an image volume" volume.example.test/v "$(dry iap-3 p registry2.example.test/team/api:1 '  volumes: [{name: v, image: {reference: volume.example.test/v}}]')"
expect_admitted "an image volume on the list" "$(dry iap-3 p registry2.example.test/team/api:1 '  volumes: [{name: v, image: {reference: registry3.example.test/team/model:1}}]')"
expect_admitted "a namespace with no row" "$(dry iap-free p nginx)"

# The worst case pod: every image off the list, 100 containers and 100 init
# containers, against a 20-row, 8+4+2 entry map. It must be judged by the list.
out="$(many iap-3 worst 100 100 | kubectl create --dry-run=server -f - 2>&1 || true)"
grep -q "cost" <<<"$out" && fail "the worst-case pod ran out of the CEL cost budget: $out"
grep -q "image worst.example.test/team/some/deep/path/image-0:1.2.3 is not on the allow-list of namespace iap-3 (and 199 more)" <<<"$out" \
  || fail "the worst-case pod was not judged by the list: $out"
echo "refused: 200 off-list images, judged by the list, within the cost budget"

# An ephemeral container (kubectl debug) is checked on the subresource. The
# pod only has to exist: its image is on the list, it need not be running.
kubectl -n iap-3 run live --image registry.k8s.io/e2e-test-images/agnhost:2.66.1 --restart=Never >/dev/null
out="$(kubectl -n iap-3 debug live --image debug.example.test/shell --attach=false -c dbg 2>&1 || true)"
expect_refused "an ephemeral container" debug.example.test/shell "$out"
kubectl -n iap-3 debug live --image "$AGNHOST" --attach=false -c dbg-ok >/dev/null || fail "an ephemeral container on the list was refused"
echo "admitted: an ephemeral container on the list"
# A label edit leaves every image alone: skipped, whatever the pod's images.
kubectl -n iap-3 label pod live conformance=1 >/dev/null || fail "an update that leaves the images alone was refused"
echo "admitted: an update that leaves the images alone"

step "3. [Warn, Audit]: the same off-list pod is admitted, with a warning"
render '["Warn","Audit"]' | kubectl apply -f - >/dev/null
probe_warns() { grep -q "^Warning:.*is not on the allow-list" <<<"$(dry iap-3 probe probe.example.test/x)"; }
until_ok "the binding to warn" 90 probe_warns
out="$(dry iap-3 p row4.example.test/app/api:1)"
grep -q "created (server dry run)" <<<"$out" || fail "Warn refused a pod: $out"
grep -q "^Warning:.*image row4.example.test/app/api:1 is not on the allow-list of namespace iap-3" <<<"$out" \
  || fail "Warn did not warn about the image: $out"
echo "admitted with a warning naming the image"

printf '\nimage allow-list: conformance passed\n'
