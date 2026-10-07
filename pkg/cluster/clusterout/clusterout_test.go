package clusterout_test

import (
	"testing"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/k8s/pkg/cluster"
	"github.com/truvity/k8s/pkg/cluster/clusterout"
)

// await returns what the output resolves to, or false when it does not
// resolve (an apply that failed upstream never runs).
func await[T any](o pulumi.Output) (T, bool) {
	ch := make(chan T, 1)
	o.ApplyT(func(v T) T { ch <- v; return v })

	select {
	case v := <-ch:
		return v, true
	case <-time.After(2 * time.Second):
		var zero T
		return zero, false
	}
}

func TestFields(t *testing.T) {
	good := cluster.Outputs{Provider: cluster.ProviderTalos, Name: "c1", Endpoint: "https://c1.example:6443", OIDCIssuer: "https://issuer.example"}
	o := pulumi.ToOutput(good).(clusterout.Output)

	for name, tc := range map[string]struct {
		out  pulumi.StringOutput
		want string
	}{
		"Name":       {o.Name(), "c1"},
		"Endpoint":   {o.Endpoint(), good.Endpoint},
		"OIDCIssuer": {o.OIDCIssuer(), good.OIDCIssuer},
		"CA":         {o.CertificateAuthorityPEM(), ""},
	} {
		if got, ok := await[string](tc.out); !ok || got != tc.want {
			t.Errorf("%s() = %q, %v; want %q", name, got, ok, tc.want)
		}
	}
}

func TestValidatedStopsABadContract(t *testing.T) {
	bad := cluster.Outputs{Provider: cluster.ProviderTalos, Name: "Not A Label"}

	if _, ok := await[cluster.Outputs](pulumi.ToOutput(bad).(clusterout.Output).Validated()); ok {
		t.Error("an invalid contract resolved through Validated")
	}
}
