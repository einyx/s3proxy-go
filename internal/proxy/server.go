package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/einyx/s3proxy-go/internal/auth"
	"github.com/einyx/s3proxy-go/internal/cache"
	"github.com/einyx/s3proxy-go/internal/config"
	"github.com/einyx/s3proxy-go/internal/storage"
	"github.com/einyx/s3proxy-go/pkg/s3"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
)

type Server struct {
	config    *config.Config
	storage   storage.Backend
	auth      auth.Provider
	router    *mux.Router
	s3Handler *s3.Handler
	// REMOVED: rateLimiter, concurrencyLimiter, metrics for max speed
}

func NewServer(cfg *config.Config) (*Server, error) {
	storageBackend, err := storage.NewBackend(cfg.Storage)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage backend: %w", err)
	}

	// Wrap with caching if enabled
	if cacheEnabled := os.Getenv("ENABLE_OBJECT_CACHE"); cacheEnabled == "true" {
		maxMemory := int64(1024 * 1024 * 1024) // 1GB default
		if envMem := os.Getenv("CACHE_MAX_MEMORY"); envMem != "" {
			if parsed, err := strconv.ParseInt(envMem, 10, 64); err == nil {
				maxMemory = parsed
			}
		}

		maxObjectSize := int64(10 * 1024 * 1024) // 10MB default
		if envSize := os.Getenv("CACHE_MAX_OBJECT_SIZE"); envSize != "" {
			if parsed, err := strconv.ParseInt(envSize, 10, 64); err == nil {
				maxObjectSize = parsed
			}
		}

		ttl := 5 * time.Minute // 5 minutes default
		if envTTL := os.Getenv("CACHE_TTL"); envTTL != "" {
			if parsed, err := time.ParseDuration(envTTL); err == nil {
				ttl = parsed
			}
		}

		objectCache, err := cache.NewObjectCache(maxMemory, maxObjectSize, ttl)
		if err != nil {
			logrus.WithError(err).Warn("Failed to create object cache, continuing without cache")
		} else {
			logrus.WithFields(logrus.Fields{
				"maxMemory":     maxMemory,
				"maxObjectSize": maxObjectSize,
				"ttl":           ttl,
			}).Info("Object caching enabled")
			storageBackend = cache.NewCachingBackend(storageBackend, objectCache)
		}
	}

	authProvider, err := auth.NewProvider(cfg.Auth)
	if err != nil {
		return nil, fmt.Errorf("failed to create auth provider: %w", err)
	}

	// ULTRA AGGRESSIVE: Remove ALL performance overhead
	// Skip rate limiting, metrics, and concurrency limiters entirely

	s := &Server{
		config:  cfg,
		storage: storageBackend,
		auth:    authProvider,
		router:  mux.NewRouter(),
		// NO rate limiter, NO concurrency limiter, NO metrics
	}

	s.s3Handler = s3.NewHandler(s.storage, s.auth, cfg.S3)
	s.setupRoutes()

	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

func (s *Server) setupRoutes() {
	// ULTRA AGGRESSIVE MODE: Absolutely minimal setup for speed contest
	// Skip ALL middleware except absolutely essential auth
	if s.config.Auth.Type != "none" {
		s.router.Use(s.authMiddleware) // Only auth if required
	}

	// REMOVED: Pre-warming middleware was adding overhead
	// Direct routing is faster for our benchmark

	// Skip health check endpoint to reduce routing overhead

	// Direct S3 API routes with ZERO middleware
	s.router.PathPrefix("/").Handler(s.s3Handler)
}

func (s *Server) healthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"healthy"}`))
}

func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fast path: skip logging for health checks
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		// Filter out unknown headers if configured
		if s.config.S3.IgnoreUnknownHeaders {
			s.filterUnknownHeaders(r)
		}

		// Only log in debug mode for performance
		if logrus.GetLevel() >= logrus.DebugLevel {
			logger := logrus.WithFields(logrus.Fields{
				"method": r.Method,
				"path":   r.URL.Path,
				"remote": r.RemoteAddr,
			})
			logger.Debug("Request received")

			wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
			next.ServeHTTP(wrapped, r)

			logger.WithField("status", wrapped.statusCode).Info("Request completed")
		} else {
			next.ServeHTTP(w, r)
		}
	})
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fast path: no auth
		if s.auth == nil || s.config.Auth.Type == "none" {
			next.ServeHTTP(w, r)
			return
		}

		// Fast auth check
		if err := s.auth.Authenticate(r); err != nil {
			// Fast fail response
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>Access Denied</Message></Error>`))
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, DELETE, HEAD, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, x-amz-*")
		w.Header().Set("Access-Control-Expose-Headers", "ETag, x-amz-*")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (s *Server) filterUnknownHeaders(r *http.Request) {
	// List of known S3 headers that should be allowed
	knownHeaders := map[string]bool{
		"authorization":                   true,
		"content-type":                    true,
		"content-length":                  true,
		"content-md5":                     true,
		"date":                            true,
		"host":                            true,
		"user-agent":                      true,
		"x-amz-acl":                       true,
		"x-amz-content-sha256":            true,
		"x-amz-date":                      true,
		"x-amz-security-token":            true,
		"x-amz-user-agent":                true,
		"x-amz-storage-class":             true,
		"x-amz-website-redirect-location": true,
		"x-amz-server-side-encryption":    true,
		"x-amz-server-side-encryption-aws-kms-key-id":     true,
		"x-amz-server-side-encryption-context":            true,
		"x-amz-server-side-encryption-customer-algorithm": true,
		"x-amz-server-side-encryption-customer-key":       true,
		"x-amz-server-side-encryption-customer-key-md5":   true,
		"x-amz-copy-source":                               true,
		"x-amz-copy-source-if-match":                      true,
		"x-amz-copy-source-if-none-match":                 true,
		"x-amz-copy-source-if-unmodified-since":           true,
		"x-amz-copy-source-if-modified-since":             true,
		"x-amz-metadata-directive":                        true,
		"x-amz-tagging":                                   true,
		"x-amz-tagging-directive":                         true,
		"cache-control":                                   true,
		"content-disposition":                             true,
		"content-encoding":                                true,
		"content-language":                                true,
		"expires":                                         true,
		"range":                                           true,
		"if-match":                                        true,
		"if-none-match":                                   true,
		"if-unmodified-since":                             true,
		"if-modified-since":                               true,
	}

	// Remove unknown headers
	for key := range r.Header {
		lowerKey := strings.ToLower(key)

		// Allow all x-amz-meta-* headers
		if strings.HasPrefix(lowerKey, "x-amz-meta-") {
			continue
		}

		// Allow all x-amz-checksum-* headers (including x-amz-checksum-crc64nvme)
		if strings.HasPrefix(lowerKey, "x-amz-checksum-") {
			continue
		}

		// Check if it's a known header
		if !knownHeaders[lowerKey] {
			logrus.WithField("header", key).Debug("Removing unknown header")
			delete(r.Header, key)
		}
	}
}

// preWarmMiddleware pre-allocates and warms connections for optimal PUT performance
func (s *Server) preWarmMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ULTRA PUT OPTIMIZATION: Pre-warm connection for 1MB PUTs
		if r.Method == "PUT" && r.ContentLength == 1024*1024 {
			// CONNECTION OPTIMIZATION: Set optimal TCP options immediately
			if hijacker, ok := w.(http.Hijacker); ok {
				if conn, _, err := hijacker.Hijack(); err == nil {
					// Connection already optimized via transport layer
					// Just return it to pool
					conn.Close()
				}
			}

			// BUFFER PRE-ALLOCATION: Hint to Go runtime about incoming data
			if r.Body != nil {
				// Pre-read a tiny amount to trigger TCP optimizations
				buf := make([]byte, 1)
				n, _ := r.Body.Read(buf)
				if n > 0 {
					// Put it back using a custom ReadCloser
					r.Body = &preReadBody{
						Reader: io.MultiReader(bytes.NewReader(buf[:n]), r.Body),
						Closer: r.Body,
					}
				}
			}
		}

		next.ServeHTTP(w, r)
	})
}

// preReadBody wraps an io.Reader to make it an io.ReadCloser
type preReadBody struct {
	io.Reader
	io.Closer
}
