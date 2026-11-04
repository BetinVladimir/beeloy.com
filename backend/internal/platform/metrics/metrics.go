// Package metrics exposes Prometheus metrics (served on a separate internal port, never through the public proxy).
package metrics

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"beeloy.com/backend/internal/platform/outbox"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
)

var (
	Registry = prometheus.NewRegistry()

	httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "http_requests_total", Help: "HTTP requests by route and status."}, []string{"route", "status"})
	httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "http_request_duration_seconds", Help: "HTTP request duration.", Buckets: prometheus.DefBuckets}, []string{"route"})
	// SSEConnections is the number of open event streams.
	SSEConnections = prometheus.NewGauge(prometheus.GaugeOpts{Name: "sse_connections", Help: "Open SSE connections."})
)

func init() {
	Registry.MustRegister(httpRequests, httpDuration, SSEConnections)
}

type recorder struct {
	http.ResponseWriter
	status int
}

func (r *recorder) WriteHeader(c int) { r.status = c; r.ResponseWriter.WriteHeader(c) }
func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Middleware counts requests per route pattern (not per URL: the label set stays small).
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &recorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		route := "unmatched"
		if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
			route = rc.RoutePattern()
		}
		httpRequests.WithLabelValues(route, strconv.Itoa(rec.status)).Inc()
		httpDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())
	})
}

type collector struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
}

var (
	dPool    = prometheus.NewDesc("db_pool_in_use", "Connections currently in use.", nil, nil)
	dUnpub   = prometheus.NewDesc("outbox_unpublished", "Outbox events waiting for the relay.", nil, nil)
	dOldest  = prometheus.NewDesc("outbox_oldest_unpublished_seconds", "Age of the oldest unpublished outbox event.", nil, nil)
	dPending = prometheus.NewDesc("stream_pending", "Unacknowledged stream entries per consumer group.", []string{"group"}, nil)
	dI18n    = prometheus.NewDesc("i18n_active_version_info", "Active dictionary version (value is always 1).", []string{"version"}, nil)
)

func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- dPool
	ch <- dUnpub
	ch <- dOldest
	ch <- dPending
	ch <- dI18n
}

func (c *collector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ch <- prometheus.MustNewConstMetric(dPool, prometheus.GaugeValue, float64(c.pool.Stat().AcquiredConns()))
	if st, err := outbox.GetStatus(ctx, c.pool, c.rdb); err == nil {
		ch <- prometheus.MustNewConstMetric(dUnpub, prometheus.GaugeValue, float64(st.Unpublished))
		ch <- prometheus.MustNewConstMetric(dOldest, prometheus.GaugeValue, st.OldestAge.Seconds())
	}
	if n, err := outbox.Pending(ctx, c.rdb, outbox.StreamMain, "realtime"); err == nil {
		ch <- prometheus.MustNewConstMetric(dPending, prometheus.GaugeValue, float64(n), "realtime")
	}
	var v string
	if err := c.pool.QueryRow(ctx, `SELECT version FROM i18n_active WHERE id = 1`).Scan(&v); err == nil {
		ch <- prometheus.MustNewConstMetric(dI18n, prometheus.GaugeValue, 1, v)
	}
}

// Handler serves /metrics for a fresh registry that also contains the scrape-time collector.
func Handler(pool *pgxpool.Pool, rdb *redis.Client) http.Handler {
	reg := prometheus.NewRegistry()
	reg.MustRegister(httpRequests, httpDuration, SSEConnections, &collector{pool: pool, rdb: rdb})
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	return mux
}
