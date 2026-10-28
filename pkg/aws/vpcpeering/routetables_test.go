package vpcpeering_test

import (
	"reflect"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/k8s/pkg/aws/vpcpeering"
)

func TestRouteTables(t *testing.T) {
	t.Parallel()

	got := vpcpeering.RouteTables("rtb-pub", map[string]string{"zone-a": "rtb-a", "zone-b": "rtb-b"})
	want := map[string]pulumi.StringInput{
		"public": pulumi.String("rtb-pub"),
		"zone-a": pulumi.String("rtb-a"),
		"zone-b": pulumi.String("rtb-b"),
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	if _, ok := vpcpeering.RouteTables("", map[string]string{"zone-a": "rtb-a"})[vpcpeering.PublicRouteTableKey]; ok {
		t.Fatal("an empty public id must not add a public entry")
	}
}
