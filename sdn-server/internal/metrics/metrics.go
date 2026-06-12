// Package metrics exposes Prometheus instrumentation for an SDN node:
// peer/topology gauges, pub/sub message counters, API request counters, and
// storage totals, served at /metrics on the admin HTTP server.
package metrics

import (
	"net/http"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	registry     *prometheus.Registry
	registryOnce sync.Once

	connectedPeers = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: "sdn",
		Name:      "connected_peers",
		Help:      "Number of currently connected libp2p peers.",
	}, func() float64 {
		mu.RLock()
		defer mu.RUnlock()
		if peerCountFunc == nil {
			return 0
		}
		return float64(peerCountFunc())
	})

	pubsubPublished = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "sdn",
		Name:      "pubsub_messages_published_total",
		Help:      "Pub/sub messages published by this node, by schema or topic.",
	}, []string{"schema"})

	pubsubReceived = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "sdn",
		Name:      "pubsub_messages_received_total",
		Help:      "Pub/sub messages received by this node, by schema or topic.",
	}, []string{"schema"})

	apiRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "sdn",
		Name:      "api_requests_total",
		Help:      "HTTP API requests served, by route group and status class.",
	}, []string{"route", "status"})

	storageRecords = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: "sdn",
		Name:      "storage_records_total",
		Help:      "Total records held in local FlatSQL storage.",
	}, func() float64 {
		mu.RLock()
		defer mu.RUnlock()
		if storageCountFunc == nil {
			return 0
		}
		return float64(storageCountFunc())
	})

	ingestRecords = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "sdn",
		Name:      "ingest_records_total",
		Help:      "Records ingested from external sources, by source name.",
	}, []string{"source"})

	mu               sync.RWMutex
	peerCountFunc    func() int
	storageCountFunc func() int64
)

// Registry returns the process-wide SDN metrics registry, initializing it on
// first use with Go runtime/process collectors and the SDN instruments.
func Registry() *prometheus.Registry {
	registryOnce.Do(func() {
		registry = prometheus.NewRegistry()
		registry.MustRegister(
			collectors.NewGoCollector(),
			collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
			connectedPeers,
			pubsubPublished,
			pubsubReceived,
			apiRequests,
			storageRecords,
			ingestRecords,
		)
	})
	return registry
}

// Handler serves the registry in Prometheus exposition format.
func Handler() http.Handler {
	return promhttp.HandlerFor(Registry(), promhttp.HandlerOpts{})
}

// SetPeerCountFunc wires the connected-peer gauge to the live libp2p host.
// Call once at node startup, e.g. with func() int { return len(host.Network().Peers()) }.
func SetPeerCountFunc(f func() int) {
	mu.Lock()
	defer mu.Unlock()
	peerCountFunc = f
}

// SetStorageRecordCountFunc wires the storage gauge to the local store.
func SetStorageRecordCountFunc(f func() int64) {
	mu.Lock()
	defer mu.Unlock()
	storageCountFunc = f
}

// PubsubPublished records one published message for a schema or topic.
func PubsubPublished(schema string) { pubsubPublished.WithLabelValues(schema).Inc() }

// PubsubReceived records one received message for a schema or topic.
func PubsubReceived(schema string) { pubsubReceived.WithLabelValues(schema).Inc() }

// APIRequest records one served API request for a route group ("admin",
// "data", "core", ...) and status class ("2xx", "4xx", "5xx").
func APIRequest(route, status string) { apiRequests.WithLabelValues(route, status).Inc() }

// IngestRecords adds ingested records for a named source.
func IngestRecords(source string, count int) {
	if count > 0 {
		ingestRecords.WithLabelValues(source).Add(float64(count))
	}
}
