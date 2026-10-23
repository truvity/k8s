package vpc_test

import (
	"reflect"
	"testing"

	"github.com/truvity/k8s/pkg/aws/vpc"
)

func TestDiscoveryTags(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		cluster     string
		kind        vpc.SubnetType
		internalELB bool
		want        map[string]string
	}{
		{"no cluster", "", vpc.Private, true, nil},
		{"public", "c1", vpc.Public, false, map[string]string{"kubernetes.io/cluster/c1": "shared", "kubernetes.io/role/elb": "1"}},
		{"private", "c1", vpc.Private, false, map[string]string{"kubernetes.io/cluster/c1": "shared"}},
		{"private internal elb", "c1", vpc.Private, true, map[string]string{
			"kubernetes.io/cluster/c1": "shared", "kubernetes.io/role/internal-elb": "1",
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := vpc.DiscoveryTags(tc.cluster, tc.kind, tc.internalELB); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
