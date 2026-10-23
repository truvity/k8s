package cluster_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/truvity/k8s/pkg/cluster"
)

func TestDeriveComputeClusterPoolsReplacePresetPools(t *testing.T) {
	t.Parallel()

	got, err := cluster.DeriveCompute(cluster.ComputeInputs{
		PresetPools: map[string]cluster.NodePoolSpec{
			"default": {Archs: []string{"arm64"}, Weight: 10},
			"batch":   {Archs: []string{"amd64"}},
		},
		ClusterPools: map[string]cluster.NodePoolSpec{
			"batch": {Archs: []string{"arm64"}, Dedicated: true},
			"ci":    {Archs: []string{"arm64"}, CPULimit: 12},
		},
		Defaults:    testDefaults,
		NodeRoleArn: nodeRole,
	})
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, p := range got.NodePools {
		names = append(names, p.Name)
	}

	if want := []string{"batch", "ci", "default"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("pools %v, want %v", names, want)
	}

	if batch := got.NodePools[0]; len(batch.Taints) != 2 || batch.Taints[0].Key != "batch" {
		t.Fatalf("the cluster's batch must replace the preset's: %+v", batch)
	}

	if len(got.NodeClasses) != 0 {
		t.Fatalf("no shared class asked for, got %+v", got.NodeClasses)
	}
}

func TestDeriveComputeDefaultClassAndPodSubnets(t *testing.T) {
	t.Parallel()

	got, err := cluster.DeriveCompute(cluster.ComputeInputs{
		PresetPools: map[string]cluster.NodePoolSpec{
			"plain": {},
			"own":   {EphemeralStorage: &cluster.EphemeralStorage{Size: "100Gi"}},
			"named": {NodeClass: "big"},
		},
		Classes: map[string]cluster.NodeClassSpec{
			"big":      {EphemeralStorage: cluster.EphemeralStorage{Size: "300Gi", IOPS: 4000}, PodSubnets: true},
			"standard": {EphemeralStorage: cluster.EphemeralStorage{Size: "100Gi"}},
		},
		DefaultClass: "standard",
		Defaults:     testDefaults,
		NodeRoleArn:  nodeRole,
	})
	if err != nil {
		t.Fatal(err)
	}

	byName := map[string]cluster.NodePool{}
	for _, p := range got.NodePools {
		byName[p.Name] = p
	}

	if byName["plain"].NodeClassName != "standard" {
		t.Errorf("plain must take the default class: %+v", byName["plain"])
	}

	if byName["named"].NodeClassName != "big" {
		t.Errorf("an explicit class must be kept: %+v", byName["named"])
	}

	if own := byName["own"]; own.NodeClassName != "" || own.NodeClass == nil {
		t.Errorf("a pool with its own class keeps it and takes no default: %+v", own)
	}

	if len(got.NodeClasses) != 2 || got.NodeClasses[0].Name != "big" || !got.NodeClasses[0].PodSubnets ||
		got.NodeClasses[0].EphemeralStorage.IOPS != 4000 || got.NodeClasses[1].PodSubnets {
		t.Errorf("shared classes (sorted, pod subnets carried): %+v", got.NodeClasses)
	}
}

func TestDeriveComputeNoPoolsIsZero(t *testing.T) {
	t.Parallel()

	got, err := cluster.DeriveCompute(cluster.ComputeInputs{
		Classes:  map[string]cluster.NodeClassSpec{"x": {EphemeralStorage: cluster.EphemeralStorage{Size: "1Gi"}}},
		Defaults: testDefaults,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(got.NodePools) != 0 || len(got.NodeClasses) != 0 {
		t.Fatalf("want zero, got %+v", got)
	}
}

func TestDeriveComputeRefusesOwnClassWithoutNodeRole(t *testing.T) {
	t.Parallel()

	_, err := cluster.DeriveCompute(cluster.ComputeInputs{
		PresetPools: map[string]cluster.NodePoolSpec{"own": {EphemeralStorage: &cluster.EphemeralStorage{Size: "100Gi"}}},
		Defaults:    testDefaults,
	})
	if err == nil || !strings.Contains(err.Error(), "autoNodeRoleArn") {
		t.Fatalf("want the node role refusal, got %v", err)
	}
}
