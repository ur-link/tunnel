package server

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// metrics holds the Prometheus collectors. A private registry keeps the tunnel
// metrics isolated from any global default registry.
type metrics struct {
	reg           *prometheus.Registry
	activeClients prometheus.Gauge
	activeStreams prometheus.Gauge
	requests      *prometheus.CounterVec
	bytesIn       prometheus.Counter
	bytesOut      prometheus.Counter
}

func newMetrics() *metrics {
	reg := prometheus.NewRegistry()
	m := &metrics{
		reg: reg,
		activeClients: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "tunnel_active_clients",
			Help: "Number of connected tunnel clients (live sessions).",
		}),
		activeStreams: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "tunnel_active_streams",
			Help: "Number of in-flight proxied streams across all sessions.",
		}),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tunnel_requests_total",
			Help: "Total proxied requests by response status class.",
		}, []string{"class"}),
		bytesIn: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "tunnel_bytes_in_total",
			Help: "Total bytes read from local apps (responses).",
		}),
		bytesOut: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "tunnel_bytes_out_total",
			Help: "Total bytes written to local apps (requests).",
		}),
	}
	reg.MustRegister(m.activeClients, m.activeStreams, m.requests, m.bytesIn, m.bytesOut)
	return m
}

// handler serves the Prometheus exposition format.
func (m *metrics) handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// observeStatus increments the request counter bucketed by status class.
func (m *metrics) observeStatus(code int) {
	var class string
	switch {
	case code >= 500:
		class = "5xx"
	case code >= 400:
		class = "4xx"
	case code >= 300:
		class = "3xx"
	default:
		class = "2xx"
	}
	m.requests.WithLabelValues(class).Inc()
}
