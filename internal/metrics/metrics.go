// Package metrics defines the server's Prometheus metrics, served at
// /metrics. They live in their own registry, so tests can create servers
// freely without colliding in the global one.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var Registry = prometheus.NewRegistry()

// Buckets for fast in-process work: 50µs to ~1.6s.
var fast = prometheus.ExponentialBuckets(0.00005, 2, 16)

var (
	Commands = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "yorm_commands_total",
		Help: "Commands handled, by name and result (ok, a reject code, or duplicate for a retry).",
	}, []string{"name", "result"})

	CommandLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "yorm_command_latency_seconds",
		Help:    "Time the session actor spends on a command: decide, append, apply, and broadcast.",
		Buckets: fast,
	})

	EventAppend = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "yorm_event_append_seconds",
		Help:    "Time to append a command's events to Postgres.",
		Buckets: fast,
	})

	Broadcast = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "yorm_broadcast_seconds",
		Help:    "Time to project an event for every viewer and queue it to every client.",
		Buckets: fast,
	})

	Snapshots = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "yorm_snapshots_total",
		Help: "Snapshots saved, by result.",
	}, []string{"result"})

	SessionLoad = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "yorm_session_load_seconds",
		Help:    "Time to start a session: load the snapshot and replay the events after it.",
		Buckets: fast,
	})

	AppendBatch = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "yorm_append_batch_size",
		Help:    "Commands' appends committed together by group commit.",
		Buckets: prometheus.ExponentialBuckets(1, 2, 8),
	})

	SendBufferDrops = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "yorm_send_buffer_drops_total",
		Help: "Clients disconnected because their send buffer filled up.",
	})

	Reconnects = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "yorm_reconnects_total",
		Help: "Syncs from a previous seq, i.e. clients coming back after a disconnect.",
	})

	CatchUps = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "yorm_syncs_total",
		Help: "Syncs answered, by how: events (catch-up) or snapshot.",
	}, []string{"answer"})
)

func init() {
	Registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		Commands, CommandLatency, EventAppend, AppendBatch, Broadcast, Snapshots, SessionLoad,
		SendBufferDrops, Reconnects, CatchUps,
	)
}

// Gauge registers a gauge whose value comes from f, such as the number of
// active sessions. Registering the same name twice panics.
func Gauge(name, help string, f func() float64) {
	Registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: name, Help: help}, f))
}

func Handler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{})
}
