package main

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics are the controller's series. The alert rules that read them live
// in truvity/observability (charts/platform-alerts, group netpolRepair).
type Metrics struct {
	actions    *prometheus.CounterVec
	candidates *prometheus.GaugeVec
	halted     prometheus.Gauge
	lastRun    prometheus.Gauge
}

// NewMetrics registers the series on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		actions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "netpol_repair_actions_total",
			Help: "Pods repaired (mode=act: deleted) or that would have been (mode=report), by owner and reason. " +
				"TEMPORARY workaround for aws/aws-network-policy-agent#676.",
		}, []string{"namespace", "owner_kind", "owner", "reason", "mode"}),
		candidates: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "netpol_repair_candidates",
			Help: "Pods that meet the selection rules at the last examination, before the cooldown, rate limit and self-stop, by owner and reason.",
		}, []string{"namespace", "owner_kind", "owner", "reason"}),
		halted: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "netpol_repair_halted",
			Help: "1 when the self-stop has latched: acting stopped (reporting goes on) until the controller restarts.",
		}),
		lastRun: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "netpol_repair_last_run_timestamp_seconds",
			Help: "Unix time of the last successful examination.",
		}),
	}
	reg.MustRegister(m.actions, m.candidates, m.halted, m.lastRun)
	return m
}

func (m *Metrics) action(c *candidate, mode Mode) {
	m.actions.WithLabelValues(c.owner.Namespace, c.owner.Kind, c.owner.Name, c.reason, string(mode)).Inc()
}

func (m *Metrics) setCandidates(cs []candidate) {
	m.candidates.Reset()
	for i := range cs {
		c := &cs[i]
		m.candidates.WithLabelValues(c.owner.Namespace, c.owner.Kind, c.owner.Name, c.reason).Inc()
	}
}
