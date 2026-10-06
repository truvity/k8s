package cluster_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/truvity/k8s/pkg/cluster"
)

var testDefaults = cluster.PoolDefaults{
	Archs:         []string{"arm64"},
	CapacityTypes: []string{"spot", "on-demand"},
	Categories:    []string{"m", "r"},
}

const nodeRole = "arn:aws:iam::123456789012:role/example-eks-auto-node"

func TestDeriveNodePools(t *testing.T) {
	t.Parallel()

	pools, err := cluster.DeriveNodePools(map[string]cluster.NodePoolSpec{
		"default": {Archs: []string{"arm64"}, Weight: 10},
		"compute": {Archs: []string{"amd64"}},
		"ci":      {Archs: []string{"arm64"}, Dedicated: true, CPULimit: 12},
		"bare":    {},
	}, testDefaults, "")
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, p := range pools {
		names = append(names, p.Name)
	}

	if want := []string{"bare", "ci", "compute", "default"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("order %v, want %v", names, want)
	}

	bare, ci, compute := pools[0], pools[1], pools[2]
	if !reflect.DeepEqual(bare.Categories, []string{"m", "r"}) || !reflect.DeepEqual(bare.CapacityTypes, []string{"spot", "on-demand"}) ||
		!reflect.DeepEqual(bare.Archs, []string{"arm64"}) {
		t.Fatalf("defaults not applied: %+v", bare)
	}

	wantCI := []cluster.Taint{{Key: "ci", Value: "true", Effect: "NoSchedule"}, {Key: "arch", Value: "arm64", Effect: "NoSchedule"}}
	if !reflect.DeepEqual(ci.Taints, wantCI) || ci.CPULimit != 12 {
		t.Fatalf("ci: %+v", ci)
	}

	if len(compute.Taints) != 0 {
		t.Fatalf("an amd64 pool carries no arch taint: %+v", compute.Taints)
	}
}

func TestDeriveNodePoolsOwnNodeClass(t *testing.T) {
	t.Parallel()

	pools, err := cluster.DeriveNodePools(map[string]cluster.NodePoolSpec{
		"ci":       {EphemeralStorage: &cluster.EphemeralStorage{Size: "200Gi"}, PodSubnets: true},
		"platform": {NodeClass: "standard"},
	}, testDefaults, "arn:aws:iam::123456789012:role/eks/example-eks-auto-node")
	if err != nil {
		t.Fatal(err)
	}

	want := cluster.NodeClass{
		Name: "ci", Role: "example-eks-auto-node", PodSubnets: true,
		EphemeralStorage: cluster.EphemeralStorage{Size: "200Gi", IOPS: cluster.DefaultEphemeralIOPS, Throughput: cluster.DefaultEphemeralThroughput},
	}
	if pools[0].NodeClass == nil || *pools[0].NodeClass != want {
		t.Fatalf("own class %+v, want %+v", pools[0].NodeClass, want)
	}

	if pools[1].NodeClass != nil || pools[1].NodeClassName != "standard" {
		t.Fatalf("shared class: %+v", pools[1])
	}
}

func TestOwnNodeClassNeedsTheNodeRole(t *testing.T) {
	t.Parallel()

	for _, arn := range []string{"", "arn:aws:iam::123456789012:user/someone", "arn:aws:iam::123456789012:role/"} {
		_, err := cluster.DeriveNodePools(map[string]cluster.NodePoolSpec{
			"ci": {EphemeralStorage: &cluster.EphemeralStorage{Size: "200Gi"}},
		}, testDefaults, arn)
		if err == nil || !strings.Contains(err.Error(), "autoNodeRoleArn") {
			t.Fatalf("arn %q: %v", arn, err)
		}
	}
}

func TestDeriveNodeClassesAndDefault(t *testing.T) {
	t.Parallel()

	classes, err := cluster.DeriveNodeClasses(map[string]cluster.NodeClassSpec{
		"standard": {EphemeralStorage: cluster.EphemeralStorage{Size: "80Gi"}},
		"big":      {EphemeralStorage: cluster.EphemeralStorage{Size: "200Gi", IOPS: 6000}},
	}, nodeRole)
	if err != nil {
		t.Fatal(err)
	}

	if len(classes) != 2 || classes[0].Name != "big" || classes[0].EphemeralStorage.IOPS != 6000 || classes[1].EphemeralStorage.Throughput != 125 {
		t.Fatalf("classes: %+v", classes)
	}

	pools, err := cluster.DeriveNodePools(map[string]cluster.NodePoolSpec{
		"plain":    {},
		"explicit": {NodeClass: "other"},
		"own":      {EphemeralStorage: &cluster.EphemeralStorage{Size: "200Gi"}},
	}, testDefaults, nodeRole)
	if err != nil {
		t.Fatal(err)
	}

	cluster.ApplyDefaultNodeClass(pools, "standard")

	got := map[string]string{}
	for _, p := range pools {
		got[p.Name] = p.NodeClassName
	}

	if want := map[string]string{"explicit": "other", "own": "", "plain": "standard"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestValidateNodePool(t *testing.T) {
	t.Parallel()

	std := map[string]cluster.NodeClassSpec{"standard": {EphemeralStorage: cluster.EphemeralStorage{Size: "80Gi"}}}
	es := func(e cluster.EphemeralStorage) *cluster.EphemeralStorage { return &e }
	vol := func(size string, iops, tp int) *cluster.EphemeralStorage {
		return es(cluster.EphemeralStorage{Size: size, IOPS: iops, Throughput: tp})
	}

	for _, tc := range []struct {
		name string
		pool string
		np   cluster.NodePoolSpec
		want string // "" = valid
	}{
		{name: "empty", pool: "p"},
		{name: "size only", pool: "ci", np: cluster.NodePoolSpec{EphemeralStorage: es(cluster.EphemeralStorage{Size: "200Gi"})}},
		{name: "bad arch", pool: "p", np: cluster.NodePoolSpec{Archs: []string{"riscv"}}, want: "arm64 or amd64"},
		{name: "bad capacity", pool: "p", np: cluster.NodePoolSpec{CapacityTypes: []string{"reserved"}}, want: "spot or on-demand"},
		{name: "bad category", pool: "p", np: cluster.NodePoolSpec{Categories: []string{"x"}}, want: "c, m, r or t"},
		{name: "negative cpu", pool: "p", np: cluster.NodePoolSpec{CPULimit: -1}, want: "cpu_limit"},
		{name: "one cpu node", pool: "p", np: cluster.NodePoolSpec{MaxInstanceCPU: 1}, want: "max_instance_cpu"},
		{name: "node over pool", pool: "p", np: cluster.NodePoolSpec{CPULimit: 4, MaxInstanceCPU: 8}, want: "exceeds cpu_limit"},
		{name: "missing size", pool: "p", np: cluster.NodePoolSpec{EphemeralStorage: es(cluster.EphemeralStorage{})}, want: "whole Gi"},
		{name: "too big", pool: "p", np: cluster.NodePoolSpec{EphemeralStorage: es(cluster.EphemeralStorage{Size: "59001Gi"})}, want: "59000Gi"},
		{name: "iops low", pool: "p", np: cluster.NodePoolSpec{EphemeralStorage: vol("1Gi", 1000, 0)}, want: "iops"},
		{name: "throughput high", pool: "p", np: cluster.NodePoolSpec{EphemeralStorage: vol("1Gi", 0, 2000)}, want: "throughput"},
		{name: "gp3 ratio", pool: "p", np: cluster.NodePoolSpec{EphemeralStorage: vol("1Gi", 0, 1000)}, want: "0.25 MiB/s per IOPS"},
		{name: "named default", pool: "default", np: cluster.NodePoolSpec{EphemeralStorage: es(cluster.EphemeralStorage{Size: "1Gi"})}, want: "built-in"},
		{name: "shared", pool: "p", np: cluster.NodePoolSpec{NodeClass: "standard"}},
		{name: "unknown class", pool: "p", np: cluster.NodePoolSpec{NodeClass: "nope"}, want: "not in the preset's node_classes"},
		{name: "class and volume", pool: "p", np: cluster.NodePoolSpec{NodeClass: "standard", EphemeralStorage: vol("1Gi", 0, 0)}, want: "excludes"},
		{name: "pod subnets alone", pool: "p", np: cluster.NodePoolSpec{PodSubnets: true}, want: "pod_subnets"},
		{name: "blank zone", pool: "p", np: cluster.NodePoolSpec{Zones: []string{""}}, want: "empty entry"},
		{name: "duplicate zone", pool: "p", np: cluster.NodePoolSpec{Zones: []string{"a", "a"}}, want: "twice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := cluster.ValidateNodePool("preset x", tc.pool, &tc.np, std)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestValidateNodeClass(t *testing.T) {
	t.Parallel()

	if err := cluster.ValidateNodeClass("preset x", "default", &cluster.NodeClassSpec{EphemeralStorage: cluster.EphemeralStorage{Size: "80Gi"}}); err == nil {
		t.Fatal("a shared class named default must be refused")
	}

	if err := cluster.ValidateNodeClass("preset x", "std", &cluster.NodeClassSpec{EphemeralStorage: cluster.EphemeralStorage{Size: "80G"}}); err == nil {
		t.Fatal("a size outside whole Gi must be refused")
	}
}
