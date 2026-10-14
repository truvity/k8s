package vpclayout_test

import (
	"reflect"
	"testing"

	"github.com/truvity/k8s/pkg/aws/vpclayout"
)

// Four networks: a hub (0), two spokes inside 10.64.0.0/14 (1, 2) and a
// spoke outside it (4) that runs no router.
var (
	fleet = map[string]int{"hub": 0, "a": 1, "b": 2, "far": 4}

	peering = vpclayout.Peering{Hub: "hub", Connections: []vpclayout.Connection{
		{Name: "hub-a", Spoke: "a"},
		{Name: "hub-far", Spoke: "far"},
		{Name: "hub-b", Spoke: "b"},
	}}

	routers = map[string]bool{"hub": true, "a": true, "b": true}
)

func hasRouter(name string) bool { return routers[name] }

func TestValidateIndexes(t *testing.T) {
	t.Parallel()

	if err := vpclayout.ValidateIndexes(fleet); err != nil {
		t.Fatal(err)
	}

	if err := vpclayout.ValidateIndexes(map[string]int{"x": 3, "y": 3}); err == nil {
		t.Fatal("two networks with one index must be refused")
	}
}

func TestPeeringValidate(t *testing.T) {
	t.Parallel()

	if err := peering.Validate(fleet); err != nil {
		t.Fatal(err)
	}

	if err := (vpclayout.Peering{Hub: "nowhere"}).Validate(fleet); err != nil {
		t.Fatalf("no connection is valid whatever the hub: %v", err)
	}

	for name, p := range map[string]vpclayout.Peering{
		"unknown hub":     {Hub: "nope", Connections: []vpclayout.Connection{{Name: "c", Spoke: "a"}}},
		"unknown spoke":   {Hub: "hub", Connections: []vpclayout.Connection{{Name: "c", Spoke: "nope"}}},
		"empty name":      {Hub: "hub", Connections: []vpclayout.Connection{{Spoke: "a"}}},
		"duplicate name":  {Hub: "hub", Connections: []vpclayout.Connection{{Name: "c", Spoke: "a"}, {Name: "c", Spoke: "b"}}},
		"duplicate spoke": {Hub: "hub", Connections: []vpclayout.Connection{{Name: "c", Spoke: "a"}, {Name: "d", Spoke: "a"}}},
		"hub with itself": {Hub: "hub", Connections: []vpclayout.Connection{{Name: "c", Spoke: "hub"}}},
	} {
		if err := p.Validate(fleet); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestSpokeCIDRsOutside(t *testing.T) {
	t.Parallel()

	got, err := plan.SpokeCIDRsOutside("10.64.0.0/14", peering, fleet)
	if err != nil || !reflect.DeepEqual(got, []string{"10.68.0.0/16"}) {
		t.Fatalf("got %v %v", got, err)
	}

	got, err = plan.SpokeCIDRsOutside("", peering, fleet)
	if err != nil || !reflect.DeepEqual(got, []string{"10.65.0.0/16", "10.66.0.0/16", "10.68.0.0/16"}) {
		t.Fatalf("no supernet covers nothing: %v %v", got, err)
	}

	if _, err := plan.SpokeCIDRsOutside("not-a-cidr", peering, fleet); err == nil {
		t.Fatal("a malformed supernet must be refused")
	}

	if _, err := plan.SpokeCIDRsOutside("", vpclayout.Peering{Hub: "hub", Connections: []vpclayout.Connection{{Name: "c", Spoke: "nope"}}}, fleet); err == nil {
		t.Fatal("an unknown spoke must be refused")
	}
}

func TestRouterlessSpokes(t *testing.T) {
	t.Parallel()

	if got := peering.RouterlessSpokes(fleet, hasRouter); !reflect.DeepEqual(got, []string{"far"}) {
		t.Fatalf("RouterlessSpokes: %v", got)
	}

	if got := plan.RouterlessSpokeCIDRs("hub", peering, fleet, hasRouter); !reflect.DeepEqual(got, []string{"10.68.0.0/16"}) {
		t.Fatalf("RouterlessSpokeCIDRs(hub): %v", got)
	}

	for _, name := range []string{"a", "b", "far"} {
		if got := plan.RouterlessSpokeCIDRs(name, peering, fleet, hasRouter); len(got) != 0 {
			t.Errorf("RouterlessSpokeCIDRs(%s) = %v, want none: only the hub has peers", name, got)
		}
	}

	all := func(string) bool { return true }
	if got := peering.RouterlessSpokes(fleet, all); len(got) != 0 {
		t.Fatalf("every spoke has a router: %v", got)
	}
}
