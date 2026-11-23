package telemetry

import (
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var latencyBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1800, 3600}
var sourceReleasePattern = regexp.MustCompile(`^[a-z0-9._-]+@[a-f0-9]{40}$`)

type httpMetrics struct {
	registry    *prometheus.Registry
	requests    *prometheus.CounterVec
	duration    *prometheus.HistogramVec
	environment string
}

func metricEnvironment(value string) string {
	switch value {
	case "production", "staging", "development", "test":
		return value
	default:
		return "unknown"
	}
}
func (d *Diagnostics) metricsSet() *httpMetrics {
	d.metricsOnce.Do(func() {
		labels := []string{"component", "environment", "phase", "method", "status_class", "outcome", "error_category"}
		m := &httpMetrics{registry: prometheus.NewRegistry(), environment: metricEnvironment(d.environment)}
		m.requests = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "re8ch_toolchain_http_requests_total", Help: "Completed toolchain HTTP requests."}, labels)
		m.duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "re8ch_toolchain_http_request_duration_seconds", Help: "Toolchain HTTP request duration.", Buckets: latencyBuckets}, labels)
		release := d.release
		if !sourceReleasePattern.MatchString(release) {
			release = "unknown"
		}
		build := prometheus.NewGauge(prometheus.GaugeOpts{Name: "re8ch_toolchain_build_info", Help: "Immutable source for Sentry/GitHub correlation.", ConstLabels: prometheus.Labels{"component": d.component, "release": release, "repository": "among-clusters/among-clusters", "environment": m.environment}})
		build.Set(1)
		m.registry.MustRegister(m.requests, m.duration, build)
		d.metrics = m
	})
	return d.metrics
}
func requestPhase(component, path string) string {
	if component == "among-hub" {
		if strings.HasPrefix(path, "/v1/") {
			return "hub_api"
		}
		return "http_entry"
	}
	switch {
	case strings.HasPrefix(path, "/k8s/"):
		return "kubernetes_proxy"
	case path == "/v1/public-key":
		return "public_key"
	case path == "/v1/credentials":
		return "credential_store"
	case path == "/v1/credentials/revoke":
		return "credential_revoke"
	default:
		return "http_entry"
	}
}
func metricMethod(value string) string {
	switch value {
	case "GET", "HEAD", "POST", "PUT", "DELETE", "CONNECT", "OPTIONS", "TRACE", "PATCH":
		return value
	default:
		return "OTHER"
	}
}
func metricCategory(value string) string {
	switch value {
	case "none", "http_error", "delivery", "panic", "canceled", "timeout", "transport", "denied", "unavailable", "invalid_or_expired", "invalid_response":
		return value
	default:
		return "internal"
	}
}
func (d *Diagnostics) observeHTTP(r *http.Request, status int, failed bool, errorCategory string, elapsed time.Duration) {
	m := d.metricsSet()
	statusClass := "none"
	if status >= 100 && status <= 599 {
		statusClass = strconv.Itoa(status/100) + "xx"
	}
	outcome := "success"
	if failed {
		outcome = "error"
	}
	labels := []string{d.component, m.environment, requestPhase(d.component, r.URL.Path), metricMethod(r.Method), statusClass, outcome, metricCategory(errorCategory)}
	m.requests.WithLabelValues(labels...).Inc()
	m.duration.WithLabelValues(labels...).Observe(elapsed.Seconds())
}

// Metrics never share the public application mux and never bind a wildcard.
// Pod-network policies scope scraping; hostNetwork deployments also require
// actual private POD_IP/listener and public-unreachability validation.
func (d *Diagnostics) MetricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(d.metricsSet().registry, promhttp.HandlerOpts{EnableOpenMetrics: true}))
	return mux
}
func (d *Diagnostics) ServeMetrics() {
	server := &http.Server{Addr: metricsAddress(os.Getenv("POD_IP")), Handler: d.MetricsHandler()}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Print("internal metrics listener stopped")
	}
}

func metricsAddress(podIP string) string {
	ip := net.ParseIP(podIP)
	if ip == nil || ip.IsUnspecified() {
		podIP = "127.0.0.1"
	}
	return net.JoinHostPort(podIP, "9090")
}
