package s3

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/einyx/s3proxy-go/internal/auth"
	"github.com/einyx/s3proxy-go/internal/config"
	"github.com/einyx/s3proxy-go/internal/kms"
	"github.com/einyx/s3proxy-go/internal/storage"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
)

// HandlerWithKMS extends the base Handler with KMS encryption support
type HandlerWithKMS struct {
	*Handler
	kmsManager *kms.Manager
	kmsConfig  *config.KMSKeyConfig
}

// NewHandlerWithKMS creates a new S3 handler with KMS support
func NewHandlerWithKMS(storage storage.Backend, auth auth.Provider, s3cfg config.S3Config, kmsCfg *config.KMSKeyConfig) (*HandlerWithKMS, error) {
	baseHandler := NewHandler(storage, auth, s3cfg)

	var kmsManager *kms.Manager
	if kmsCfg != nil && kmsCfg.Enabled {
		kmsConfig, err := kms.ConfigFromAppConfig(kmsCfg)
		if err != nil {
			return nil, err
		}

		kmsManager, err = kms.New(context.Background(), kmsConfig)
		if err != nil {
			return nil, err
		}
	}

	return &HandlerWithKMS{
		Handler:    baseHandler,
		kmsManager: kmsManager,
		kmsConfig:  kmsCfg,
	}, nil
}

// putObjectWithKMS handles object uploads with KMS encryption
func (h *HandlerWithKMS) putObjectWithKMS(w http.ResponseWriter, r *http.Request, bucket, key string, body io.Reader, size int64, metadata map[string]string) error {
	ctx := r.Context()

	// Check if KMS is enabled and get encryption headers
	if h.kmsManager != nil && h.kmsManager.IsEnabled() {
		// Get bucket-specific KMS configuration if available
		var bucketKMSConfig *kms.BucketKMSConfig
		if s3Backend, ok := h.storage.(*storage.S3Backend); ok {
			if bucketCfg := s3Backend.GetBucketConfig(bucket); bucketCfg != nil {
				bucketKMSConfig = kms.BucketConfigFromAppConfig(bucketCfg)
			}
		}

		// Get KMS encryption headers
		kmsHeaders := h.kmsManager.GetEncryptionHeaders(bucket, bucketKMSConfig)

		// Add KMS headers to metadata
		if metadata == nil {
			metadata = make(map[string]string)
		}

		// Store KMS headers in metadata for the storage backend
		for k, v := range kmsHeaders {
			metadata[k] = v
		}

		logrus.WithFields(logrus.Fields{
			"bucket":    bucket,
			"key":       key,
			"kmsKeyID":  kmsHeaders["x-amz-server-side-encryption-aws-kms-key-id"],
			"encrypted": true,
		}).Debug("Applying KMS encryption to object")
	}

	// Call the storage backend's PutObject with KMS metadata
	return h.storage.PutObject(ctx, bucket, key, body, size, metadata)
}

// getObjectWithKMS retrieves object metadata including encryption status
func (h *HandlerWithKMS) getObjectWithKMS(ctx context.Context, bucket, key string) (*storage.Object, error) {
	obj, err := h.storage.GetObject(ctx, bucket, key)
	if err != nil {
		return nil, err
	}

	// Add encryption status to response headers via metadata
	if h.kmsManager != nil && h.kmsManager.IsEnabled() {
		// Check if object was encrypted with KMS
		if obj.Metadata != nil {
			if encType, ok := obj.Metadata["x-amz-server-side-encryption"]; ok && encType == "aws:kms" {
				// Object is KMS encrypted - metadata already contains encryption info
				logrus.WithFields(logrus.Fields{
					"bucket":    bucket,
					"key":       key,
					"encrypted": true,
				}).Debug("Retrieved KMS encrypted object")
			}
		}
	}

	return obj, nil
}

// Override handleObject to use KMS-aware methods
func (h *HandlerWithKMS) handleObject(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	bucket := vars["bucket"]
	key := vars["key"]

	// Remove trailing slash from key if present
	key = strings.TrimSuffix(key, "/")

	if key == "" {
		h.handleBucket(w, r)
		return
	}

	// Check for multipart upload operations
	if uploadId := r.URL.Query().Get("uploadId"); uploadId != "" {
		if partNumber := r.URL.Query().Get("partNumber"); partNumber != "" {
			// Upload part
			if r.Method == "PUT" {
				h.uploadPart(w, r, bucket, key, uploadId, partNumber)
				return
			}
		} else {
			// Complete multipart upload
			if r.Method == "POST" {
				h.completeMultipartUpload(w, r, bucket, key, uploadId)
				return
			}
			// Abort multipart upload
			if r.Method == "DELETE" {
				h.abortMultipartUpload(w, r, bucket, key, uploadId)
				return
			}
		}
	}

	// Check for POST uploads (browser-based uploads)
	if r.Method == "POST" && r.URL.Query().Get("uploads") != "" {
		h.initiateMultipartUpload(w, r, bucket, key)
		return
	}

	// Check for ACL operations
	if r.URL.Query().Get("acl") != "" {
		switch r.Method {
		case "GET":
			h.getObjectACL(w, r, bucket, key)
		case "PUT":
			h.putObjectACL(w, r, bucket, key)
		default:
			h.sendError(w, nil, http.StatusMethodNotAllowed)
		}
		return
	}

	// Standard object operations with KMS support
	switch r.Method {
	case "GET":
		h.getObject(w, r, bucket, key)
	case "PUT":
		h.putObjectKMS(w, r, bucket, key)
	case "HEAD":
		h.headObject(w, r, bucket, key)
	case "DELETE":
		h.deleteObject(w, r, bucket, key)
	default:
		h.sendError(w, nil, http.StatusMethodNotAllowed)
	}
}

// putObjectKMS is the KMS-aware version of putObject
func (h *HandlerWithKMS) putObjectKMS(w http.ResponseWriter, r *http.Request, bucket, key string) {
	// Most of the logic is the same as putObject, but we'll use putObjectWithKMS
	// This is a simplified version - in production, you'd want to properly handle
	// the body reading and ETag calculation as in the original putObject

	// ctx := r.Context() // TODO: Use context when implementing full KMS support
	size := r.ContentLength

	logger := logrus.WithFields(logrus.Fields{
		"bucket": bucket,
		"key":    key,
		"size":   size,
	})

	if size < 0 {
		logger.Error("Missing Content-Length header")
		h.sendError(w, fmt.Errorf("missing Content-Length"), http.StatusBadRequest)
		return
	}

	// Extract metadata from headers
	var metadata map[string]string
	for k, v := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-amz-meta-") {
			if metadata == nil {
				metadata = make(map[string]string)
			}
			metaKey := strings.TrimPrefix(strings.ToLower(k), "x-amz-meta-")
			metadata[metaKey] = v[0]
		}
	}

	// Check for client-specified encryption
	if sse := r.Header.Get("x-amz-server-side-encryption"); sse != "" {
		if sse == "aws:kms" {
			// Client requested KMS encryption
			if metadata == nil {
				metadata = make(map[string]string)
			}
			// Copy encryption headers from request
			if keyId := r.Header.Get("x-amz-server-side-encryption-aws-kms-key-id"); keyId != "" {
				metadata["x-amz-server-side-encryption-aws-kms-key-id"] = keyId
			}
			if context := r.Header.Get("x-amz-server-side-encryption-context"); context != "" {
				metadata["x-amz-server-side-encryption-context"] = context
			}
		}
	}

	// Use KMS-aware upload
	err := h.putObjectWithKMS(w, r, bucket, key, r.Body, size, metadata)
	if err != nil {
		logger.WithError(err).Error("Failed to put object with KMS")
		h.sendError(w, err, http.StatusInternalServerError)
		return
	}

	// Set response headers
	w.Header().Set("ETag", fmt.Sprintf("\"%x\"", time.Now().UnixNano()))

	// Add KMS response headers if object was encrypted
	if h.kmsManager != nil && h.kmsManager.IsEnabled() {
		if metadata != nil {
			if keyId, ok := metadata["x-amz-server-side-encryption-aws-kms-key-id"]; ok {
				w.Header().Set("x-amz-server-side-encryption", "aws:kms")
				w.Header().Set("x-amz-server-side-encryption-aws-kms-key-id", keyId)
			}
		}
	}

	w.WriteHeader(http.StatusOK)
}

// Close cleans up KMS resources
func (h *HandlerWithKMS) Close() {
	if h.kmsManager != nil {
		h.kmsManager.Close()
	}
}
