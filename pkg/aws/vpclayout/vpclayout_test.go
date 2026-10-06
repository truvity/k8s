package vpclayout_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/truvity/k8s/pkg/aws/vpclayout"
)

var plan = vpclayout.Plan{Base: 64}

var zones = map[string]int{"region-1a": 0, "region-1b": 1, "region-1c": 2, "other-1a": 0}

func TestPlanAddresses(t *testing.T) {
	t.Parallel()

	if got := plan.VPCCIDR(3); got != "10.67.0.0/16" {
		t.Fatalf("VPCCIDR: %s", got)
	}

	if got := plan.DNSIP(3); got != "10.67.0.2" {
		t.Fatalf("DNSIP: %s", got)
	}

	got, err := plan.PublicSubnetCIDR(vpclayout.Network{Index: 1, Subnets: map[string]vpclayout.Subnet{"public": {ThirdOctet: 6}}})
	if err != nil || got != "10.65.4.0/22" {
		t.Fatalf("PublicSubnetCIDR: %s %v", got, err)
	}

	if _, err := plan.PublicSubnetCIDR(vpclayout.Network{}); err == nil {
		t.Fatal("a network with no public subnet must be refused")
	}
}

func TestFilterAZs(t *testing.T) {
	t.Parallel()

	got := vpclayout.FilterAZs(zones, "region", "aws")
	if want := []vpclayout.AZ{{"region-1a", 0}, {"region-1b", 1}, {"region-1c", 2}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}

	if got := vpclayout.FilterAZs(zones, "nowhere", "aws"); len(got) != 0 {
		t.Fatalf("aws with no AZ gets none: %v", got)
	}

	if got := vpclayout.FilterAZs(zones, "site", "onprem"); !reflect.DeepEqual(got, []vpclayout.AZ{{"site", 0}}) {
		t.Fatalf("on-prem gets one implicit AZ: %v", got)
	}
}

func TestCalculate(t *testing.T) {
	t.Parallel()

	n := vpclayout.Network{Index: 1, Subnets: map[string]vpclayout.Subnet{
		"public":  {ThirdOctet: 0, Type: "public"},
		"private": {ThirdOctet: 4, Type: "private"},
		"pods":    {ThirdOctet: 32, Type: "private", PrefixLen: 19},
	}}

	got, err := plan.Calculate("n", n, vpclayout.FilterAZs(zones, "region", "aws"))
	if err != nil {
		t.Fatal(err)
	}

	byName := map[string]vpclayout.Calculated{}
	for _, c := range got {
		byName[c.Name] = c
	}

	if c := byName["public"]; c.CIDR != "10.65.0.0/22" || c.AZSubnets["region-1b"] != "10.65.1.0/24" {
		t.Fatalf("public: %+v", c)
	}

	if c := byName["private"]; c.CIDR != "10.65.4.0/22" || c.AZSubnets["region-1c"] != "10.65.6.0/24" || c.Type != "private" {
		t.Fatalf("private: %+v", c)
	}

	if c := byName["pods"]; c.CIDR != "10.65.32.0/19" || c.AZSubnets["region-1b"] != "10.65.64.0/19" {
		t.Fatalf("pods: %+v", c)
	}

	if got[0].Name != "public" || got[2].Name != "pods" {
		t.Fatalf("subnets come in third-octet order: %v", got)
	}

	// A single AZ: the logical block is the /24.
	single, err := plan.Calculate("n", vpclayout.Network{Subnets: map[string]vpclayout.Subnet{"a": {ThirdOctet: 5}}}, []vpclayout.AZ{{"site", 0}})
	if err != nil || single[0].CIDR != "10.64.5.0/24" {
		t.Fatalf("single AZ: %+v %v", single, err)
	}
}

func TestCalculateRefuses(t *testing.T) {
	t.Parallel()

	azs := vpclayout.FilterAZs(zones, "region", "aws")

	for name, tc := range map[string]struct {
		subnets map[string]vpclayout.Subnet
		azs     []vpclayout.AZ
		want    string
	}{
		"no azs":         {map[string]vpclayout.Subnet{"a": {}}, nil, "no availability zones"},
		"octet range":    {map[string]vpclayout.Subnet{"a": {ThirdOctet: 256}}, azs, "out of range 0-255"},
		"prefix range":   {map[string]vpclayout.Subnet{"a": {PrefixLen: 12}}, azs, "out of range 16-24"},
		"misaligned":     {map[string]vpclayout.Subnet{"a": {ThirdOctet: 16, PrefixLen: 19}}, azs, "multiple of 32"},
		"overlap":        {map[string]vpclayout.Subnet{"a": {ThirdOctet: 0}, "b": {ThirdOctet: 0, PrefixLen: 19}}, azs, "overlap"},
		"slot too large": {map[string]vpclayout.Subnet{"a": {}}, []vpclayout.AZ{{"x", 0}, {"y", 4}}, "exceeds /22 block capacity"},
		"overflow":       {map[string]vpclayout.Subnet{"a": {ThirdOctet: 192, PrefixLen: 19}}, azs, "overflows"},
	} {
		_, err := plan.Calculate("n", vpclayout.Network{Subnets: tc.subnets}, tc.azs)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", name, err, tc.want)
		}
	}
}

func TestNames(t *testing.T) {
	t.Parallel()

	if got := vpclayout.SubnetName("devel", "private", "region-1b"); got != "devel-private-b" {
		t.Fatalf("SubnetName: %s", got)
	}

	if vpclayout.AZSuffix("") != "" || vpclayout.AZSuffix("region-1c") != "c" {
		t.Fatal("AZSuffix")
	}
}
