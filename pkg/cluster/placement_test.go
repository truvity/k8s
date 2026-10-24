package cluster_test

import (
	"strings"
	"testing"

	"github.com/truvity/k8s/pkg/cluster"
)

func TestValidatePoolZones(t *testing.T) {
	t.Parallel()

	azs := []string{"r-1a", "r-1b"}
	pools := map[string]cluster.NodePoolSpec{"a": {Zones: []string{"r-1a"}}, "b": {}}

	if err := cluster.ValidatePoolZones("c", pools, azs); err != nil {
		t.Fatal(err)
	}

	pools["b"] = cluster.NodePoolSpec{Zones: []string{"r-1z"}}

	err := cluster.ValidatePoolZones("c", pools, azs)
	if err == nil || !strings.Contains(err.Error(), `node_pools.b.zones names "r-1z"`) {
		t.Fatalf("got %v", err)
	}
}

func TestValidatePoolPodSubnets(t *testing.T) {
	t.Parallel()

	pools := map[string]cluster.NodePoolSpec{"a": {PodSubnets: true}}
	classes := map[string]cluster.NodeClassSpec{"k": {PodSubnets: true}}

	if err := cluster.ValidatePoolPodSubnets("c", pools, classes, true); err != nil {
		t.Fatal(err)
	}

	if err := cluster.ValidatePoolPodSubnets("c", pools, nil, false); err == nil {
		t.Fatal("pool: want error")
	}

	if err := cluster.ValidatePoolPodSubnets("c", nil, classes, false); err == nil {
		t.Fatal("class: want error")
	}

	if err := cluster.ValidatePoolPodSubnets("c", nil, nil, false); err != nil {
		t.Fatal(err)
	}
}
