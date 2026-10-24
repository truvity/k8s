package cluster

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	minutesPerHour = 60
	minutesPerDay  = 24 * minutesPerHour
	minutesPerWeek = 7 * minutesPerDay
)

// weekdays are the names a window's Days take, Monday first. The index is the
// day's place in the week; cron counts from Sunday (0), see cronWeekday.
var weekdays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

var (
	windowStartRE    = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])$`)
	windowDurationRE = regexp.MustCompile(`^(?:([0-9]+)h)?(?:([0-9]+)m)?$`)
)

// The reasons a window withholds. Empty is not among them: reclaiming a node
// that holds no pod moves no workload.
const (
	reasonDrifted       = "Drifted"
	reasonUnderutilized = "Underutilized"
)

type (
	// MaintenanceWindow is when Karpenter may VOLUNTARILY replace a pool's
	// nodes (drift, consolidation). Outside it a budget of zero nodes holds
	// both back. All times are UTC: Karpenter's schedules are.
	//
	// It does not hold back what is forceful: a node that reaches expireAfter,
	// a spot interruption, a node that fails its health repair. A node is
	// replaced at the same weekday and clock time it was born, so a window
	// catches expiry only for nodes created inside it.
	MaintenanceWindow struct {
		// Days are the weekdays the window opens on: mon, tue, wed, thu, fri,
		// sat, sun. At least one, each once.
		Days []string
		// Start is the time of day the window opens, "HH:MM" UTC.
		Start string
		// Duration is how long it stays open, in whole hours and minutes
		// ("2h", "90m", "1h30m"): more than zero, under 24 hours.
		Duration string
	}

	// Budget is one entry of a pool's disruption budgets, as Karpenter's
	// NodePool spec.disruption.budgets takes it.
	Budget struct {
		// Nodes is a count ("0", "1") or a percentage ("10%").
		Nodes string `yaml:"nodes"`
		// Reasons limits the budget to these disruption reasons; empty is all.
		Reasons []string `yaml:"reasons,omitempty"`
		// Schedule is the cron (UTC) at which the budget becomes active;
		// with Duration, how long it stays so. Empty: always active.
		Schedule string `yaml:"schedule,omitempty"`
		Duration string `yaml:"duration,omitempty"`
	}
)

// windowBudgets turns a window into the budgets that enforce it. Karpenter
// applies the smallest of the budgets active at a moment, so a budget cannot
// open a window; it can only close the time around it. The result is one
// zero-node budget per stretch between a window's end and the next window's
// start, which is the complement of the window, week-periodic.
func windowBudgets(w *MaintenanceWindow) ([]Budget, error) {
	days, startMin, durMin, err := parseWindow(w)
	if err != nil {
		return nil, err
	}

	starts := make([]int, len(days))
	for i, d := range days {
		starts[i] = d*minutesPerDay + startMin
	}

	sort.Ints(starts)

	budgets := make([]Budget, 0, len(starts))

	for i, s := range starts {
		closes := s + durMin

		next := starts[(i+1)%len(starts)]
		if i == len(starts)-1 {
			next += minutesPerWeek
		}

		closeAt := closes % minutesPerWeek

		budgets = append(budgets, Budget{
			Nodes:   "0",
			Reasons: []string{reasonDrifted, reasonUnderutilized},
			Schedule: fmt.Sprintf("%d %d * * %d",
				closeAt%minutesPerHour,
				(closeAt/minutesPerHour)%24,
				cronWeekday(closeAt/minutesPerDay%7)),
			Duration: formatDuration(next - closes),
		})
	}

	return budgets, nil
}

// parseWindow checks a window and returns its weekday indexes (Monday 0), its
// start in minutes into the day and its length in minutes.
func parseWindow(w *MaintenanceWindow) (days []int, startMin, durMin int, err error) {
	if len(w.Days) == 0 {
		return nil, 0, 0, fmt.Errorf("days must name at least one weekday (%s)", strings.Join(weekdays, ", "))
	}

	seen := make(map[string]bool, len(w.Days))

	for _, name := range w.Days {
		found := -1

		for i, wd := range weekdays {
			if wd == name {
				found = i
			}
		}

		if found < 0 {
			return nil, 0, 0, fmt.Errorf("days contains %q (must be one of %s)", name, strings.Join(weekdays, ", "))
		}

		if seen[name] {
			return nil, 0, 0, fmt.Errorf("days lists %q twice", name)
		}

		seen[name] = true

		days = append(days, found)
	}

	m := windowStartRE.FindStringSubmatch(w.Start)
	if m == nil {
		return nil, 0, 0, fmt.Errorf("start %q must be HH:MM, 24-hour UTC", w.Start)
	}

	hour, _ := strconv.Atoi(m[1])
	minute, _ := strconv.Atoi(m[2])
	startMin = hour*minutesPerHour + minute

	durMin, err = parseDurationMinutes(w.Duration)
	if err != nil {
		return nil, 0, 0, err
	}

	return days, startMin, durMin, nil
}

// parseDurationMinutes reads "2h", "90m" or "1h30m" as minutes: more than
// zero, under a day, so that a window never meets the next day's.
func parseDurationMinutes(s string) (int, error) {
	m := windowDurationRE.FindStringSubmatch(s)
	if s == "" || m == nil {
		return 0, fmt.Errorf("duration %q must be whole hours and minutes like 2h, 90m or 1h30m", s)
	}

	total := 0

	if m[1] != "" {
		h, _ := strconv.Atoi(m[1])
		total += h * minutesPerHour
	}

	if m[2] != "" {
		n, _ := strconv.Atoi(m[2])
		total += n
	}

	if total <= 0 || total >= minutesPerDay {
		return 0, fmt.Errorf("duration %q must be more than 0 and under 24h", s)
	}

	return total, nil
}

// formatDuration writes minutes in the form Karpenter's budget duration takes:
// hours and minutes, never seconds.
func formatDuration(minutes int) string {
	h, m := minutes/minutesPerHour, minutes%minutesPerHour

	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}

	if h == 0 {
		return fmt.Sprintf("%dm", m)
	}

	return fmt.Sprintf("%dh%dm", h, m)
}

// cronWeekday maps a day index (Monday 0) to cron's (Sunday 0, Monday 1).
func cronWeekday(idx int) int {
	return (idx + 1) % 7
}

// validateMaintenanceWindow is parseWindow with the pool's scope on the error.
func validateMaintenanceWindow(scope, poolName string, w *MaintenanceWindow) error {
	if w == nil {
		return nil
	}

	if _, _, _, err := parseWindow(w); err != nil {
		return fmt.Errorf("%s: node_pools.%s.maintenance_window: %w", scope, poolName, err)
	}

	return nil
}
