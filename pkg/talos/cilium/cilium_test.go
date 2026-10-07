package cilium_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/truvity/k8s/pkg/talos/cilium"
)

func TestDefaultsAreTalosSafe(t *testing.T) {
	v, err := cilium.Values(cilium.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if v["k8sServiceHost"] != "localhost" || v["k8sServicePort"] != 7445 || v["kubeProxyReplacement"] != true {
		t.Errorf("API access %v %v %v", v["k8sServiceHost"], v["k8sServicePort"], v["kubeProxyReplacement"])
	}

	cg := v["cgroup"].(map[string]any)
	if cg["autoMount"].(map[string]any)["enabled"] != false || cg["hostRoot"] != "/sys/fs/cgroup" {
		t.Errorf("cgroup %v", cg)
	}

	caps := v["securityContext"].(map[string]any)["capabilities"].(map[string]any)["ciliumAgent"].([]string)
	for _, c := range caps {
		if c == "SYS_MODULE" {
			t.Error("SYS_MODULE is in the agent's capabilities; Talos refuses it")
		}
	}

	if v["l2announcements"].(map[string]any)["enabled"] != true || v["externalIPs"].(map[string]any)["enabled"] != true {
		t.Error("L2 announcements are not on by default")
	}

	if _, ok := v["bgpControlPlane"]; ok {
		t.Error("BGP is on by default")
	}

	if _, ok := v["routingMode"]; ok {
		t.Error("native routing without a CIDR")
	}
}

func TestOptions(t *testing.T) {
	v, err := cilium.Values(cilium.Options{
		KubePrismPort: 7446, NativeRoutingCIDR: "10.244.0.0/16", Devices: []string{"eth+"},
		DisableL2Announcements: true, BGP: true, Hubble: true, OperatorReplicas: 1, PolicyAuditMode: true,
		Overrides: map[string]any{"bandwidthManager": map[string]any{"enabled": true}},
	})
	if err != nil {
		t.Fatal(err)
	}

	for key, want := range map[string]any{
		"k8sServicePort": 7446, "routingMode": "native", "autoDirectNodeRoutes": true,
		"ipv4NativeRoutingCIDR": "10.244.0.0/16", "policyAuditMode": true,
	} {
		if v[key] != want {
			t.Errorf("%s = %v, want %v", key, v[key], want)
		}
	}

	if _, ok := v["l2announcements"]; ok {
		t.Error("L2 announcements stayed on")
	}

	if v["bgpControlPlane"].(map[string]any)["enabled"] != true {
		t.Error("BGP is off")
	}

	if v["bandwidthManager"] == nil {
		t.Error("an override was dropped")
	}
}

func TestRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		o    cilium.Options
		want string
	}{
		"port":        {cilium.Options{KubePrismPort: 70000}, "not a port"},
		"replicas":    {cilium.Options{OperatorReplicas: -1}, "negative"},
		"cidr":        {cilium.Options{NativeRoutingCIDR: "10.244.0.1/16"}, "NativeRoutingCIDR"},
		"ipv6 cidr":   {cilium.Options{NativeRoutingCIDR: "fd00::/64"}, "NativeRoutingCIDR"},
		"no announce": {cilium.Options{DisableL2Announcements: true}, "nothing announces"},
		"device":      {cilium.Options{Devices: []string{""}}, "Devices[0] is empty"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := cilium.Values(tc.o); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

var update = flag.Bool("update", false, "rewrite testdata/values.yaml")

// TestGoldenDefaults pins the default values file the guide shows.
func TestGoldenDefaults(t *testing.T) {
	v, err := cilium.Values(cilium.Options{NativeRoutingCIDR: "10.244.0.0/16", Devices: []string{"eth0"}})
	if err != nil {
		t.Fatal(err)
	}

	got, err := yaml.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join("testdata", "values.yaml")
	if *update {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if string(want) != string(got) {
		t.Errorf("%s differs (run with -update and review):\n%s", path, got)
	}
}
