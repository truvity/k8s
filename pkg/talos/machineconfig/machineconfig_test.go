package machineconfig_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/siderolabs/talos/pkg/machinery/config/configloader"
	"github.com/siderolabs/talos/pkg/machinery/config/encoder"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	"go.yaml.in/yaml/v4"

	"github.com/truvity/k8s/pkg/cluster"
	"github.com/truvity/k8s/pkg/talos/machineconfig"
	"github.com/truvity/k8s/pkg/talos/schematic"
)

var update = flag.Bool("update", false, "rewrite the golden files under testdata/")

const (
	talosVersion = "v1.14.2"
	schematicID  = "376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba" // the stock image
	boardID      = "ee21ef4a5ef808a9b7484cc0dda0f25075021691c8c09a276591eedb638ea1f9"
)

var (
	bundleOnce sync.Once
	bundles    [2]*secrets.Bundle
)

// testBundles returns two independent secrets bundles, generated once.
func testBundles(t *testing.T) [2]*secrets.Bundle {
	t.Helper()

	bundleOnce.Do(func() {
		for i := range bundles {
			b, err := machineconfig.NewSecrets(talosVersion)
			if err != nil {
				panic(err)
			}

			bundles[i] = b
		}
	})

	return bundles
}

// example is a small cluster using every field: three control plane nodes
// behind a VIP, a worker with local volumes and one on its own board image.
func example() *machineconfig.Cluster {
	return &machineconfig.Cluster{
		Name:              "example",
		Endpoint:          "https://10.0.0.10:6443",
		TalosVersion:      talosVersion,
		KubernetesVersion: "1.36.4",
		Installer:         schematic.Installer{SchematicID: schematicID},
		Network: machineconfig.Network{
			PodSubnets:     []string{"10.244.0.0/16"},
			ServiceSubnets: []string{"10.96.0.0/12"},
			NodeSubnets:    []string{"10.0.0.0/24"},
		},
		ControlPlane: machineconfig.ControlPlane{
			VIP:      "10.0.0.10",
			CertSANs: []string{"api.example.com"},
		},
		ServiceAccountIssuer: "https://oidc.example.com/example",
		TalosAPIAccess:       &machineconfig.TalosAPIAccess{Roles: []string{"os:etcd:backup"}, Namespaces: []string{"etcd-backup"}},
		Capabilities:         []cluster.Capability{cluster.APIAccess, cluster.WorkloadIdentity, cluster.NetworkPolicy, cluster.LoadBalancing, cluster.Storage},
		Patches: []string{`apiVersion: v1alpha1
kind: RegistryMirrorConfig
name: docker.io
endpoints:
  - url: https://mirror.example.com/v2/docker.io
    overridePath: true
`},
		ControlPlanePatches: []string{`apiVersion: v1alpha1
kind: KubeNodeConfig
taints:
  node-role.kubernetes.io/control-plane: PreferNoSchedule
`},
		Nodes: []machineconfig.Node{
			{Hostname: "cp-1", Role: machineconfig.RoleControlPlane, Address: "10.0.0.11", InstallDisk: "/dev/nvme0n1", VIPLink: "eth0"},
			{Hostname: "cp-2", Role: machineconfig.RoleControlPlane, Address: "10.0.0.12", InstallDisk: "/dev/nvme0n1", VIPLink: "eth0"},
			{Hostname: "cp-3", Role: machineconfig.RoleControlPlane, Address: "10.0.0.13", InstallDisk: "/dev/nvme0n1", VIPLink: "eth0"},
			{
				Hostname: "db-1", Role: machineconfig.RoleWorker, Address: "10.0.0.21",
				InstallDiskSelector: `disk.transport == "nvme"`,
				Labels:              map[string]string{"example.com/storage": "local"},
				Taints:              map[string]string{"example.com/storage": "local:NoSchedule"},
				LocalVolumes: []machineconfig.LocalVolume{
					{Name: "local-db", DiskSelector: `disk.transport == "nvme" && !system_disk`, MinSize: "100GiB", MaxSize: "400GiB", Encryption: "tpm"},
					{Name: "bulk", DiskSelector: `disk.rotational`, MinSize: "10GiB", Filesystem: "ext4"},
				},
			},
			{
				Hostname: "board-1", Role: machineconfig.RoleWorker, Address: "10.0.0.31", InstallDisk: "/dev/mmcblk0",
				Installer: &schematic.Installer{SchematicID: boardID},
				Patches: []string{`apiVersion: v1alpha1
kind: KubeletConfig
extraArgs:
  rotate-server-certificates: "true"
`},
			},
		},
	}
}

type doc = map[string]any

// documents parses a rendered config into its documents, keyed by
// "<kind>" or "<kind>/<name>"; the legacy document is "v1alpha1".
func documents(t *testing.T, data []byte) map[string]doc {
	t.Helper()

	out := map[string]doc{}
	dec := yaml.NewDecoder(bytes.NewReader(data))

	for {
		var d doc
		if err := dec.Decode(&d); err != nil {
			break
		}

		key := "v1alpha1"
		if k, ok := d["kind"].(string); ok {
			key = k
			if n, ok := d["name"].(string); ok {
				key += "/" + n
			}
		}

		if _, dup := out[key]; dup {
			t.Fatalf("document %s appears twice", key)
		}

		out[key] = d
	}

	return out
}

func get(d doc, path ...string) any {
	var cur any = d

	for _, p := range path {
		m, ok := cur.(doc)
		if !ok {
			return nil
		}

		cur = m[p]
	}

	return cur
}

func render(t *testing.T, c *machineconfig.Cluster) *machineconfig.Rendered {
	t.Helper()

	out, err := machineconfig.Render(c, testBundles(t)[0])
	if err != nil {
		t.Fatal(err)
	}

	return out
}

func TestEveryConfigLoadsBackInTalos(t *testing.T) {
	out := render(t, example())

	if len(out.Nodes) != 5 {
		t.Fatalf("rendered %d nodes", len(out.Nodes))
	}

	for host, data := range out.Nodes {
		if _, err := configloader.NewFromBytes(data); err != nil {
			t.Errorf("%s: Talos does not load its own config: %v", host, err)
		}
	}

	if !bytes.Contains(out.Talosconfig, []byte("10.0.0.11")) || bytes.Contains(out.Talosconfig, []byte("10.0.0.21")) {
		t.Errorf("talosconfig endpoints are not the control plane nodes:\n%s", out.Talosconfig)
	}

	if len(out.Warnings) != 0 {
		t.Errorf("warnings: %v", out.Warnings)
	}
}

func TestTheDeclaredSettingsReachTheConfig(t *testing.T) {
	out := render(t, example())

	cp := documents(t, out.Nodes["cp-1"])
	db := documents(t, out.Nodes["db-1"])
	board := documents(t, out.Nodes["board-1"])

	for name, d := range map[string]map[string]doc{"cp-1": cp, "db-1": db, "board-1": board} {
		for _, kind := range []string{"KubeFlannelCNIConfig", "KubeExternalManifestConfig/custom-cni"} {
			if _, ok := d[kind]; ok {
				t.Errorf("%s: %s is rendered; the CNI is the caller's Cilium", name, kind)
			}
		}

		if got := get(d["RegistryMirrorConfig/docker.io"], "name"); got != "docker.io" {
			t.Errorf("%s: the cluster-wide patch did not apply", name)
		}
	}

	if got := get(cp["UnattendedInstallConfig"], "installer", "image"); got != "factory.talos.dev/metal-installer/"+schematicID+":"+talosVersion {
		t.Errorf("cp-1 installer %v", got)
	}

	if got := get(cp["UnattendedInstallConfig"], "provisioning", "diskSelector", "match"); got != `disk.dev_path == "/dev/nvme0n1"` {
		t.Errorf("cp-1 install disk selector %v", got)
	}

	if got := get(db["UnattendedInstallConfig"], "provisioning", "diskSelector", "match"); got != `disk.transport == "nvme"` {
		t.Errorf("db-1 install disk selector %v", got)
	}

	if got := get(board["UnattendedInstallConfig"], "installer", "image"); got != "factory.talos.dev/metal-installer/"+boardID+":"+talosVersion {
		t.Errorf("board-1 installer %v: its own schematic did not win", got)
	}

	if got := get(cp["KubeProxyConfig"], "enabled"); got != false {
		t.Errorf("cp-1: kube-proxy enabled = %v, want false", got)
	}

	if got := get(cp["HostnameConfig"], "hostname"); got != "cp-1" {
		t.Errorf("cp-1 hostname %v", got)
	}

	if got := get(cp["HostnameConfig"], "auto"); got != nil {
		t.Errorf("cp-1 hostname is also auto: %v", got)
	}

	if got := get(cp["KubeServiceAccountConfig"], "issuer", "issuerURL"); got != "https://oidc.example.com/example" {
		t.Errorf("issuer %v", got)
	}

	if got := get(cp["KubeServiceAccountConfig"], "accepted", "issuers"); len(got.([]any)) != 1 || got.([]any)[0] != "https://10.0.0.10:6443" {
		t.Errorf("accepted issuers %v", got)
	}

	if get(cp["KubeServiceAccountConfig"], "issuer", "privateKey") == "" {
		t.Error("the patch dropped the ServiceAccount key")
	}

	if got := get(cp["KubeAPIServerConfig"], "extraArgs", "service-account-jwks-uri"); got != "https://oidc.example.com/example/openid/v1/jwks" {
		t.Errorf("jwks uri %v", got)
	}

	if got := get(cp["Layer2VIPConfig/10.0.0.10"], "link"); got != "eth0" {
		t.Errorf("vip link %v", got)
	}

	if _, ok := db["Layer2VIPConfig/10.0.0.10"]; ok {
		t.Error("a worker holds the VIP")
	}

	if got := get(cp["KubeTalosAPIAccessConfig"], "allowedRoles"); len(got.([]any)) != 1 {
		t.Errorf("talos api access %v", got)
	}

	if got := get(cp["v1alpha1"], "cluster", "etcd", "advertisedSubnets"); len(got.([]any)) != 1 {
		t.Errorf("etcd advertisedSubnets %v", got)
	}

	if got := get(db["KubeNodeConfig"], "nodeIP", "validSubnets"); len(got.([]any)) != 1 {
		t.Errorf("kubelet validSubnets %v", got)
	}

	if got := get(cp["KubeNodeConfig"], "taints", "node-role.kubernetes.io/control-plane"); got != "PreferNoSchedule" {
		t.Errorf("control plane taint %v: the role patch did not win over the generated one", got)
	}

	if got := get(db["KubeNodeConfig"], "taints", "example.com/storage"); got != "local:NoSchedule" {
		t.Errorf("db-1 taint %v", got)
	}

	if got := get(db["UserVolumeConfig/local-db"], "provisioning", "maxSize"); got != "400GiB" {
		t.Errorf("local-db maxSize %v", got)
	}

	if got := get(db["UserVolumeConfig/local-db"], "encryption", "provider"); got != "luks2" {
		t.Errorf("local-db encryption %v", got)
	}

	if got := get(db["UserVolumeConfig/bulk"], "filesystem", "type"); got != "ext4" {
		t.Errorf("bulk filesystem %v", got)
	}

	if got := get(board["KubeletConfig"], "extraArgs", "rotate-server-certificates"); got != "true" {
		t.Errorf("the node's own patch did not apply: %v", got)
	}
}

func TestFlannelKeepsTalosDefaults(t *testing.T) {
	c := example()
	c.Network.CNI = machineconfig.CNIFlannel
	c.Network.KubeProxy = true

	d := documents(t, render(t, c).Nodes["cp-1"])

	if _, ok := d["KubeFlannelCNIConfig"]; !ok {
		t.Error("flannel is missing")
	}

	if got := get(d["KubeProxyConfig"], "enabled"); got == false {
		t.Error("kube-proxy is disabled")
	}
}

func TestNoIssuerLeavesTheDefault(t *testing.T) {
	c := example()
	c.ServiceAccountIssuer = ""
	c.Capabilities = nil

	out := render(t, c)
	d := documents(t, out.Nodes["cp-1"])

	if got := get(d["KubeServiceAccountConfig"], "issuer", "issuerURL"); got != c.Endpoint {
		t.Errorf("issuer %v, want the endpoint", got)
	}

	if out.Contract.OIDCIssuer != "" || out.Contract.Capabilities.Has(cluster.WorkloadIdentity) {
		t.Errorf("contract offers workload identity without an issuer: %+v", out.Contract)
	}

	if err := out.Contract.Validate(); err != nil {
		t.Error(err)
	}
}

func TestContract(t *testing.T) {
	out := render(t, example())

	if err := out.Contract.Validate(); err != nil {
		t.Fatal(err)
	}

	if out.Contract.Provider != cluster.ProviderTalos || out.Contract.OIDCIssuer != "https://oidc.example.com/example" {
		t.Errorf("contract %+v", out.Contract)
	}

	if out.Contract.CertificateAuthorityPEM != string(testBundles(t)[0].Certs.K8s.Crt) {
		t.Error("the contract's CA is not the bundle's Kubernetes CA")
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	a, b := render(t, example()), render(t, example())

	for host := range a.Nodes {
		if !bytes.Equal(a.Nodes[host], b.Nodes[host]) {
			t.Errorf("%s: two renders differ", host)
		}
	}
}

func TestSecretsRoundTrip(t *testing.T) {
	b := testBundles(t)[0]

	data, err := machineconfig.MarshalSecrets(b)
	if err != nil {
		t.Fatal(err)
	}

	back, err := machineconfig.ParseSecrets(data)
	if err != nil {
		t.Fatal(err)
	}

	r1, err := machineconfig.Render(example(), b)
	if err != nil {
		t.Fatal(err)
	}

	r2, err := machineconfig.Render(example(), back)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(r1.Nodes["cp-1"], r2.Nodes["cp-1"]) {
		t.Error("a parsed bundle renders another config")
	}

	if _, err := machineconfig.ParseSecrets([]byte("cluster: {}\n")); err == nil {
		t.Error("an incomplete bundle parsed")
	}
}

func TestEvenControlPlaneWarns(t *testing.T) {
	c := example()
	c.Nodes = c.Nodes[1:]

	if out := render(t, c); len(out.Warnings) == 0 || !strings.Contains(out.Warnings[0], "odd number") {
		t.Errorf("warnings %v", out.Warnings)
	}
}

func TestRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(c *machineconfig.Cluster)
		want   string
	}{
		"name":            {func(c *machineconfig.Cluster) { c.Name = "Example" }, "not a DNS-1123 label"},
		"endpoint scheme": {func(c *machineconfig.Cluster) { c.Endpoint = "http://10.0.0.10:6443" }, "endpoint"},
		"endpoint port":   {func(c *machineconfig.Cluster) { c.Endpoint = "https://10.0.0.10" }, "endpoint"},
		"talos minor":     {func(c *machineconfig.Cluster) { c.TalosVersion = "v1.13.4" }, "renders for Talos v1.14.x only"},
		"talos format":    {func(c *machineconfig.Cluster) { c.TalosVersion = "1.14.2" }, "not vX.Y.Z"},
		"k8s too new":     {func(c *machineconfig.Cluster) { c.KubernetesVersion = "1.40.0" }, "kubernetesVersion 1.40.0"},
		"k8s with v":      {func(c *machineconfig.Cluster) { c.KubernetesVersion = "v1.36.4" }, "no leading v"},
		"no schematic":    {func(c *machineconfig.Cluster) { c.Installer = schematic.Installer{} }, "64-hex"},
		"pod cidr":        {func(c *machineconfig.Cluster) { c.Network.PodSubnets = []string{"10.244.0.1/16"} }, "host bits"},
		"cni":             {func(c *machineconfig.Cluster) { c.Network.CNI = "calico" }, "network.cni"},
		"flannel proxy":   {func(c *machineconfig.Cluster) { c.Network.CNI = machineconfig.CNIFlannel }, "flannel needs kube-proxy"},
		"vip":             {func(c *machineconfig.Cluster) { c.ControlPlane.VIP = "vip" }, "not an IP"},
		"vip link":        {func(c *machineconfig.Cluster) { c.Nodes[0].VIPLink = "" }, "needs vipLink"},
		"worker vip link": {func(c *machineconfig.Cluster) { c.Nodes[3].VIPLink = "eth0" }, "vipLink on a worker"},
		"issuer slash":    {func(c *machineconfig.Cluster) { c.ServiceAccountIssuer = "https://oidc.example.com/" }, "trailing slash"},
		"issuer http":     {func(c *machineconfig.Cluster) { c.ServiceAccountIssuer = "http://oidc.example.com" }, "serviceAccountIssuer"},
		"api role":        {func(c *machineconfig.Cluster) { c.TalosAPIAccess.Roles = []string{"os:root"} }, "unknown role"},
		"api empty":       {func(c *machineconfig.Cluster) { c.TalosAPIAccess.Namespaces = nil }, "at least one role and one namespace"},
		"capability":      {func(c *machineconfig.Cluster) { c.Capabilities = []cluster.Capability{"gpu"} }, "unknown capability"},
		"no cp":           {func(c *machineconfig.Cluster) { c.Nodes = c.Nodes[3:] }, "no control plane node"},
		"dup host":        {func(c *machineconfig.Cluster) { c.Nodes[1].Hostname = "cp-1" }, "hostname repeated"},
		"dup address":     {func(c *machineconfig.Cluster) { c.Nodes[1].Address = "10.0.0.11" }, "repeated"},
		"bad address":     {func(c *machineconfig.Cluster) { c.Nodes[1].Address = "cp-2.example.com" }, "not an IP"},
		"role":            {func(c *machineconfig.Cluster) { c.Nodes[3].Role = "etcd" }, "role \"etcd\""},
		"two disks":       {func(c *machineconfig.Cluster) { c.Nodes[0].InstallDiskSelector = "true" }, "exactly one of installDisk"},
		"no disk":         {func(c *machineconfig.Cluster) { c.Nodes[0].InstallDisk = "" }, "exactly one of installDisk"},
		"disk path":       {func(c *machineconfig.Cluster) { c.Nodes[0].InstallDisk = "nvme0n1" }, "not a /dev path"},
		"volume name":     {func(c *machineconfig.Cluster) { c.Nodes[3].LocalVolumes[0].Name = "Local" }, "not a DNS-1123 label"},
		"volume repeat":   {func(c *machineconfig.Cluster) { c.Nodes[3].LocalVolumes[1].Name = "local-db" }, "name repeated"},
		"volume disk":     {func(c *machineconfig.Cluster) { c.Nodes[3].LocalVolumes[0].DiskSelector = " " }, "diskSelector is required"},
		"volume size":     {func(c *machineconfig.Cluster) { c.Nodes[3].LocalVolumes[0].MaxSize = "big" }, "not a size"},
		"volume no size":  {func(c *machineconfig.Cluster) { c.Nodes[3].LocalVolumes[1].MinSize = "" }, "minSize or maxSize is required"},
		"volume fs":       {func(c *machineconfig.Cluster) { c.Nodes[3].LocalVolumes[0].Filesystem = "btrfs" }, "filesystem"},
		"volume crypt":    {func(c *machineconfig.Cluster) { c.Nodes[3].LocalVolumes[0].Encryption = "static" }, "encryption"},
		"node installer":  {func(c *machineconfig.Cluster) { c.Nodes[4].Installer.SchematicID = "x" }, "64-hex"},
		"bad patch":       {func(c *machineconfig.Cluster) { c.Patches = []string{"kind: NoSuchConfig\napiVersion: v1alpha1\n"} }, "load patches"},
		"invalid result": {func(c *machineconfig.Cluster) {
			c.Nodes[0].Patches = []string{"apiVersion: v1alpha1\nkind: Layer2VIPConfig\nname: not-an-ip\nlink: eth0\n"}
		}, "validate"},
	} {
		t.Run(name, func(t *testing.T) {
			c := example()
			tc.mutate(c)

			_, err := machineconfig.Render(c, testBundles(t)[0])
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestRefusalReportsEveryProblem(t *testing.T) {
	c := example()
	c.Name, c.Endpoint, c.Nodes[0].Address = "", "", ""

	err := c.Validate()
	for _, want := range []string{"name", "endpoint", "address"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

var (
	pemBlock = regexp.MustCompile(`(?m)^(\s*)-----BEGIN [A-Z ]+-----\n(?:\s*[A-Za-z0-9+/=]+\n)+\s*-----END [A-Z ]+-----\n?`)
	b64Value = regexp.MustCompile(`: (LS0t|[A-Za-z0-9+/]{40,}={0,2})[A-Za-z0-9+/=]*$`)
	idValue  = regexp.MustCompile(`(?m)^(\s*(?:clusterID|id|token|secret): ).+$`)
)

// redact masks every secret and every value a fresh bundle changes, so the
// golden shows the shape of a node's config and nothing random.
func redact(t *testing.T, data []byte) []byte {
	t.Helper()

	cfg, err := configloader.NewFromBytes(data)
	if err != nil {
		t.Fatal(err)
	}

	out, err := cfg.RedactSecrets("<redacted>").EncodeBytes(encoder.WithComments(encoder.CommentsDisabled))
	if err != nil {
		t.Fatal(err)
	}

	out = pemBlock.ReplaceAll(out, []byte("${1}<pem>\n"))

	var b bytes.Buffer

	for line := range strings.SplitSeq(string(out), "\n") {
		line = b64Value.ReplaceAllString(line, ": <base64>")
		line = idValue.ReplaceAllString(line, "${1}<random>")
		b.WriteString(line + "\n")
	}

	return bytes.TrimRight(b.Bytes(), "\n")
}

// TestGolden pins, per node, the patch the Cluster's fields make and the
// whole config with its secrets masked. Two bundles must give the same
// golden: anything random that leaks through the masking fails here.
// Regenerate with: go test ./pkg/talos/machineconfig -update
func TestGolden(t *testing.T) {
	c := example()
	bs := testBundles(t)

	r0, err := machineconfig.Render(c, bs[0])
	if err != nil {
		t.Fatal(err)
	}

	r1, err := machineconfig.Render(c, bs[1])
	if err != nil {
		t.Fatal(err)
	}

	for _, n := range c.Nodes {
		patch, err := c.RenderedPatch(n.Hostname)
		if err != nil {
			t.Fatal(err)
		}

		full := redact(t, r0.Nodes[n.Hostname])
		if other := redact(t, r1.Nodes[n.Hostname]); !bytes.Equal(full, other) {
			t.Errorf("%s: the masked config differs between two bundles; mask what leaks", n.Hostname)
		}

		check(t, filepath.Join("testdata", "patch", n.Hostname+".yaml"), []byte(patch))
		check(t, filepath.Join("testdata", "config", n.Hostname+".yaml"), append(full, '\n'))
	}
}

func check(t *testing.T, path string, got []byte) {
	t.Helper()

	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}

		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}

	if !bytes.Equal(want, got) {
		t.Errorf("%s differs from the render (run with -update and review the diff)", path)
	}
}
