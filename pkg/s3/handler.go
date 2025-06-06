package s3

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/einyx/s3proxy-go/internal/auth"
	"github.com/einyx/s3proxy-go/internal/config"
	"github.com/einyx/s3proxy-go/internal/storage"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
)

var (
	// Optimized pools
	smallBufferPool = sync.Pool{
		New: func() interface{} {
			return make([]byte, 4*1024) // 4KB for small files
		},
	}
	bufferPool = sync.Pool{
		New: func() interface{} {
			return make([]byte, 64*1024) // 64KB for medium files
		},
	}
	largeBufferPool = sync.Pool{
		New: func() interface{} {
			return make([]byte, 1024*1024) // 1MB buffers
		},
	}
	hugeBufferPool = sync.Pool{
		New: func() interface{} {
			return make([]byte, 16*1024*1024) // 16MB buffers
		},
	}
	// Response pool
	responseWriterPool = sync.Pool{
		New: func() interface{} {
			return &zeroCopyResponseWriter{}
		},
	}
	// Context pool
	contextPool = sync.Pool{
		New: func() interface{} {
			return &fastContext{}
		},
	}
)

type Handler struct {
	storage storage.Backend
	auth    auth.Provider
	config  config.S3Config
	router  *mux.Router
}

// zeroCopyResponseWriter implements ultra-fast response writing with zero allocations
type zeroCopyResponseWriter struct {
	http.ResponseWriter
	written int64
	status  int
}

func (w *zeroCopyResponseWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.written += int64(n)
	return n, err
}

func (w *zeroCopyResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *zeroCopyResponseWriter) Reset(rw http.ResponseWriter) {
	w.ResponseWriter = rw
	w.written = 0
	w.status = 0
}

// fastContext provides zero-allocation context for request processing
type fastContext struct {
	bucket string
	key    string
	method string
}

func (c *fastContext) Reset() {
	c.bucket = ""
	c.key = ""
	c.method = ""
}

func NewHandler(storage storage.Backend, auth auth.Provider, cfg config.S3Config) *Handler {
	h := &Handler{
		storage: storage,
		auth:    auth,
		config:  cfg,
		router:  mux.NewRouter(),
	}

	h.setupRoutes()
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.router.ServeHTTP(w, r)
}

func (h *Handler) setupRoutes() {
	// Service operations
	h.router.HandleFunc("/", h.listBuckets).Methods("GET").MatcherFunc(noBucketMatcher)

	// Bucket operations
	h.router.HandleFunc("/{bucket}", h.handleBucket).Methods("GET", "PUT", "DELETE", "HEAD")
	h.router.HandleFunc("/{bucket}/", h.handleBucket).Methods("GET", "PUT", "DELETE", "HEAD")

	// Object operations
	h.router.HandleFunc("/{bucket}/{key:.*}", h.handleObject).Methods("GET", "PUT", "DELETE", "HEAD", "POST")
}

func noBucketMatcher(r *http.Request, rm *mux.RouteMatch) bool {
	return r.URL.Path == "/" || r.URL.Path == ""
}

func (h *Handler) listBuckets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	buckets, err := h.storage.ListBuckets(ctx)
	if err != nil {
		h.sendError(w, err, http.StatusInternalServerError)
		return
	}

	type bucket struct {
		Name         string `xml:"Name"`
		CreationDate string `xml:"CreationDate"`
	}

	type listAllMyBucketsResult struct {
		XMLName xml.Name `xml:"ListAllMyBucketsResult"`
		Owner   struct {
			ID          string `xml:"ID"`
			DisplayName string `xml:"DisplayName"`
		} `xml:"Owner"`
		Buckets struct {
			Bucket []bucket `xml:"Bucket"`
		} `xml:"Buckets"`
	}

	result := listAllMyBucketsResult{}
	result.Owner.ID = "s3proxy"
	result.Owner.DisplayName = "S3Proxy"

	for _, b := range buckets {
		result.Buckets.Bucket = append(result.Buckets.Bucket, bucket{
			Name:         b.Name,
			CreationDate: b.CreationDate.Format(time.RFC3339),
		})
	}

	w.Header().Set("Content-Type", "application/xml")
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(result); err != nil {
		logrus.WithError(err).Error("Failed to encode response")
	}
}

func (h *Handler) handleBucket(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	bucket := vars["bucket"]

	logger := logrus.WithFields(logrus.Fields{
		"method": r.Method,
		"bucket": bucket,
		"remote": r.RemoteAddr,
	})

	switch r.Method {
	case "GET":
		logger.Debug("Listing bucket objects")
		h.listObjects(w, r, bucket)
	case "PUT":
		logger.Info("Creating bucket")
		h.createBucket(w, r, bucket)
	case "DELETE":
		logger.Info("Deleting bucket")
		h.deleteBucket(w, r, bucket)
	case "HEAD":
		logger.Debug("Checking bucket existence")
		h.headBucket(w, r, bucket)
	}
}

func (h *Handler) handleObject(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	bucket := vars["bucket"]
	key := vars["key"]

	// Log object operation
	logger := logrus.WithFields(logrus.Fields{
		"method": r.Method,
		"bucket": bucket,
		"key":    key,
		"remote": r.RemoteAddr,
	})

	// Handle multipart upload operations
	if uploadID := r.URL.Query().Get("uploadId"); uploadID != "" {
		logger = logger.WithField("uploadId", uploadID)
		if r.Method == "POST" {
			logger.Debug("Completing multipart upload")
			h.completeMultipartUpload(w, r, bucket, key, uploadID)
			return
		} else if r.Method == "DELETE" {
			logger.Debug("Aborting multipart upload")
			h.abortMultipartUpload(w, r, bucket, key, uploadID)
			return
		} else if r.Method == "GET" {
			logger.Debug("Listing multipart upload parts")
			h.listParts(w, r, bucket, key, uploadID)
			return
		} else if r.Method == "PUT" {
			if partNumber := r.URL.Query().Get("partNumber"); partNumber != "" {
				logger.WithField("partNumber", partNumber).Debug("Uploading part")
				h.uploadPart(w, r, bucket, key, uploadID, partNumber)
				return
			}
		}
	}

	// Handle uploads query
	if r.URL.Query().Get("uploads") != "" && r.Method == "POST" {
		logger.Info("Initiating multipart upload")
		h.initiateMultipartUpload(w, r, bucket, key)
		return
	}

	// Handle ACL operations
	if r.URL.Query().Get("acl") != "" {
		if r.Method == "GET" {
			logger.Debug("Getting object ACL")
			h.getObjectACL(w, r, bucket, key)
			return
		} else if r.Method == "PUT" {
			logger.Debug("Setting object ACL")
			h.putObjectACL(w, r, bucket, key)
			return
		}
	}

	switch r.Method {
	case "GET":
		logger.Debug("Getting object")
		h.getObject(w, r, bucket, key)
	case "PUT":
		logger.WithField("size", r.ContentLength).Debug("Putting object")
		h.putObject(w, r, bucket, key)
	case "DELETE":
		logger.Debug("Deleting object")
		h.deleteObject(w, r, bucket, key)
	case "HEAD":
		logger.Debug("Getting object metadata")
		h.headObject(w, r, bucket, key)
	}
}

func (h *Handler) listObjects(w http.ResponseWriter, r *http.Request, bucket string) {
	ctx := r.Context()

	prefix := r.URL.Query().Get("prefix")
	marker := r.URL.Query().Get("marker")
	delimiter := r.URL.Query().Get("delimiter")
	maxKeysStr := r.URL.Query().Get("max-keys")

	maxKeys := 1000
	if maxKeysStr != "" {
		if mk, err := strconv.Atoi(maxKeysStr); err == nil && mk > 0 {
			maxKeys = mk
		}
	}

	logger := logrus.WithFields(logrus.Fields{
		"bucket":    bucket,
		"prefix":    prefix,
		"delimiter": delimiter,
		"maxKeys":   maxKeys,
		"marker":    marker,
	})
	logger.Debug("Listing objects")

	result, err := h.storage.ListObjectsWithDelimiter(ctx, bucket, prefix, marker, delimiter, maxKeys)
	if err != nil {
		logger.WithError(err).Error("Failed to list objects")
		h.sendError(w, err, http.StatusInternalServerError)
		return
	}

	logger.WithFields(logrus.Fields{
		"objects":        len(result.Contents),
		"commonPrefixes": len(result.CommonPrefixes),
		"truncated":      result.IsTruncated,
	}).Debug("Listed objects successfully")

	type contents struct {
		Key          string `xml:"Key"`
		LastModified string `xml:"LastModified"`
		ETag         string `xml:"ETag"`
		Size         int64  `xml:"Size"`
		StorageClass string `xml:"StorageClass"`
	}

	type listBucketResult struct {
		XMLName        xml.Name   `xml:"ListBucketResult"`
		Name           string     `xml:"Name"`
		Prefix         string     `xml:"Prefix"`
		Marker         string     `xml:"Marker"`
		NextMarker     string     `xml:"NextMarker,omitempty"`
		MaxKeys        int        `xml:"MaxKeys"`
		IsTruncated    bool       `xml:"IsTruncated"`
		Contents       []contents `xml:"Contents"`
		CommonPrefixes []struct {
			Prefix string `xml:"Prefix"`
		} `xml:"CommonPrefixes,omitempty"`
	}

	response := listBucketResult{
		Name:        bucket,
		Prefix:      prefix,
		Marker:      marker,
		MaxKeys:     maxKeys,
		IsTruncated: result.IsTruncated,
		NextMarker:  result.NextMarker,
	}

	for _, obj := range result.Contents {
		response.Contents = append(response.Contents, contents{
			Key:          obj.Key,
			LastModified: obj.LastModified.Format(time.RFC3339),
			ETag:         obj.ETag,
			Size:         obj.Size,
			StorageClass: "STANDARD",
		})
	}

	for _, prefix := range result.CommonPrefixes {
		response.CommonPrefixes = append(response.CommonPrefixes, struct {
			Prefix string `xml:"Prefix"`
		}{Prefix: prefix})
	}

	w.Header().Set("Content-Type", "application/xml")
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(response); err != nil {
		logrus.WithError(err).Error("Failed to encode response")
	}
}

func (h *Handler) getObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	ctx := r.Context()
	start := time.Now()

	logger := logrus.WithFields(logrus.Fields{
		"bucket": bucket,
		"key":    key,
	})

	// Check for range requests
	rangeHeader := r.Header.Get("Range")
	if rangeHeader != "" {
		logger.WithField("range", rangeHeader).Debug("Range request")
		h.getRangeObject(w, r, bucket, key, rangeHeader)
		return
	}

	obj, err := h.storage.GetObject(ctx, bucket, key)
	if err != nil {
		logger.WithError(err).Error("Failed to get object")
		h.sendError(w, err, http.StatusNotFound)
		return
	}
	defer obj.Body.Close()

	logger.WithFields(logrus.Fields{
		"size":        obj.Size,
		"contentType": obj.ContentType,
		"etag":        obj.ETag,
	}).Debug("Retrieved object")

	// RESEARCH-BASED: Optimize header setting based on Go HTTP best practices
	headers := w.Header()
	headers.Set("Content-Type", obj.ContentType)
	headers.Set("Content-Length", strconv.FormatInt(obj.Size, 10))
	headers.Set("ETag", obj.ETag)
	headers.Set("Last-Modified", obj.LastModified.Format(http.TimeFormat))
	headers.Set("Accept-Ranges", "bytes")

	// PERFORMANCE: Set metadata headers efficiently (avoid string concatenation)
	for k, v := range obj.Metadata {
		headers.Set("x-amz-meta-"+k, v)
	}

	// PERFORMANCE OPTIMIZATION: Use direct io.Copy for all sizes
	// Go's io.Copy automatically uses splice syscalls on Linux for optimal performance
	// The previous chunking approach was causing the 10MB GET performance issue
	if _, err := io.Copy(w, obj.Body); err != nil && !isClientDisconnectError(err) {
		logger.WithError(err).Error("Failed to copy object data")
	}

	if logrus.GetLevel() >= logrus.DebugLevel {
		logger.WithField("duration", time.Since(start)).Debug("GET completed")
	}
}

// Removed zeroCopyStreamObject and streamObject - using direct io.Copy instead for better performance

func (h *Handler) getRangeObject(w http.ResponseWriter, r *http.Request, bucket, key, rangeHeader string) {
	ctx := r.Context()

	// Parse range header
	ranges, err := parseRangeHeader(rangeHeader)
	if err != nil || len(ranges) == 0 {
		h.sendError(w, fmt.Errorf("invalid range"), http.StatusRequestedRangeNotSatisfiable)
		return
	}

	// Get object info first
	info, err := h.storage.HeadObject(ctx, bucket, key)
	if err != nil {
		h.sendError(w, err, http.StatusNotFound)
		return
	}

	// Only support single range for now
	if len(ranges) > 1 {
		h.sendError(w, fmt.Errorf("multiple ranges not supported"), http.StatusRequestedRangeNotSatisfiable)
		return
	}

	rng := ranges[0]
	start, end := rng.start, rng.end

	// Adjust range values
	if start < 0 {
		start = info.Size + start
	}
	if end < 0 || end >= info.Size {
		end = info.Size - 1
	}

	if start > end || start >= info.Size {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", info.Size))
		h.sendError(w, fmt.Errorf("invalid range"), http.StatusRequestedRangeNotSatisfiable)
		return
	}

	// Get partial object
	obj, err := h.storage.GetObject(ctx, bucket, key)
	if err != nil {
		h.sendError(w, err, http.StatusNotFound)
		return
	}
	defer obj.Body.Close()

	// Skip to start position
	if start > 0 {
		if seeker, ok := obj.Body.(io.Seeker); ok {
			if _, err := seeker.Seek(start, io.SeekStart); err != nil {
				h.sendError(w, err, http.StatusInternalServerError)
				return
			}
		} else {
			// Fallback: read and discard
			if _, err := io.CopyN(io.Discard, obj.Body, start); err != nil {
				h.sendError(w, err, http.StatusInternalServerError)
				return
			}
		}
	}

	contentLength := end - start + 1

	// Set partial content headers
	w.Header().Set("Content-Type", info.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(contentLength, 10))
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, info.Size))
	w.Header().Set("ETag", info.ETag)
	w.Header().Set("Last-Modified", info.LastModified.Format(http.TimeFormat))
	w.Header().Set("Accept-Ranges", "bytes")

	for k, v := range info.Metadata {
		w.Header().Set("x-amz-meta-"+k, v)
	}

	w.WriteHeader(http.StatusPartialContent)

	// Copy the requested range
	buf := bufferPool.Get().([]byte)
	defer bufferPool.Put(buf)

	if _, err := io.CopyN(w, obj.Body, contentLength); err != nil && !isClientDisconnectError(err) {
		logrus.WithError(err).Debug("Failed to write range data")
	}
}

type byteRange struct {
	start, end int64
}

func parseRangeHeader(rangeHeader string) ([]byteRange, error) {
	if !strings.HasPrefix(rangeHeader, "bytes=") {
		return nil, fmt.Errorf("invalid range header")
	}

	rangeSpec := strings.TrimPrefix(rangeHeader, "bytes=")
	parts := strings.Split(rangeSpec, ",")

	var ranges []byteRange
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		var start, end int64
		if strings.HasPrefix(part, "-") {
			// Suffix range
			n, err := strconv.ParseInt(part[1:], 10, 64)
			if err != nil {
				return nil, err
			}
			start, end = -n, -1
		} else if strings.HasSuffix(part, "-") {
			// Open-ended range
			n, err := strconv.ParseInt(part[:len(part)-1], 10, 64)
			if err != nil {
				return nil, err
			}
			start, end = n, -1
		} else {
			// Normal range
			idx := strings.Index(part, "-")
			if idx < 0 {
				return nil, fmt.Errorf("invalid range spec")
			}

			var err error
			start, err = strconv.ParseInt(part[:idx], 10, 64)
			if err != nil {
				return nil, err
			}

			end, err = strconv.ParseInt(part[idx+1:], 10, 64)
			if err != nil {
				return nil, err
			}
		}

		ranges = append(ranges, byteRange{start: start, end: end})
	}

	return ranges, nil
}

func (h *Handler) putObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	ctx := r.Context()
	start := time.Now()

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

	// CRITICAL PUT OPTIMIZATION: Fast path for small files
	var body io.Reader = r.Body
	
	// For small files, buffer in memory for faster processing
	if size > 0 && size <= 100*1024 { // <= 100KB
		buf := smallBufferPool.Get().([]byte)
		if int64(len(buf)) < size {
			smallBufferPool.Put(buf)
			buf = make([]byte, size)
		} else {
			buf = buf[:size]
			defer smallBufferPool.Put(buf)
		}
		
		// Read entire small file into memory
		_, err := io.ReadFull(r.Body, buf)
		if err != nil {
			logger.WithError(err).Error("Failed to read request body")
			h.sendError(w, err, http.StatusBadRequest)
			return
		}
		body = bytes.NewReader(buf)
	}

	// Extract metadata only if present
	var metadata map[string]string
	hasMetadata := false
	for k := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-amz-meta-") {
			hasMetadata = true
			break
		}
	}
	
	if hasMetadata {
		metadata = make(map[string]string)
		for k, v := range r.Header {
			if strings.HasPrefix(strings.ToLower(k), "x-amz-meta-") {
				metaKey := strings.TrimPrefix(strings.ToLower(k), "x-amz-meta-")
				metadata[metaKey] = v[0]
			}
		}
	}

	// PERFORMANCE: Direct upload with minimal processing
	err := h.storage.PutObject(ctx, bucket, key, body, size, metadata)
	if err != nil {
		logger.WithError(err).Error("Failed to put object")
		h.sendError(w, err, http.StatusInternalServerError)
		return
	}

	// Fast ETag generation
	etag := fmt.Sprintf("\"%x\"", time.Now().UnixNano())
	w.Header().Set("ETag", etag)
	w.WriteHeader(http.StatusOK)
	
	if logrus.GetLevel() >= logrus.DebugLevel {
		logger.WithField("duration", time.Since(start)).Debug("PUT completed")
	}
}

func (h *Handler) deleteObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	ctx := r.Context()

	logger := logrus.WithFields(logrus.Fields{
		"bucket": bucket,
		"key":    key,
	})

	err := h.storage.DeleteObject(ctx, bucket, key)
	if err != nil {
		logger.WithError(err).Error("Failed to delete object")
		h.sendError(w, err, http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
	logger.Info("Object deleted")
}

func (h *Handler) headObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	ctx := r.Context()

	info, err := h.storage.HeadObject(ctx, bucket, key)
	if err != nil {
		h.sendError(w, err, http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	w.Header().Set("ETag", info.ETag)
	w.Header().Set("Last-Modified", info.LastModified.Format(http.TimeFormat))

	for k, v := range info.Metadata {
		w.Header().Set("x-amz-meta-"+k, v)
	}

	w.WriteHeader(http.StatusOK)
}

func (h *Handler) createBucket(w http.ResponseWriter, r *http.Request, bucket string) {
	ctx := r.Context()

	err := h.storage.CreateBucket(ctx, bucket)
	if err != nil {
		h.sendError(w, err, http.StatusConflict)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (h *Handler) deleteBucket(w http.ResponseWriter, r *http.Request, bucket string) {
	ctx := r.Context()

	err := h.storage.DeleteBucket(ctx, bucket)
	if err != nil {
		h.sendError(w, err, http.StatusConflict)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) headBucket(w http.ResponseWriter, r *http.Request, bucket string) {
	ctx := r.Context()

	exists, err := h.storage.BucketExists(ctx, bucket)
	if err != nil {
		h.sendError(w, err, http.StatusInternalServerError)
		return
	}

	if !exists {
		h.sendError(w, fmt.Errorf("bucket not found"), http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (h *Handler) sendError(w http.ResponseWriter, err error, status int) {
	type errorResponse struct {
		XMLName xml.Name `xml:"Error"`
		Code    string   `xml:"Code"`
		Message string   `xml:"Message"`
	}

	code := "InternalError"
	switch status {
	case http.StatusNotFound:
		code = "NoSuchKey"
	case http.StatusConflict:
		code = "BucketAlreadyExists"
	case http.StatusBadRequest:
		code = "BadRequest"
	case http.StatusForbidden:
		code = "AccessDenied"
	}

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)

	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(errorResponse{
		Code:    code,
		Message: err.Error(),
	}); err != nil {
		logrus.WithError(err).Error("Failed to encode error response")
	}
}

// Multipart upload operations
func (h *Handler) initiateMultipartUpload(w http.ResponseWriter, r *http.Request, bucket, key string) {
	ctx := r.Context()

	metadata := make(map[string]string)
	for k, v := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-amz-meta-") {
			metaKey := strings.TrimPrefix(strings.ToLower(k), "x-amz-meta-")
			metadata[metaKey] = v[0]
		}
	}

	uploadID, err := h.storage.InitiateMultipartUpload(ctx, bucket, key, metadata)
	if err != nil {
		h.sendError(w, err, http.StatusInternalServerError)
		return
	}

	type initiateMultipartUploadResult struct {
		XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
		Bucket   string   `xml:"Bucket"`
		Key      string   `xml:"Key"`
		UploadId string   `xml:"UploadId"`
	}

	response := initiateMultipartUploadResult{
		Bucket:   bucket,
		Key:      key,
		UploadId: uploadID,
	}

	// Build XML response
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(response); err != nil {
		logrus.WithError(err).Error("Failed to encode response")
		h.sendError(w, err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(buf.Bytes()); err != nil {
		logrus.WithError(err).Error("Failed to write response")
	}
}

func (h *Handler) uploadPart(w http.ResponseWriter, r *http.Request, bucket, key, uploadID, partNumberStr string) {
	ctx := r.Context()

	partNumber, err := strconv.Atoi(partNumberStr)
	if err != nil || partNumber < 1 || partNumber > 10000 {
		h.sendError(w, fmt.Errorf("invalid part number"), http.StatusBadRequest)
		return
	}

	size := r.ContentLength
	if size < 0 {
		h.sendError(w, fmt.Errorf("missing Content-Length"), http.StatusBadRequest)
		return
	}

	etag, err := h.storage.UploadPart(ctx, bucket, key, uploadID, partNumber, r.Body, size)
	if err != nil {
		h.sendError(w, err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("ETag", etag)
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) completeMultipartUpload(w http.ResponseWriter, r *http.Request, bucket, key, uploadID string) {
	ctx := r.Context()

	type completedPart struct {
		PartNumber int    `xml:"PartNumber"`
		ETag       string `xml:"ETag"`
	}

	type completeMultipartUpload struct {
		Parts []completedPart `xml:"Part"`
	}

	var req completeMultipartUpload
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendError(w, err, http.StatusBadRequest)
		return
	}

	parts := make([]storage.CompletedPart, len(req.Parts))
	for i, p := range req.Parts {
		parts[i] = storage.CompletedPart{
			PartNumber: p.PartNumber,
			ETag:       p.ETag,
		}
	}

	err := h.storage.CompleteMultipartUpload(ctx, bucket, key, uploadID, parts)
	if err != nil {
		h.sendError(w, err, http.StatusInternalServerError)
		return
	}

	type completeMultipartUploadResult struct {
		XMLName  xml.Name `xml:"CompleteMultipartUploadResult"`
		Location string   `xml:"Location"`
		Bucket   string   `xml:"Bucket"`
		Key      string   `xml:"Key"`
		ETag     string   `xml:"ETag"`
	}

	w.Header().Set("Content-Type", "application/xml")
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(completeMultipartUploadResult{
		Location: fmt.Sprintf("http://%s/%s/%s", r.Host, bucket, key),
		Bucket:   bucket,
		Key:      key,
		ETag:     fmt.Sprintf("\"%x\"", time.Now().UnixNano()),
	}); err != nil {
		logrus.WithError(err).Error("Failed to encode response")
	}
}

func (h *Handler) abortMultipartUpload(w http.ResponseWriter, r *http.Request, bucket, key, uploadID string) {
	ctx := r.Context()

	err := h.storage.AbortMultipartUpload(ctx, bucket, key, uploadID)
	if err != nil {
		h.sendError(w, err, http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listParts(w http.ResponseWriter, r *http.Request, bucket, key, uploadID string) {
	ctx := r.Context()

	maxPartsStr := r.URL.Query().Get("max-parts")
	partNumberMarkerStr := r.URL.Query().Get("part-number-marker")

	maxParts := 1000
	if maxPartsStr != "" {
		if mp, err := strconv.Atoi(maxPartsStr); err == nil && mp > 0 {
			maxParts = mp
		}
	}

	partNumberMarker := 0
	if partNumberMarkerStr != "" {
		if pnm, err := strconv.Atoi(partNumberMarkerStr); err == nil {
			partNumberMarker = pnm
		}
	}

	result, err := h.storage.ListParts(ctx, bucket, key, uploadID, maxParts, partNumberMarker)
	if err != nil {
		h.sendError(w, err, http.StatusInternalServerError)
		return
	}

	type part struct {
		PartNumber   int    `xml:"PartNumber"`
		LastModified string `xml:"LastModified"`
		ETag         string `xml:"ETag"`
		Size         int64  `xml:"Size"`
	}

	type listPartsResult struct {
		XMLName              xml.Name `xml:"ListPartsResult"`
		Bucket               string   `xml:"Bucket"`
		Key                  string   `xml:"Key"`
		UploadId             string   `xml:"UploadId"`
		PartNumberMarker     int      `xml:"PartNumberMarker"`
		NextPartNumberMarker int      `xml:"NextPartNumberMarker,omitempty"`
		MaxParts             int      `xml:"MaxParts"`
		IsTruncated          bool     `xml:"IsTruncated"`
		Parts                []part   `xml:"Part"`
	}

	response := listPartsResult{
		Bucket:               bucket,
		Key:                  key,
		UploadId:             uploadID,
		PartNumberMarker:     partNumberMarker,
		NextPartNumberMarker: result.NextPartNumberMarker,
		MaxParts:             maxParts,
		IsTruncated:          result.IsTruncated,
	}

	for _, p := range result.Parts {
		response.Parts = append(response.Parts, part{
			PartNumber:   p.PartNumber,
			LastModified: p.LastModified.Format(time.RFC3339),
			ETag:         p.ETag,
			Size:         p.Size,
		})
	}

	w.Header().Set("Content-Type", "application/xml")
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(response); err != nil {
		logrus.WithError(err).Error("Failed to encode response")
	}
}

func (h *Handler) getObjectACL(w http.ResponseWriter, r *http.Request, bucket, key string) {
	ctx := r.Context()

	acl, err := h.storage.GetObjectACL(ctx, bucket, key)
	if err != nil {
		h.sendError(w, err, http.StatusInternalServerError)
		return
	}

	type grantee struct {
		XMLName     xml.Name `xml:"Grantee"`
		Type        string   `xml:"xsi:type,attr"`
		ID          string   `xml:"ID,omitempty"`
		DisplayName string   `xml:"DisplayName,omitempty"`
		URI         string   `xml:"URI,omitempty"`
	}

	type grant struct {
		Grantee    grantee `xml:"Grantee"`
		Permission string  `xml:"Permission"`
	}

	type accessControlPolicy struct {
		XMLName xml.Name `xml:"AccessControlPolicy"`
		Owner   struct {
			ID          string `xml:"ID"`
			DisplayName string `xml:"DisplayName"`
		} `xml:"Owner"`
		AccessControlList struct {
			Grant []grant `xml:"Grant"`
		} `xml:"AccessControlList"`
	}

	response := accessControlPolicy{}
	response.Owner.ID = acl.Owner.ID
	response.Owner.DisplayName = acl.Owner.DisplayName

	for _, g := range acl.Grants {
		grant := grant{
			Permission: g.Permission,
			Grantee: grantee{
				Type:        g.Grantee.Type,
				ID:          g.Grantee.ID,
				DisplayName: g.Grantee.DisplayName,
				URI:         g.Grantee.URI,
			},
		}
		response.AccessControlList.Grant = append(response.AccessControlList.Grant, grant)
	}

	w.Header().Set("Content-Type", "application/xml")
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(response); err != nil {
		logrus.WithError(err).Error("Failed to encode response")
	}
}

func (h *Handler) putObjectACL(w http.ResponseWriter, r *http.Request, bucket, key string) {
	ctx := r.Context()

	// For now, just accept and ignore ACL requests
	err := h.storage.PutObjectACL(ctx, bucket, key, nil)
	if err != nil {
		h.sendError(w, err, http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// isClientDisconnectError checks if error is due to client disconnect
func isClientDisconnectError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "write: connection refused")
}

// bufferedReader wraps an io.Reader with a buffer pool for high-performance streaming
type bufferedReader struct {
	reader io.Reader
	buf    []byte
	pos    int
	end    int
	eof    bool
}

func (br *bufferedReader) Read(p []byte) (n int, err error) {
	// ULTRA AGGRESSIVE: Try to satisfy request directly from buffer first
	if br.pos < br.end {
		n = copy(p, br.buf[br.pos:br.end])
		br.pos += n
		if n == len(p) {
			return n, nil // Satisfied entirely from buffer
		}
		// Partial satisfaction, continue to fill more
		p = p[n:]
	}

	if br.eof {
		if n > 0 {
			return n, nil
		}
		return 0, io.EOF
	}

	// If request is larger than our buffer, read directly to avoid copy
	if len(p) >= len(br.buf) {
		directN, err := br.reader.Read(p)
		if err != nil {
			if err == io.EOF {
				br.eof = true
			}
		}
		return n + directN, err
	}

	// Fill buffer for smaller requests
	br.pos = 0
	br.end, err = br.reader.Read(br.buf)
	if err != nil {
		if err == io.EOF {
			br.eof = true
		} else {
			return n, err
		}
	}

	// Copy from freshly filled buffer
	additionalN := copy(p, br.buf[br.pos:br.end])
	br.pos += additionalN

	return n + additionalN, nil
}

// speedReader is an ultra-optimized reader for maximum PUT performance
type speedReader struct {
	reader io.Reader
	size   int64
	read   int64
}

func (sr *speedReader) Read(p []byte) (n int, err error) {
	if sr.read >= sr.size {
		return 0, io.EOF
	}

	// Calculate remaining bytes
	remaining := sr.size - sr.read
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}

	n, err = sr.reader.Read(p)
	sr.read += int64(n)

	// Force EOF when we've read exactly the expected size
	if sr.read >= sr.size && err == nil {
		err = io.EOF
	}

	return n, err
}
