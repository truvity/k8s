#!/usr/bin/env bash
# Conformance of the Talos provider, without Talos hardware:
#
#   1. offline: render a cluster's machine configs with a fresh secrets bundle
#      and validate every one with the real talosctl (metal mode, strict);
#   2. kind, standing in for Talos nodes (no CNI, no kube-proxy): Cilium with
#      the Talos values (pkg/talos/cilium), then
#      - LB-IPAM and L2 announcements (charts/cilium-config): a LoadBalancer
#        Service gets an address from the pool and answers from the runner;
#      - NetworkPolicy is enforced (charts/cluster-network-policies);
#      - local volumes (charts/local-volumes) bind and hold data on their node;
#      - the internal CA (charts/cluster-pki) issues a certificate;
#      - the etcd backup objects (charts/talos-etcd-backup) pass the API
#        server and Pod Security restricted.
#
# Needs: docker, kind, kubectl, helm, go, talosctl, curl, python3. CI runs it
# (.github/workflows/conformance.yaml); it creates and deletes a kind cluster.
set -euo pipefail

CLUSTER="${CLUSTER:-talos-conformance}"
CILIUM_VERSION="${CILIUM_VERSION:-$(sed -n 's/^pinned_version: *"\{0,1\}\([^"]*\)"\{0,1\}$/\1/p' charts/cilium-crds/crdctl.yaml)}"
CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.21.2}"
AGNHOST="${AGNHOST:-registry.k8s.io/e2e-test-images/agnhost:2.66.1}"
BUSYBOX="${BUSYBOX:-docker.io/library/busybox:1.37}"
work="$(mktemp -d)"

step() { printf '\n=== %s\n' "$*"; }
fail() { echo "CONFORMANCE FAILURE: $*" >&2; exit 1; }

cleanup() {
  if [ "${KEEP_CLUSTER:-}" = "" ]; then kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true; fi
  rm -rf "$work"
}
trap cleanup EXIT

step "1. render machine configs offline and validate them with talosctl $(talosctl version --client --short 2>/dev/null | tail -1)"
go run ./tests/conformance/cmd/conformance talos-render "$work/talos"
for f in "$work"/talos/*.machine.yaml; do
  talosctl validate --mode metal --strict --config "$f" || fail "talosctl refuses $(basename "$f")"
done
python3 - "$work/talos" <<'PY'
import json, sys
d = sys.argv[1]
disc = json.load(open(f"{d}/openid-configuration.json"))
jwks = json.load(open(f"{d}/jwks.json"))
contract = json.load(open(f"{d}/contract.json"))
assert disc["jwks_uri"] == disc["issuer"] + "/openid/v1/jwks", disc
assert disc["issuer"] == contract["OIDCIssuer"], (disc, contract)
assert [k["alg"] for k in jwks["keys"]] == ["RS256"], jwks
print("issuer documents match the contract")
PY

step "2. kind cluster without CNI and kube-proxy"
cat > "$work/kind.yaml" <<YAML
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  disableDefaultCNI: true
  kubeProxyMode: none
nodes:
  - role: control-plane
  - role: worker
  - role: worker
YAML
kind create cluster --name "$CLUSTER" --config "$work/kind.yaml" --wait 0s
kubectl config use-context "kind-$CLUSTER" >/dev/null

step "3. Cilium $CILIUM_VERSION with the Talos values"
helm upgrade --install cilium-crds charts/cilium-crds --wait
go run ./tests/conformance/cmd/conformance cilium-values "$CLUSTER-control-plane" 6443 > "$work/cilium.yaml"
helm repo add cilium https://helm.cilium.io >/dev/null
helm upgrade --install cilium cilium/cilium --version "$CILIUM_VERSION" -n kube-system \
  -f "$work/cilium.yaml" --set operator.skipCRDCreation=true --wait --timeout 10m
kubectl wait --for=condition=Ready nodes --all --timeout 5m

step "4. LB-IPAM and L2 announcements (cilium-config)"
subnet="$(docker network inspect kind -f '{{range .IPAM.Config}}{{println .Subnet}}{{end}}' | grep -v : | head -1)"
prefix="$(echo "$subnet" | cut -d. -f1-2)"
start="$prefix.255.200" stop="$prefix.255.220"
cat > "$work/network.yaml" <<YAML
loadBalancerIPPools:
  conformance:
    blocks:
      - {start: $start, stop: $stop}
l2Announcements:
  policies:
    default:
      interfaces: ["^eth0\$"]
      loadBalancerIPs: true
YAML
helm upgrade --install cilium-config charts/cilium-config -f "$work/network.yaml" --wait
kubectl create namespace conformance
kubectl -n conformance create deployment echo --image "$AGNHOST" --port 8080 -- /agnhost netexec --http-port=8080
kubectl -n conformance expose deployment echo --type LoadBalancer --port 80 --target-port 8080
kubectl -n conformance rollout status deployment/echo --timeout 3m
kubectl -n conformance wait --for=jsonpath='{.status.loadBalancer.ingress[0].ip}' service/echo --timeout 2m
ip="$(kubectl -n conformance get service echo -o jsonpath='{.status.loadBalancer.ingress[0].ip}')"
python3 -c "import ipaddress,sys; a,s,e=(ipaddress.ip_address(x) for x in sys.argv[1:]); sys.exit(0 if s<=a<=e else 1)" "$ip" "$start" "$stop" \
  || fail "the Service got $ip, outside the pool $start-$stop"
curl -fsS --retry 20 --retry-all-errors --retry-delay 3 --max-time 5 "http://$ip/hostname" >/dev/null \
  || fail "nothing answers at $ip: L2 announcements"
echo "LoadBalancer $ip answers from the runner"

step "5. NetworkPolicy is enforced (cluster-network-policies)"
kubectl -n conformance run client --image "$AGNHOST" --labels role=client --command -- /agnhost pause
kubectl -n conformance wait --for=condition=Ready pod/client --timeout 2m
reach() { kubectl -n conformance exec client -- /agnhost connect echo:80 --timeout 5s >/dev/null 2>&1; }
reach || fail "the client cannot reach echo before any policy"
cat > "$work/deny.yaml" <<'YAML'
policies:
  - name: echo-ingress
    namespace: conformance
    reason: "conformance: the first policy selecting echo makes its ingress default-deny"
    podSelector: {matchLabels: {app: echo}}
    policyTypes: [Ingress]
YAML
helm upgrade --install netpol charts/cluster-network-policies -f "$work/deny.yaml" --wait
for _ in $(seq 1 20); do reach || break; sleep 3; done
reach && fail "echo is still reachable under a deny-all ingress policy"
cat > "$work/allow.yaml" <<'YAML'
policies:
  - name: echo-ingress
    namespace: conformance
    reason: "conformance: only the client may reach echo, on its port"
    podSelector: {matchLabels: {app: echo}}
    policyTypes: [Ingress]
    ingress:
      - from: [{podSelector: {matchLabels: {role: client}}}]
        ports: [{port: 8080, protocol: TCP}]
YAML
helm upgrade --install netpol charts/cluster-network-policies -f "$work/allow.yaml" --wait
for _ in $(seq 1 20); do reach && break; sleep 3; done
reach || fail "the allowed client cannot reach echo"
echo "deny then allow: enforced"

step "6. local volumes bind on their node and hold data (local-volumes)"
node="$CLUSTER-worker"
docker exec "$node" mkdir -p /var/mnt/conformance/data
cat > "$work/volumes.yaml" <<YAML
storageClasses:
  local-conformance: {}
volumes:
  conformance-data:
    storageClass: local-conformance
    node: $node
    path: /var/mnt/conformance/data
    capacity: 1Gi
    claim: {namespace: conformance, name: data}
YAML
helm upgrade --install local-volumes charts/local-volumes -f "$work/volumes.yaml" --wait
kubectl apply -f - <<YAML
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: data, namespace: conformance}
spec:
  storageClassName: local-conformance
  accessModes: [ReadWriteOnce]
  resources: {requests: {storage: 1Gi}}
---
apiVersion: v1
kind: Pod
metadata: {name: writer, namespace: conformance}
spec:
  restartPolicy: Never
  containers:
    - name: writer
      image: $BUSYBOX
      command: [sh, -c, "echo conformance > /data/proof"]
      volumeMounts: [{name: data, mountPath: /data}]
  volumes: [{name: data, persistentVolumeClaim: {claimName: data}}]
YAML
kubectl -n conformance wait --for=jsonpath='{.status.phase}'=Succeeded pod/writer --timeout 3m
[ "$(kubectl -n conformance get pod writer -o jsonpath='{.spec.nodeName}')" = "$node" ] || fail "the writer did not run on the volume's node"
[ "$(docker exec "$node" cat /var/mnt/conformance/data/proof)" = conformance ] || fail "the data is not on the node's disk"
echo "bound to conformance-data on $node, data on disk"

step "7. the internal CA issues a certificate (cluster-pki)"
helm repo add jetstack https://charts.jetstack.io >/dev/null
helm upgrade --install cert-manager jetstack/cert-manager --version "$CERT_MANAGER_VERSION" -n cert-manager --create-namespace \
  --set crds.enabled=true --wait --timeout 10m
helm upgrade --install cluster-pki charts/cluster-pki -n cert-manager --wait
kubectl wait --for=condition=Ready clusterissuer/internal-ca --timeout 3m
kubectl apply -f - <<'YAML'
apiVersion: cert-manager.io/v1
kind: Certificate
metadata: {name: echo-tls, namespace: conformance}
spec:
  secretName: echo-tls
  dnsNames: [echo.conformance.svc]
  issuerRef: {name: internal-ca, kind: ClusterIssuer}
YAML
kubectl -n conformance wait --for=condition=Ready certificate/echo-tls --timeout 3m
echo "issued by internal-ca"

step "8. etcd backup objects pass the API server and Pod Security restricted (talos-etcd-backup)"
kubectl create namespace etcd-backup
kubectl label namespace etcd-backup pod-security.kubernetes.io/enforce=restricted pod-security.kubernetes.io/warn=restricted
helm template etcd-backup charts/talos-etcd-backup -n etcd-backup -f tests/lint/talos-etcd-backup.yaml \
  | python3 -c '
import sys
docs = [d for d in sys.stdin.read().split("\n---\n") if d.strip() and "apiVersion: talos.dev/" not in d]
print("\n---\n".join(docs))' > "$work/backup.yaml"
kubectl apply --dry-run=server -n etcd-backup -f "$work/backup.yaml" 2> "$work/backup.err" || { cat "$work/backup.err"; fail "the API server refuses the backup objects"; }
if grep -q "would violate PodSecurity" "$work/backup.err"; then cat "$work/backup.err"; fail "the backup job violates Pod Security restricted"; fi
echo "accepted, no Pod Security warning"

printf '\nconformance: all checks passed\n'
