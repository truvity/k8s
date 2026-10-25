package vpclayout_test

import (
	"reflect"
	"testing"

	"github.com/truvity/k8s/pkg/aws/vpclayout"
)

func TestHTTPSIngressCIDRs(t *testing.T) {
	t.Parallel()

	const super = "10.64.0.0/14"

	got, err := plan.HTTPSIngressCIDRs("hub", super, peering, fleet, true)
	if err != nil || !reflect.DeepEqual(got, []string{super, "10.68.0.0/16"}) {
		t.Fatalf("hub: %v %v", got, err)
	}

	got, err = plan.HTTPSIngressCIDRs("a", super, peering, fleet, true)
	if err != nil || !reflect.DeepEqual(got, []string{super}) {
		t.Fatalf("spoke: %v %v", got, err)
	}

	got, err = plan.HTTPSIngressCIDRs("hub", super, peering, fleet, false)
	if err != nil || got != nil {
		t.Fatalf("no private subnet serves nothing: %v %v", got, err)
	}

	if _, err := plan.HTTPSIngressCIDRs("hub", "bad", peering, fleet, true); err == nil {
		t.Fatal("a malformed supernet must be refused")
	}
}

func TestHubEgressCIDRs(t *testing.T) {
	t.Parallel()

	const super = "10.64.0.0/14"

	got, err := plan.HubEgressCIDRs("hub", super, peering, fleet, true)
	if err != nil || !reflect.DeepEqual(got, []string{"10.68.0.0/16"}) {
		t.Fatalf("hub: %v %v", got, err)
	}

	for name, c := range map[string]struct {
		net     string
		private bool
	}{"spoke": {"a", true}, "hub without private": {"hub", false}} {
		got, err := plan.HubEgressCIDRs(c.net, super, peering, fleet, c.private)
		if err != nil || got != nil {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
}

func TestRouterlessSpokeIngress(t *testing.T) {
	t.Parallel()

	hub := vpclayout.Network{Index: 0, Subnets: map[string]vpclayout.Subnet{"public": {ThirdOctet: 6}}}
	subnets := []vpclayout.Calculated{
		{Name: "public", CIDR: "10.64.4.0/22"},
		{Name: "cp", CIDR: "10.64.8.0/22"},
		{Name: "svc", CIDR: "10.64.12.0/22"},
		{Name: "worker", CIDR: "10.64.16.0/22"},
	}

	got, ok, err := plan.RouterlessSpokeIngress("far", peering, fleet, hasRouter, hub, subnets, "cp", "worker")
	want := vpclayout.HubSources{PublicBlock: "10.64.4.0/22", NodeCIDRs: []string{"10.64.8.0/22", "10.64.16.0/22"}}

	if err != nil || !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("far: %+v %v %v", got, ok, err)
	}

	for _, name := range []string{"hub", "a", "b"} {
		if _, ok, err := plan.RouterlessSpokeIngress(name, peering, fleet, hasRouter, hub, subnets, "cp"); ok || err != nil {
			t.Errorf("%s is not a routerless spoke: %v %v", name, ok, err)
		}
	}

	if _, _, err := plan.RouterlessSpokeIngress("far", peering, fleet, hasRouter, hub, subnets, "nodes"); err == nil {
		t.Fatal("a hub with no node subnet must be refused")
	}

	if _, _, err := plan.RouterlessSpokeIngress("far", peering, fleet, hasRouter, vpclayout.Network{}, subnets, "cp"); err == nil {
		t.Fatal("a hub with no public subnet must be refused")
	}
}
