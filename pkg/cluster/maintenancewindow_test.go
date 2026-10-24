package cluster_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/truvity/k8s/pkg/cluster"
)

func drainBlock(schedule, duration string) cluster.Budget {
	return cluster.Budget{
		Nodes:    "0",
		Reasons:  []string{"Drifted", "Underutilized"},
		Schedule: schedule,
		Duration: duration,
	}
}

func TestMaintenanceWindowBudgets(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		w    cluster.MaintenanceWindow
		want []cluster.Budget
	}{
		{
			// Tue, Wed, Thu 01:00-03:00 UTC: closed from 03:00 to the next
			// 01:00, and from Thursday 03:00 to Tuesday 01:00 (118h).
			name: "three nights",
			w:    cluster.MaintenanceWindow{Days: []string{"thu", "tue", "wed"}, Start: "01:00", Duration: "2h"},
			want: []cluster.Budget{
				drainBlock("0 3 * * 2", "22h"),
				drainBlock("0 3 * * 3", "22h"),
				drainBlock("0 3 * * 4", "118h"),
			},
		},
		{
			// One window a week, over the end of the day and of the week:
			// open Sunday 23:00 to Monday 01:00, closed the other 166 hours.
			name: "one window across midnight",
			w:    cluster.MaintenanceWindow{Days: []string{"sun"}, Start: "23:00", Duration: "2h"},
			want: []cluster.Budget{drainBlock("0 1 * * 1", "166h")},
		},
		{
			name: "minutes",
			w:    cluster.MaintenanceWindow{Days: []string{"sat"}, Start: "04:15", Duration: "1h30m"},
			want: []cluster.Budget{drainBlock("45 5 * * 6", "166h30m")},
		},
		{
			// Every day: one closed stretch per day.
			name: "daily",
			w:    cluster.MaintenanceWindow{Days: []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}, Start: "02:00", Duration: "90m"},
			want: []cluster.Budget{
				drainBlock("30 3 * * 1", "22h30m"),
				drainBlock("30 3 * * 2", "22h30m"),
				drainBlock("30 3 * * 3", "22h30m"),
				drainBlock("30 3 * * 4", "22h30m"),
				drainBlock("30 3 * * 5", "22h30m"),
				drainBlock("30 3 * * 6", "22h30m"),
				drainBlock("30 3 * * 0", "22h30m"),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			pools, err := cluster.DeriveNodePools(map[string]cluster.NodePoolSpec{
				"windowed": {MaintenanceWindow: &tc.w},
			}, testDefaults, "")
			if err != nil {
				t.Fatal(err)
			}

			if got := pools[0].Budgets; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("budgets\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestNoWindowNoBudgets(t *testing.T) {
	t.Parallel()

	pools, err := cluster.DeriveNodePools(map[string]cluster.NodePoolSpec{"plain": {}}, testDefaults, "")
	if err != nil {
		t.Fatal(err)
	}

	if len(pools[0].Budgets) != 0 {
		t.Fatalf("a pool without a window carries no extra budget: %+v", pools[0].Budgets)
	}
}

func TestValidateMaintenanceWindow(t *testing.T) {
	t.Parallel()

	ok := cluster.MaintenanceWindow{Days: []string{"tue"}, Start: "01:00", Duration: "2h"}

	if err := cluster.ValidateNodePool("preset x", "p", &cluster.NodePoolSpec{MaintenanceWindow: &ok}, nil); err != nil {
		t.Fatalf("a good window: %v", err)
	}

	bad := map[string]cluster.MaintenanceWindow{
		"at least one weekday": {Start: "01:00", Duration: "2h"},
		"must be one of":       {Days: []string{"tuesday"}, Start: "01:00", Duration: "2h"},
		"twice":                {Days: []string{"tue", "tue"}, Start: "01:00", Duration: "2h"},
		"must be HH:MM":        {Days: []string{"tue"}, Start: "1:00", Duration: "2h"},
		"HH:MM, 24-hour":       {Days: []string{"tue"}, Start: "24:00", Duration: "2h"},
		"whole hours":          {Days: []string{"tue"}, Start: "01:00", Duration: "2 hours"},
		"under 24h":            {Days: []string{"tue"}, Start: "01:00", Duration: "24h"},
		"more than 0":          {Days: []string{"tue"}, Start: "01:00", Duration: "0m"},
		"whole hours and":      {Days: []string{"tue"}, Start: "01:00"},
	}

	for want, w := range bad {
		err := cluster.ValidateNodePool("preset x", "p", &cluster.NodePoolSpec{MaintenanceWindow: &w}, nil)
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "node_pools.p.maintenance_window") {
			t.Errorf("window %+v: got %v, want an error naming node_pools.p.maintenance_window with %q", w, err, want)
		}
	}

	if _, err := cluster.DeriveNodePools(map[string]cluster.NodePoolSpec{
		"p": {MaintenanceWindow: &cluster.MaintenanceWindow{Days: []string{"x"}, Start: "01:00", Duration: "2h"}},
	}, testDefaults, ""); err == nil {
		t.Fatal("DeriveNodePools accepted a bad window")
	}
}
