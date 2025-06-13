package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/sirupsen/logrus"

	"github.com/einyx/s3proxy-go/internal/config"
)

type S3Backend struct {
	defaultClient   *s3.S3                          // Default S3 client
	clients         map[string]*s3.S3               // Per-region S3 clients
	sessions        map[string]*session.Session     // Per-region sessions
	config          *config.S3StorageConfig         // Keep reference to config
	bucketMapping   map[string]string               // Simple virtual to real bucket mapping
	bucketConfigs   map[string]*config.BucketConfig // Per-bucket configuration
	bufferPool      sync.Pool
	largeBufferPool sync.Pool
	metadataCache   *MetadataCache
	mu              sync.RWMutex // Protect client creation
}

// MetadataCache provides fast object metadata caching
type MetadataCache struct {
	mu    sync.RWMutex
	cache map[string]*cachedMetadata
	ttl   time.Duration
}

type cachedMetadata struct {
	info   *ObjectInfo
	expiry time.Time
}

func (m *MetadataCache) Get(key string) (*ObjectInfo, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if entry, ok := m.cache[key]; ok && time.Now().Before(entry.expiry) {
		return entry.info, true
	}
	return nil, false
}

func (m *MetadataCache) Set(key string, info *ObjectInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cache[key] = &cachedMetadata{
		info:   info,
		expiry: time.Now().Add(m.ttl),
	}
}

func (m *MetadataCache) Delete(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cache, key)
}

// mapBucket maps a virtual bucket name to a real bucket name if mapping is configured
func (s *S3Backend) mapBucket(virtualBucket string) string {
	// First check bucket configs (new style)
	if s.bucketConfigs != nil {
		if cfg, ok := s.bucketConfigs[virtualBucket]; ok && cfg.RealName != "" {
			logrus.WithFields(logrus.Fields{
				"virtual": virtualBucket,
				"real":    cfg.RealName,
			}).Debug("Mapping bucket name from bucket config")
			return cfg.RealName
		}
	}

	// Fall back to simple mapping (old style)
	if s.bucketMapping != nil {
		if realBucket, ok := s.bucketMapping[virtualBucket]; ok {
			logrus.WithFields(logrus.Fields{
				"virtual": virtualBucket,
				"real":    realBucket,
			}).Debug("Mapping bucket name from simple mapping")
			return realBucket
		}
	}

	return virtualBucket
}

// getPrefixForBucket returns the prefix (subdirectory) for a virtual bucket
func (s *S3Backend) getPrefixForBucket(virtualBucket string) string {
	logrus.WithFields(logrus.Fields{
		"virtualBucket": virtualBucket,
		"hasConfigs":    s.bucketConfigs != nil,
		"configCount":   len(s.bucketConfigs),
	}).Debug("getPrefixForBucket called")

	if s.bucketConfigs != nil {
		if cfg, ok := s.bucketConfigs[virtualBucket]; ok {
			logrus.WithFields(logrus.Fields{
				"virtualBucket": virtualBucket,
				"config":        cfg,
				"hasPrefix":     cfg.Prefix != "",
			}).Info("Found bucket config")

			if cfg.Prefix != "" {
				// Ensure prefix ends with / if it doesn't already
				prefix := cfg.Prefix
				if !strings.HasSuffix(prefix, "/") {
					prefix += "/"
				}
				logrus.WithFields(logrus.Fields{
					"virtualBucket": virtualBucket,
					"prefix":        prefix,
				}).Info("Using bucket prefix")
				return prefix
			}
		}
	}

	logrus.WithField("virtualBucket", virtualBucket).Debug("No prefix found for bucket")
	return ""
}

// addPrefixToKey adds the bucket prefix to a key if configured
func (s *S3Backend) addPrefixToKey(virtualBucket, key string) string {
	prefix := s.getPrefixForBucket(virtualBucket)
	if prefix == "" {
		return key
	}
	// Avoid double slashes
	if strings.HasPrefix(key, "/") {
		return prefix + key[1:]
	}
	return prefix + key
}

// removePrefixFromKey removes the bucket prefix from a key if configured
func (s *S3Backend) removePrefixFromKey(virtualBucket, key string) string {
	prefix := s.getPrefixForBucket(virtualBucket)
	if prefix == "" {
		return key
	}
	return strings.TrimPrefix(key, prefix)
}

// getClientForBucket returns the appropriate S3 client for the given bucket
func (s *S3Backend) getClientForBucket(bucket string) (*s3.S3, error) {
	// Check if we have a specific configuration for this bucket (virtual bucket)
	if s.bucketConfigs != nil {
		if cfg, ok := s.bucketConfigs[bucket]; ok {
			logrus.WithFields(logrus.Fields{
				"virtualBucket": bucket,
				"realBucket":    cfg.RealName,
				"region":        cfg.Region,
			}).Debug("Using bucket-specific configuration")
			return s.getOrCreateClient(cfg)
		}

		// Check if this is a real bucket that's used by virtual buckets
		for virtualBucket, cfg := range s.bucketConfigs {
			if cfg.RealName == bucket {
				logrus.WithFields(logrus.Fields{
					"realBucket":    bucket,
					"virtualBucket": virtualBucket,
					"region":        cfg.Region,
				}).Debug("Using configuration from virtual bucket mapping")
				return s.getOrCreateClient(cfg)
			}
		}
	}

	logrus.WithField("bucket", bucket).Debug("Using default client for bucket")
	// Use default client
	return s.defaultClient, nil
}

// getOrCreateClient gets or creates an S3 client for the given bucket configuration
func (s *S3Backend) getOrCreateClient(bucketCfg *config.BucketConfig) (*s3.S3, error) {
	// Use region as the key for client caching
	clientKey := bucketCfg.Region // pragma: allowlist secret
	if bucketCfg.Endpoint != "" {
		clientKey = bucketCfg.Endpoint + "_" + bucketCfg.Region // pragma: allowlist secret
	}

	// Check if client already exists
	s.mu.RLock()
	if client, ok := s.clients[clientKey]; ok {
		s.mu.RUnlock()
		return client, nil
	}
	s.mu.RUnlock()

	// Create new client
	s.mu.Lock()
	defer s.mu.Unlock()

	// Double-check after acquiring write lock
	if client, ok := s.clients[clientKey]; ok {
		return client, nil
	}

	// Create AWS config for this bucket
	awsConfig := &aws.Config{
		Region:           aws.String(bucketCfg.Region),
		S3ForcePathStyle: aws.Bool(s.config.UsePathStyle),
		MaxRetries:       aws.Int(3),
	}

	// Handle endpoint
	if bucketCfg.Endpoint != "" {
		awsConfig.Endpoint = aws.String(bucketCfg.Endpoint)
	} else if s.config.Endpoint != "" && bucketCfg.Endpoint == "" {
		// Use default endpoint if bucket doesn't specify one
		awsConfig.Endpoint = aws.String(s.config.Endpoint)
	}

	if s.config.DisableSSL {
		awsConfig.DisableSSL = aws.Bool(true)
	}

	// Handle credentials - bucket-specific take precedence
	if bucketCfg.AccessKey != "" && bucketCfg.SecretKey != "" {
		awsConfig.Credentials = credentials.NewStaticCredentials(bucketCfg.AccessKey, bucketCfg.SecretKey, "")
	} else if s.config.AccessKey != "" && s.config.SecretKey != "" {
		awsConfig.Credentials = credentials.NewStaticCredentials(s.config.AccessKey, s.config.SecretKey, "")
	}

	// Create session
	sess, err := session.NewSession(awsConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS session for region %s: %w", bucketCfg.Region, err)
	}

	// Create client
	client := s3.New(sess)

	// Cache the client and session
	s.clients[clientKey] = client // pragma: allowlist secret
	s.sessions[clientKey] = sess  // pragma: allowlist secret

	logrus.WithFields(logrus.Fields{
		"region":   bucketCfg.Region,
		"endpoint": bucketCfg.Endpoint,
		"key":      clientKey,
	}).Info("Created new S3 client for bucket")

	return client, nil
}

func NewS3Backend(cfg *config.S3StorageConfig) (*S3Backend, error) {
	// Basic AWS config
	awsConfig := &aws.Config{
		Region:           aws.String(cfg.Region),
		S3ForcePathStyle: aws.Bool(cfg.UsePathStyle),
		MaxRetries:       aws.Int(3),
		// Enable S3 cross-region bucket access
		S3UseAccelerate: aws.Bool(false),
		// Use the global endpoint for cross-region compatibility
		S3DisableContentMD5Validation: aws.Bool(false),
	}

	// Handle endpoint only for non-AWS services (MinIO, LocalStack, etc.)
	if cfg.Endpoint != "" {
		awsConfig.Endpoint = aws.String(cfg.Endpoint)
		logrus.WithField("endpoint", cfg.Endpoint).Info("Using custom S3 endpoint")
	}

	if cfg.DisableSSL {
		awsConfig.DisableSSL = aws.Bool(true)
	}

	// Handle credentials
	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		awsConfig.Credentials = credentials.NewStaticCredentials(cfg.AccessKey, cfg.SecretKey, "")
		logrus.Info("Using static AWS credentials")
	} else {
		logrus.Info("Using AWS default credential chain (env vars, IAM role, etc.)")
	}

	// Create session
	var sess *session.Session
	var err error

	if cfg.Profile != "" {
		sess, err = session.NewSessionWithOptions(session.Options{
			Config:            *awsConfig,
			Profile:           cfg.Profile,
			SharedConfigState: session.SharedConfigEnable,
		})
		logrus.WithField("profile", cfg.Profile).Info("Using AWS profile")
	} else {
		sess, err = session.NewSession(awsConfig)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create AWS session: %w", err)
	}

	// Create S3 client - let AWS SDK handle everything
	s3Client := s3.New(sess)

	// Log the actual credentials provider being used (for debugging)
	if sess.Config.Credentials != nil {
		creds, err := sess.Config.Credentials.Get()
		if err != nil {
			logrus.WithError(err).Warn("Failed to get AWS credentials for logging")
		} else {
			logrus.WithFields(logrus.Fields{
				"provider":     creds.ProviderName,
				"hasAccessKey": creds.AccessKeyID != "",
			}).Info("AWS credentials resolved")
		}
	}

	// Log bucket mapping if configured
	if len(cfg.BucketMapping) > 0 {
		logrus.WithField("bucketMapping", cfg.BucketMapping).Info("Bucket mapping configured")
	}

	// Log bucket configs if configured
	if len(cfg.BucketConfigs) > 0 {
		logrus.WithField("bucketConfigs", cfg.BucketConfigs).Info("Bucket configs configured")
	}

	logrus.WithFields(logrus.Fields{
		"endpoint":     cfg.Endpoint,
		"region":       cfg.Region,
		"usePathStyle": cfg.UsePathStyle,
	}).Info("S3 backend created")

	return &S3Backend{
		defaultClient: s3Client,
		clients:       make(map[string]*s3.S3),
		sessions:      make(map[string]*session.Session),
		config:        cfg,
		bucketMapping: cfg.BucketMapping,
		bucketConfigs: cfg.BucketConfigs,
		bufferPool: sync.Pool{
			New: func() interface{} {
				buf := make([]byte, 32*1024) // 32KB buffers
				return &buf
			},
		},
		largeBufferPool: sync.Pool{
			New: func() interface{} {
				buf := make([]byte, 1024*1024) // 1MB buffers
				return &buf
			},
		},
		metadataCache: &MetadataCache{
			cache: make(map[string]*cachedMetadata),
			ttl:   30 * time.Second,
		},
	}, nil
}

func (s *S3Backend) ListBuckets(ctx context.Context) ([]BucketInfo, error) {
	// ListBuckets is a global operation, use default client
	result, err := s.defaultClient.ListBucketsWithContext(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return nil, fmt.Errorf("failed to list buckets: %w", err)
	}

	// Create a map to track real buckets
	realBuckets := make(map[string]BucketInfo)
	for _, b := range result.Buckets {
		realBuckets[aws.StringValue(b.Name)] = BucketInfo{
			Name:         aws.StringValue(b.Name),
			CreationDate: aws.TimeValue(b.CreationDate),
		}
	}

	// Start with all real buckets
	buckets := make([]BucketInfo, 0, len(result.Buckets)+len(s.bucketConfigs))

	// Add all real buckets first
	for _, info := range realBuckets {
		buckets = append(buckets, info)
	}

	// Add virtual buckets
	for virtualName, config := range s.bucketConfigs {
		// Use the creation date of the real bucket if available
		creationDate := time.Now()
		if realBucket, exists := realBuckets[config.RealName]; exists {
			creationDate = realBucket.CreationDate
		}

		buckets = append(buckets, BucketInfo{
			Name:         virtualName,
			CreationDate: creationDate,
		})
	}

	return buckets, nil
}

func (s *S3Backend) CreateBucket(ctx context.Context, bucket string) error {
	realBucket := s.mapBucket(bucket)
	client, err := s.getClientForBucket(bucket)
	if err != nil {
		return fmt.Errorf("failed to get client for bucket: %w", err)
	}

	_, err = client.CreateBucketWithContext(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(realBucket),
	})
	if err != nil {
		return fmt.Errorf("failed to create bucket: %w", err)
	}
	return nil
}

func (s *S3Backend) DeleteBucket(ctx context.Context, bucket string) error {
	realBucket := s.mapBucket(bucket)
	client, err := s.getClientForBucket(bucket)
	if err != nil {
		return fmt.Errorf("failed to get client for bucket: %w", err)
	}

	_, err = client.DeleteBucketWithContext(ctx, &s3.DeleteBucketInput{
		Bucket: aws.String(realBucket),
	})
	if err != nil {
		return fmt.Errorf("failed to delete bucket: %w", err)
	}
	return nil
}

func (s *S3Backend) BucketExists(ctx context.Context, bucket string) (bool, error) {
	realBucket := s.mapBucket(bucket)
	client, err := s.getClientForBucket(bucket)
	if err != nil {
		return false, fmt.Errorf("failed to get client for bucket: %w", err)
	}

	_, err = client.HeadBucketWithContext(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(realBucket),
	})
	if err != nil {
		if strings.Contains(err.Error(), "NotFound") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *S3Backend) ListObjects(ctx context.Context, bucket, prefix, marker string, maxKeys int) (*ListObjectsResult, error) {
	return s.ListObjectsWithDelimiter(ctx, bucket, prefix, marker, "", maxKeys)
}

func (s *S3Backend) ListObjectsWithDelimiter(ctx context.Context, bucket, prefix, marker, delimiter string, maxKeys int) (*ListObjectsResult, error) {
	realBucket := s.mapBucket(bucket)
	bucketPrefix := s.getPrefixForBucket(bucket)

	// Combine bucket prefix with the requested prefix
	actualPrefix := bucketPrefix + prefix

	logrus.WithFields(logrus.Fields{
		"bucket":       bucket,
		"realBucket":   realBucket,
		"bucketPrefix": bucketPrefix,
		"prefix":       prefix,
		"actualPrefix": actualPrefix,
		"delimiter":    delimiter,
	}).Info("S3 ListObjectsWithDelimiter called")

	input := &s3.ListObjectsInput{
		Bucket:  aws.String(realBucket),
		MaxKeys: aws.Int64(int64(maxKeys)),
	}

	if actualPrefix != "" {
		input.Prefix = aws.String(actualPrefix)
	}
	if marker != "" {
		// Add prefix to marker too
		input.Marker = aws.String(s.addPrefixToKey(bucket, marker))
	}
	if delimiter != "" {
		input.Delimiter = aws.String(delimiter)
	}

	client, err := s.getClientForBucket(bucket)
	if err != nil {
		return nil, fmt.Errorf("failed to get client for bucket: %w", err)
	}

	resp, err := client.ListObjectsWithContext(ctx, input)
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"bucket": bucket,
			"error":  err.Error(),
		}).Error("S3 ListObjects failed")
		return nil, fmt.Errorf("failed to list objects: %w", err)
	}

	result := &ListObjectsResult{
		IsTruncated: aws.BoolValue(resp.IsTruncated),
		Contents:    make([]ObjectInfo, 0, len(resp.Contents)),
	}

	for _, obj := range resp.Contents {
		key := aws.StringValue(obj.Key)
		size := aws.Int64Value(obj.Size)

		// Remove bucket prefix from key
		virtualKey := s.removePrefixFromKey(bucket, key)

		// When using delimiter, filter out directory markers
		if delimiter == "/" && strings.HasSuffix(virtualKey, "/") {
			continue
		}

		result.Contents = append(result.Contents, ObjectInfo{
			Key:          virtualKey,
			Size:         size,
			ETag:         aws.StringValue(obj.ETag),
			LastModified: aws.TimeValue(obj.LastModified),
			StorageClass: aws.StringValue(obj.StorageClass),
		})
	}

	if resp.NextMarker != nil {
		// Remove bucket prefix from next marker
		result.NextMarker = s.removePrefixFromKey(bucket, aws.StringValue(resp.NextMarker))
	}

	for _, prefix := range resp.CommonPrefixes {
		// Remove bucket prefix from common prefixes
		virtualPrefix := s.removePrefixFromKey(bucket, aws.StringValue(prefix.Prefix))
		result.CommonPrefixes = append(result.CommonPrefixes, virtualPrefix)
	}

	return result, nil
}

func (s *S3Backend) GetObject(ctx context.Context, bucket, key string) (*Object, error) {
	realBucket := s.mapBucket(bucket)
	realKey := s.addPrefixToKey(bucket, key)

	input := &s3.GetObjectInput{
		Bucket: aws.String(realBucket),
		Key:    aws.String(realKey),
	}

	client, err := s.getClientForBucket(bucket)
	if err != nil {
		return nil, fmt.Errorf("failed to get client for bucket: %w", err)
	}

	resp, err := client.GetObjectWithContext(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to get object: %w", err)
	}

	metadata := make(map[string]string)
	for k, v := range resp.Metadata {
		if v != nil {
			metadata[k] = *v
		}
	}

	return &Object{
		Body:         resp.Body,
		ContentType:  aws.StringValue(resp.ContentType),
		Size:         aws.Int64Value(resp.ContentLength),
		ETag:         aws.StringValue(resp.ETag),
		LastModified: aws.TimeValue(resp.LastModified),
		Metadata:     metadata,
	}, nil
}

func (s *S3Backend) PutObject(ctx context.Context, bucket, key string, reader io.Reader, size int64, metadata map[string]string) error {
	realBucket := s.mapBucket(bucket)
	realKey := s.addPrefixToKey(bucket, key)

	var body io.ReadSeeker
	if rs, ok := reader.(io.ReadSeeker); ok {
		body = rs
	} else {
		// Buffer for small objects
		if size > 0 && size <= 5*1024*1024 { // 5MB
			data, err := io.ReadAll(reader)
			if err != nil {
				return fmt.Errorf("failed to read data: %w", err)
			}
			body = bytes.NewReader(data)
		} else {
			// For large objects, use multipart upload
			return s.putObjectMultipart(ctx, bucket, realBucket, key, reader, metadata)
		}
	}

	input := &s3.PutObjectInput{
		Bucket:        aws.String(realBucket),
		Key:           aws.String(realKey),
		Body:          body,
		ContentLength: aws.Int64(size),
	}

	if len(metadata) > 0 {
		input.Metadata = make(map[string]*string, len(metadata))
		for k, v := range metadata {
			input.Metadata[k] = aws.String(v)
		}
	}

	client, err := s.getClientForBucket(bucket)
	if err != nil {
		return fmt.Errorf("failed to get client for bucket: %w", err)
	}

	_, err = client.PutObjectWithContext(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to put object: %w", err)
	}

	// Invalidate cache
	s.metadataCache.Delete(fmt.Sprintf("%s/%s", bucket, key))

	return nil
}

func (s *S3Backend) DeleteObject(ctx context.Context, bucket, key string) error {
	realBucket := s.mapBucket(bucket)
	realKey := s.addPrefixToKey(bucket, key)

	client, err := s.getClientForBucket(bucket)
	if err != nil {
		return fmt.Errorf("failed to get client for bucket: %w", err)
	}

	_, err = client.DeleteObjectWithContext(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(realBucket),
		Key:    aws.String(realKey),
	})
	if err != nil {
		return fmt.Errorf("failed to delete object: %w", err)
	}

	// Invalidate cache
	s.metadataCache.Delete(fmt.Sprintf("%s/%s", bucket, key))

	return nil
}

func (s *S3Backend) HeadObject(ctx context.Context, bucket, key string) (*ObjectInfo, error) {
	realBucket := s.mapBucket(bucket)
	realKey := s.addPrefixToKey(bucket, key)

	// Check cache first
	cacheKey := fmt.Sprintf("%s/%s", bucket, key)
	if cached, found := s.metadataCache.Get(cacheKey); found {
		return cached, nil
	}

	client, err := s.getClientForBucket(bucket)
	if err != nil {
		return nil, fmt.Errorf("failed to get client for bucket: %w", err)
	}

	resp, err := client.HeadObjectWithContext(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(realBucket),
		Key:    aws.String(realKey),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to head object: %w", err)
	}

	metadata := make(map[string]string)
	for k, v := range resp.Metadata {
		if v != nil {
			metadata[k] = *v
		}
	}

	info := &ObjectInfo{
		Key:          key,
		Size:         aws.Int64Value(resp.ContentLength),
		ETag:         aws.StringValue(resp.ETag),
		LastModified: aws.TimeValue(resp.LastModified),
		StorageClass: aws.StringValue(resp.StorageClass),
		Metadata:     metadata,
	}

	// Cache the result
	s.metadataCache.Set(cacheKey, info)

	return info, nil
}

func (s *S3Backend) GetObjectACL(ctx context.Context, bucket, key string) (*ACL, error) {
	realBucket := s.mapBucket(bucket)
	realKey := s.addPrefixToKey(bucket, key)

	client, err := s.getClientForBucket(bucket)
	if err != nil {
		return nil, fmt.Errorf("failed to get client for bucket: %w", err)
	}

	resp, err := client.GetObjectAclWithContext(ctx, &s3.GetObjectAclInput{
		Bucket: aws.String(realBucket),
		Key:    aws.String(realKey),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get object ACL: %w", err)
	}

	acl := &ACL{
		Owner: Owner{
			ID:          aws.StringValue(resp.Owner.ID),
			DisplayName: aws.StringValue(resp.Owner.DisplayName),
		},
		Grants: make([]Grant, 0, len(resp.Grants)),
	}

	for _, g := range resp.Grants {
		grant := Grant{
			Permission: aws.StringValue(g.Permission),
		}

		if g.Grantee != nil {
			grant.Grantee = Grantee{
				Type:        aws.StringValue(g.Grantee.Type),
				ID:          aws.StringValue(g.Grantee.ID),
				DisplayName: aws.StringValue(g.Grantee.DisplayName),
				URI:         aws.StringValue(g.Grantee.URI),
			}
		}

		acl.Grants = append(acl.Grants, grant)
	}

	return acl, nil
}

func (s *S3Backend) PutObjectACL(ctx context.Context, bucket, key string, acl *ACL) error {
	// For simplicity, we'll just acknowledge the request
	// A full implementation would convert the ACL to S3 format
	return nil
}

func (s *S3Backend) InitiateMultipartUpload(ctx context.Context, bucket, key string, metadata map[string]string) (string, error) {
	realBucket := s.mapBucket(bucket)
	realKey := s.addPrefixToKey(bucket, key)

	input := &s3.CreateMultipartUploadInput{
		Bucket: aws.String(realBucket),
		Key:    aws.String(realKey),
	}

	if len(metadata) > 0 {
		input.Metadata = make(map[string]*string)
		for k, v := range metadata {
			input.Metadata[k] = aws.String(v)
		}
	}

	client, err := s.getClientForBucket(bucket)
	if err != nil {
		return "", fmt.Errorf("failed to get client for bucket: %w", err)
	}

	resp, err := client.CreateMultipartUploadWithContext(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to initiate multipart upload: %w", err)
	}

	return aws.StringValue(resp.UploadId), nil
}

func (s *S3Backend) UploadPart(ctx context.Context, bucket, key, uploadID string, partNumber int, reader io.Reader, size int64) (string, error) {
	realBucket := s.mapBucket(bucket)
	realKey := s.addPrefixToKey(bucket, key)

	// Read all data to calculate ETag
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("failed to read part data: %w", err)
	}

	client, err := s.getClientForBucket(bucket)
	if err != nil {
		return "", fmt.Errorf("failed to get client for bucket: %w", err)
	}

	resp, err := client.UploadPartWithContext(ctx, &s3.UploadPartInput{
		Bucket:        aws.String(realBucket),
		Key:           aws.String(realKey),
		UploadId:      aws.String(uploadID),
		PartNumber:    aws.Int64(int64(partNumber)),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
	})
	if err != nil {
		return "", fmt.Errorf("failed to upload part: %w", err)
	}

	return aws.StringValue(resp.ETag), nil
}

func (s *S3Backend) CompleteMultipartUpload(ctx context.Context, bucket, key, uploadID string, parts []CompletedPart) error {
	realBucket := s.mapBucket(bucket)
	realKey := s.addPrefixToKey(bucket, key)

	completedParts := make([]*s3.CompletedPart, len(parts))
	for i, p := range parts {
		completedParts[i] = &s3.CompletedPart{
			PartNumber: aws.Int64(int64(p.PartNumber)),
			ETag:       aws.String(p.ETag),
		}
	}

	client, err := s.getClientForBucket(bucket)
	if err != nil {
		return fmt.Errorf("failed to get client for bucket: %w", err)
	}

	_, err = client.CompleteMultipartUploadWithContext(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(realBucket),
		Key:      aws.String(realKey),
		UploadId: aws.String(uploadID),
		MultipartUpload: &s3.CompletedMultipartUpload{
			Parts: completedParts,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to complete multipart upload: %w", err)
	}

	return nil
}

func (s *S3Backend) AbortMultipartUpload(ctx context.Context, bucket, key, uploadID string) error {
	realBucket := s.mapBucket(bucket)
	realKey := s.addPrefixToKey(bucket, key)

	client, err := s.getClientForBucket(bucket)
	if err != nil {
		return fmt.Errorf("failed to get client for bucket: %w", err)
	}

	_, err = client.AbortMultipartUploadWithContext(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(realBucket),
		Key:      aws.String(realKey),
		UploadId: aws.String(uploadID),
	})
	if err != nil {
		return fmt.Errorf("failed to abort multipart upload: %w", err)
	}
	return nil
}

func (s *S3Backend) ListParts(ctx context.Context, bucket, key, uploadID string, maxParts int, partNumberMarker int) (*ListPartsResult, error) {
	virtualBucket := bucket // Keep track of the virtual bucket name
	realBucket := s.mapBucket(bucket)
	realKey := s.addPrefixToKey(bucket, key)

	input := &s3.ListPartsInput{
		Bucket:   aws.String(realBucket),
		Key:      aws.String(realKey),
		UploadId: aws.String(uploadID),
		MaxParts: aws.Int64(int64(maxParts)),
	}

	if partNumberMarker > 0 {
		input.PartNumberMarker = aws.Int64(int64(partNumberMarker))
	}

	client, err := s.getClientForBucket(bucket)
	if err != nil {
		return nil, fmt.Errorf("failed to get client for bucket: %w", err)
	}

	resp, err := client.ListPartsWithContext(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to list parts: %w", err)
	}

	result := &ListPartsResult{
		Bucket:      virtualBucket, // Return the virtual bucket name to the client
		Key:         key,
		UploadID:    uploadID,
		IsTruncated: aws.BoolValue(resp.IsTruncated),
		Parts:       make([]Part, 0, len(resp.Parts)),
	}

	for _, p := range resp.Parts {
		result.Parts = append(result.Parts, Part{
			PartNumber:   int(aws.Int64Value(p.PartNumber)),
			ETag:         aws.StringValue(p.ETag),
			Size:         aws.Int64Value(p.Size),
			LastModified: aws.TimeValue(p.LastModified),
		})
	}

	if resp.NextPartNumberMarker != nil {
		result.NextPartNumberMarker = int(aws.Int64Value(resp.NextPartNumberMarker))
	}

	return result, nil
}

// putObjectMultipart handles large uploads with streaming multipart upload
func (s *S3Backend) putObjectMultipart(ctx context.Context, virtualBucket, realBucket, key string, reader io.Reader, metadata map[string]string) error {
	// Get the client for the virtual bucket
	client, err := s.getClientForBucket(virtualBucket)
	if err != nil {
		return fmt.Errorf("failed to get client for bucket: %w", err)
	}

	// Add prefix to key
	realKey := s.addPrefixToKey(virtualBucket, key)

	// Initiate multipart upload
	input := &s3.CreateMultipartUploadInput{
		Bucket: aws.String(realBucket),
		Key:    aws.String(realKey),
	}

	if len(metadata) > 0 {
		input.Metadata = make(map[string]*string)
		for k, v := range metadata {
			input.Metadata[k] = aws.String(v)
		}
	}

	resp, err := client.CreateMultipartUploadWithContext(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to initiate multipart upload: %w", err)
	}

	uploadID := resp.UploadId

	// Use 5MB parts
	partSize := int64(5 * 1024 * 1024)

	var parts []*s3.CompletedPart
	partNumber := int64(1)

	buf := make([]byte, partSize)
	for {
		n, readErr := io.ReadFull(reader, buf)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			// Abort upload on error
			_, _ = client.AbortMultipartUploadWithContext(ctx, &s3.AbortMultipartUploadInput{
				Bucket:   aws.String(realBucket),
				Key:      aws.String(realKey),
				UploadId: uploadID,
			})
			return fmt.Errorf("failed to read part: %w", readErr)
		}

		if n == 0 {
			break
		}

		// Upload this part
		partInput := &s3.UploadPartInput{
			Bucket:     aws.String(realBucket),
			Key:        aws.String(realKey),
			UploadId:   uploadID,
			PartNumber: aws.Int64(partNumber),
			Body:       bytes.NewReader(buf[:n]),
		}

		partResp, uploadErr := client.UploadPartWithContext(ctx, partInput)
		if uploadErr != nil {
			// Abort upload on error
			_, _ = client.AbortMultipartUploadWithContext(ctx, &s3.AbortMultipartUploadInput{
				Bucket:   aws.String(realBucket),
				Key:      aws.String(realKey),
				UploadId: uploadID,
			})
			return fmt.Errorf("failed to upload part %d: %w", partNumber, uploadErr)
		}

		parts = append(parts, &s3.CompletedPart{
			ETag:       partResp.ETag,
			PartNumber: aws.Int64(partNumber),
		})

		partNumber++

		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
	}

	// Complete multipart upload
	completeInput := &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(realBucket),
		Key:      aws.String(realKey),
		UploadId: uploadID,
		MultipartUpload: &s3.CompletedMultipartUpload{
			Parts: parts,
		},
	}

	_, err = client.CompleteMultipartUploadWithContext(ctx, completeInput)
	if err != nil {
		return fmt.Errorf("failed to complete multipart upload: %w", err)
	}

	// Invalidate cache
	s.metadataCache.Delete(fmt.Sprintf("%s/%s", virtualBucket, key))

	return nil
}
