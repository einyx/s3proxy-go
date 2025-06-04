package metrics

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds all the metrics for the s3proxy
type Metrics struct {
	// Request metrics
	RequestsTotal    *prometheus.CounterVec
	RequestDuration  *prometheus.HistogramVec
	RequestsInFlight prometheus.Gauge
	ResponseSize     *prometheus.HistogramVec

	// Storage metrics
	StorageOpsTotal    *prometheus.CounterVec
	StorageOpsDuration *prometheus.HistogramVec
	StorageErrors      *prometheus.CounterVec

	// Cache metrics
	CacheHits   *prometheus.CounterVec
	CacheMisses *prometheus.CounterVec
	CacheSize   prometheus.Gauge

	// Rate limiting metrics
	RateLimitTotal   *prometheus.CounterVec
	ConcurrencyLimit *prometheus.CounterVec

	// System metrics
	GoroutineCount prometheus.Gauge
	MemoryUsage    prometheus.Gauge
	GCDuration     prometheus.Histogram

	// Real-time stats (for fast access without Prometheus overhead)
	requestCount     uint64
	errorCount       uint64
	bytesTransferred uint64
	lastResetTime    time.Time
	mu               sync.RWMutex
}

// NewMetrics creates a new metrics instance
func NewMetrics(namespace string) *Metrics {
	if namespace == "" {
		namespace = "s3proxy"
	}

	return &Metrics{
		RequestsTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "requests_total",
				Help:      "Total number of requests processed",
			},
			[]string{"method", "bucket", "status_code", "operation"},
		),

		RequestDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "request_duration_seconds",
				Help:      "Request duration in seconds",
				Buckets:   prometheus.ExponentialBuckets(0.001, 2, 15), // 1ms to ~32s
			},
			[]string{"method", "bucket", "operation"},
		),

		RequestsInFlight: promauto.NewGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "requests_in_flight",
				Help:      "Number of requests currently being processed",
			},
		),

		ResponseSize: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "response_size_bytes",
				Help:      "Response size in bytes",
				Buckets:   prometheus.ExponentialBuckets(1024, 4, 10), // 1KB to ~1GB
			},
			[]string{"method", "bucket", "operation"},
		),

		StorageOpsTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "storage_operations_total",
				Help:      "Total number of storage backend operations",
			},
			[]string{"backend", "operation", "status"},
		),

		StorageOpsDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "storage_operation_duration_seconds",
				Help:      "Storage operation duration in seconds",
				Buckets:   prometheus.ExponentialBuckets(0.001, 2, 15),
			},
			[]string{"backend", "operation"},
		),

		StorageErrors: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "storage_errors_total",
				Help:      "Total number of storage errors",
			},
			[]string{"backend", "operation", "error_type"},
		),

		CacheHits: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "cache_hits_total",
				Help:      "Total number of cache hits",
			},
			[]string{"cache_type"},
		),

		CacheMisses: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "cache_misses_total",
				Help:      "Total number of cache misses",
			},
			[]string{"cache_type"},
		),

		CacheSize: promauto.NewGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "cache_size_bytes",
				Help:      "Current cache size in bytes",
			},
		),

		RateLimitTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "rate_limit_total",
				Help:      "Total number of rate limit events",
			},
			[]string{"limit_type", "action"},
		),

		ConcurrencyLimit: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "concurrency_limit_total",
				Help:      "Total number of concurrency limit events",
			},
			[]string{"action"},
		),

		GoroutineCount: promauto.NewGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "goroutines_count",
				Help:      "Number of goroutines",
			},
		),

		MemoryUsage: promauto.NewGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "memory_usage_bytes",
				Help:      "Memory usage in bytes",
			},
		),

		GCDuration: promauto.NewHistogram(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "gc_duration_seconds",
				Help:      "Garbage collection duration in seconds",
				Buckets:   prometheus.ExponentialBuckets(0.0001, 2, 15),
			},
		),

		lastResetTime: time.Now(),
	}
}

// IncRequest increments request counter
func (m *Metrics) IncRequest(method, bucket, statusCode, operation string) {
	m.RequestsTotal.WithLabelValues(method, bucket, statusCode, operation).Inc()
	atomic.AddUint64(&m.requestCount, 1)
}

// IncError increments error counter
func (m *Metrics) IncError() {
	atomic.AddUint64(&m.errorCount, 1)
}

// AddBytesTransferred adds to bytes transferred counter
func (m *Metrics) AddBytesTransferred(bytes uint64) {
	atomic.AddUint64(&m.bytesTransferred, bytes)
}

// ObserveRequestDuration observes request duration
func (m *Metrics) ObserveRequestDuration(method, bucket, operation string, duration time.Duration) {
	m.RequestDuration.WithLabelValues(method, bucket, operation).Observe(duration.Seconds())
}

// ObserveResponseSize observes response size
func (m *Metrics) ObserveResponseSize(method, bucket, operation string, size int64) {
	m.ResponseSize.WithLabelValues(method, bucket, operation).Observe(float64(size))
}

// IncStorageOp increments storage operation counter
func (m *Metrics) IncStorageOp(backend, operation, status string) {
	m.StorageOpsTotal.WithLabelValues(backend, operation, status).Inc()
}

// ObserveStorageOpDuration observes storage operation duration
func (m *Metrics) ObserveStorageOpDuration(backend, operation string, duration time.Duration) {
	m.StorageOpsDuration.WithLabelValues(backend, operation).Observe(duration.Seconds())
}

// IncStorageError increments storage error counter
func (m *Metrics) IncStorageError(backend, operation, errorType string) {
	m.StorageErrors.WithLabelValues(backend, operation, errorType).Inc()
}

// IncCacheHit increments cache hit counter
func (m *Metrics) IncCacheHit(cacheType string) {
	m.CacheHits.WithLabelValues(cacheType).Inc()
}

// IncCacheMiss increments cache miss counter
func (m *Metrics) IncCacheMiss(cacheType string) {
	m.CacheMisses.WithLabelValues(cacheType).Inc()
}

// SetCacheSize sets current cache size
func (m *Metrics) SetCacheSize(bytes float64) {
	m.CacheSize.Set(bytes)
}

// IncRateLimit increments rate limit counter
func (m *Metrics) IncRateLimit(limitType, action string) {
	m.RateLimitTotal.WithLabelValues(limitType, action).Inc()
}

// IncConcurrencyLimit increments concurrency limit counter
func (m *Metrics) IncConcurrencyLimit(action string) {
	m.ConcurrencyLimit.WithLabelValues(action).Inc()
}

// GetStats returns current statistics
func (m *Metrics) GetStats() Stats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	elapsed := time.Since(m.lastResetTime)
	requests := atomic.LoadUint64(&m.requestCount)
	errors := atomic.LoadUint64(&m.errorCount)
	bytes := atomic.LoadUint64(&m.bytesTransferred)

	return Stats{
		TotalRequests:    requests,
		TotalErrors:      errors,
		BytesTransferred: bytes,
		RequestsPerSec:   float64(requests) / elapsed.Seconds(),
		ErrorRate:        float64(errors) / float64(requests),
		Throughput:       float64(bytes) / elapsed.Seconds(),
		Uptime:           elapsed,
	}
}

// ResetStats resets the statistics counters
func (m *Metrics) ResetStats() {
	m.mu.Lock()
	defer m.mu.Unlock()

	atomic.StoreUint64(&m.requestCount, 0)
	atomic.StoreUint64(&m.errorCount, 0)
	atomic.StoreUint64(&m.bytesTransferred, 0)
	m.lastResetTime = time.Now()
}

// Stats holds performance statistics
type Stats struct {
	TotalRequests    uint64        `json:"total_requests"`
	TotalErrors      uint64        `json:"total_errors"`
	BytesTransferred uint64        `json:"bytes_transferred"`
	RequestsPerSec   float64       `json:"requests_per_sec"`
	ErrorRate        float64       `json:"error_rate"`
	Throughput       float64       `json:"throughput_bytes_per_sec"`
	Uptime           time.Duration `json:"uptime"`
}

// MetricsMiddleware returns a middleware that collects HTTP metrics
func (m *Metrics) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Increment in-flight requests
			m.RequestsInFlight.Inc()
			defer m.RequestsInFlight.Dec()

			// Wrap response writer to capture status and size
			wrapped := &responseWriter{
				ResponseWriter: w,
				statusCode:     http.StatusOK,
			}

			// Extract operation and bucket from path
			operation, bucket := extractOperationAndBucket(r)

			// Call next handler
			next.ServeHTTP(wrapped, r)

			// Record metrics
			duration := time.Since(start)
			statusCode := strconv.Itoa(wrapped.statusCode)

			m.IncRequest(r.Method, bucket, statusCode, operation)
			m.ObserveRequestDuration(r.Method, bucket, operation, duration)
			m.ObserveResponseSize(r.Method, bucket, operation, wrapped.bytesWritten)
			m.AddBytesTransferred(uint64(wrapped.bytesWritten))

			if wrapped.statusCode >= 400 {
				m.IncError()
			}
		})
	}
}

// responseWriter wraps http.ResponseWriter to capture metrics
type responseWriter struct {
	http.ResponseWriter
	statusCode   int
	bytesWritten int64
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	rw.bytesWritten += int64(n)
	return n, err
}

// extractOperationAndBucket extracts S3 operation and bucket from request
func extractOperationAndBucket(r *http.Request) (operation, bucket string) {
	path := r.URL.Path
	if path == "/" || path == "" {
		return "ListBuckets", ""
	}

	// Remove leading slash
	if path[0] == '/' {
		path = path[1:]
	}

	parts := strings.SplitN(path, "/", 2)
	bucket = parts[0]

	// Determine operation based on method and path
	switch r.Method {
	case "GET":
		if len(parts) == 1 {
			return "ListObjects", bucket
		}
		return "GetObject", bucket
	case "PUT":
		if len(parts) == 1 {
			return "CreateBucket", bucket
		}
		return "PutObject", bucket
	case "DELETE":
		if len(parts) == 1 {
			return "DeleteBucket", bucket
		}
		return "DeleteObject", bucket
	case "HEAD":
		if len(parts) == 1 {
			return "HeadBucket", bucket
		}
		return "HeadObject", bucket
	case "POST":
		return "PostObject", bucket
	default:
		return "Unknown", bucket
	}
}

// Handler returns HTTP handler for metrics endpoint
func (m *Metrics) Handler() http.Handler {
	return promhttp.Handler()
}

// StatsHandler returns HTTP handler for JSON stats endpoint
func (m *Metrics) StatsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stats := m.GetStats()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		// Simple JSON encoding without external dependencies
		fmt.Fprintf(w, `{
  "total_requests": %d,
  "total_errors": %d,
  "bytes_transferred": %d,
  "requests_per_sec": %.2f,
  "error_rate": %.4f,
  "throughput_bytes_per_sec": %.2f,
  "uptime_seconds": %.0f
}`, stats.TotalRequests, stats.TotalErrors, stats.BytesTransferred,
			stats.RequestsPerSec, stats.ErrorRate, stats.Throughput, stats.Uptime.Seconds())
	})
}
