package main

import (
	"errors"
	"fmt"
	"time"
)

// Mode is what the repairer does with a candidate.
type Mode string

const (
	// ModeReport logs, counts and records an Event for what would be
	// deleted, and deletes nothing. The default.
	ModeReport Mode = "report"
	// ModeAct deletes the candidate, within the safety limits.
	ModeAct Mode = "act"
)

// Config holds every threshold and limit. DefaultConfig is what the chart
// ships; the flags in main.go override it field by field.
type Config struct {
	Mode     Mode
	Interval time.Duration

	// A candidate restarted RestartThreshold times within RestartWindow,
	// or has been Running and NotReady for NotReadyAfter.
	RestartThreshold int
	RestartWindow    time.Duration
	NotReadyAfter    time.Duration
	// The only pod of its owner is deleted only after it has been stuck
	// this long (there is no other replica to carry the traffic, and no
	// other replica to prove the owner itself is healthy).
	SingleReplicaStuckAfter time.Duration

	// Safety limits.
	OwnerCooldown     time.Duration // no second repair of one owner within this
	MinDeleteInterval time.Duration // cluster-wide spacing of deletions
	MaxRepairs        int           // after MaxRepairs within HaltWindow, the next candidate halts acting
	HaltWindow        time.Duration

	Namespaces        []string // empty: all
	ExcludeNamespaces []string

	// Optional stronger signals, both off by default.
	MaxNodeAge             time.Duration // 0: off
	RequireTimeoutEvidence bool
	TimeoutPatterns        []string
	LogTailLines           int64

	// An EKS Auto Mode NodeDiagnostic for the pod's node before a repair.
	NodeDiagnostic            bool
	NodeDiagnosticDestination string

	// Self is this controller's own pod, namespace/name; never a candidate.
	Self string
}

// DefaultConfig is the configuration the brief and the chart ship.
func DefaultConfig() Config {
	return Config{
		Mode:                      ModeReport,
		Interval:                  time.Minute,
		RestartThreshold:          3,
		RestartWindow:             15 * time.Minute,
		NotReadyAfter:             10 * time.Minute,
		SingleReplicaStuckAfter:   10 * time.Minute,
		OwnerCooldown:             30 * time.Minute,
		MinDeleteInterval:         time.Minute,
		MaxRepairs:                5,
		HaltWindow:                time.Hour,
		TimeoutPatterns:           []string{"i/o timeout", "connect: connection timed out"},
		LogTailLines:              100,
		NodeDiagnosticDestination: "node",
	}
}

// Validate refuses a configuration that would disable a safety limit.
func (c *Config) Validate() error {
	var errs []error
	if c.Mode != ModeReport && c.Mode != ModeAct {
		errs = append(errs, fmt.Errorf("mode %q: want report or act", c.Mode))
	}
	positive := map[string]time.Duration{
		"interval": c.Interval, "restart-window": c.RestartWindow, "not-ready-after": c.NotReadyAfter,
		"single-replica-stuck-after": c.SingleReplicaStuckAfter, "owner-cooldown": c.OwnerCooldown,
		"min-delete-interval": c.MinDeleteInterval, "halt-window": c.HaltWindow,
	}
	for name, d := range positive {
		if d <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive, got %s", name, d))
		}
	}
	if c.RestartThreshold < 1 {
		errs = append(errs, fmt.Errorf("restart-threshold must be at least 1, got %d", c.RestartThreshold))
	}
	if c.MaxRepairs < 1 {
		errs = append(errs, fmt.Errorf("max-repairs must be at least 1, got %d", c.MaxRepairs))
	}
	if c.MaxNodeAge < 0 {
		errs = append(errs, fmt.Errorf("max-node-age must not be negative, got %s", c.MaxNodeAge))
	}
	if c.RequireTimeoutEvidence && len(c.TimeoutPatterns) == 0 {
		errs = append(errs, errors.New("require-timeout-evidence needs at least one timeout pattern"))
	}
	if c.LogTailLines < 0 {
		errs = append(errs, fmt.Errorf("log-tail-lines must not be negative, got %d", c.LogTailLines))
	}
	if c.NodeDiagnostic && c.NodeDiagnosticDestination == "" {
		errs = append(errs, errors.New("node-diagnostic needs node-diagnostic-destination"))
	}
	return errors.Join(errs...)
}
