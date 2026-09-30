package cluster_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/truvity/k8s/pkg/cluster"
)

func testCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func valid(t *testing.T) cluster.Outputs {
	return cluster.Outputs{
		Provider:                cluster.ProviderEKS,
		Name:                    "example",
		Endpoint:                "https://api.example.test",
		CertificateAuthorityPEM: testCA(t),
		OIDCIssuer:              "https://oidc.example.test/id/abc",
		Capabilities:            cluster.NewCapabilities(cluster.NodePools, cluster.WorkloadIdentity),
	}
}

func TestValidateAcceptsAWellFormedCluster(t *testing.T) {
	if err := valid(t).Validate(); err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
}

func TestValidateRefuses(t *testing.T) {
	cases := map[string]struct {
		mutate func(*cluster.Outputs)
		want   string
	}{
		"uppercase name":     {func(o *cluster.Outputs) { o.Name = "Example" }, "DNS-1123"},
		"empty name":         {func(o *cluster.Outputs) { o.Name = "" }, "DNS-1123"},
		"plain http":         {func(o *cluster.Outputs) { o.Endpoint = "http://api.example.test" }, "endpoint"},
		"no CA":              {func(o *cluster.Outputs) { o.CertificateAuthorityPEM = "" }, "certificateAuthorityPEM"},
		"identity no issuer": {func(o *cluster.Outputs) { o.OIDCIssuer = "" }, "workload-identity is offered"},
		"issuer no identity": {func(o *cluster.Outputs) { o.Capabilities = cluster.NewCapabilities(cluster.Storage) }, "not offered"},
		"unknown capability": {func(o *cluster.Outputs) { o.Capabilities["teleport"] = struct{}{} }, "unknown capability"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			o := valid(t)
			tc.mutate(&o)
			err := o.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	err := cluster.Outputs{}.Validate()
	if err == nil {
		t.Fatal("an empty Outputs must be refused")
	}
	for _, want := range []string{"name", "endpoint", "certificateAuthorityPEM"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestCapabilitiesListIsStable(t *testing.T) {
	got := cluster.NewCapabilities(cluster.APIAccess, cluster.NodePools, cluster.Storage).List()
	want := []cluster.Capability{cluster.NodePools, cluster.Storage, cluster.APIAccess}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
