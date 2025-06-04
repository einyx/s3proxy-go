package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-storage-blob-go/azblob"
	"github.com/einyx/s3proxy-go/internal/config"
	"github.com/sirupsen/logrus"
)

type AzureBackend struct {
	serviceURL    azblob.ServiceURL
	credential    azblob.Credential
	accountName   string
	containerName string
	bufferPool    sync.Pool
}

func NewAzureBackend(cfg *config.AzureStorageConfig) (*AzureBackend, error) {
	var credential azblob.Credential
	var err error

	if cfg.UseSAS {
		credential = azblob.NewAnonymousCredential()
	} else {
		credential, err = azblob.NewSharedKeyCredential(cfg.AccountName, cfg.AccountKey)
		if err != nil {
			return nil, fmt.Errorf("invalid credentials: %w", err)
		}
	}

	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://%s.blob.core.windows.net", cfg.AccountName)
	}

	if cfg.UseSAS && cfg.SASToken != "" {
		if !strings.Contains(endpoint, "?") {
			endpoint += "?" + cfg.SASToken
		} else {
			endpoint += "&" + cfg.SASToken
		}
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid endpoint: %w", err)
	}

	containerName := cfg.ContainerName
	if containerName == "" {
		containerName = "$root"
	}

	// AZURE SDK: Balanced performance configuration
	pipelineOptions := azblob.PipelineOptions{
		Retry: azblob.RetryOptions{
			Policy:        azblob.RetryPolicyExponential, // Better retry policy
			MaxTries:      3,                             // Reasonable retries
			TryTimeout:    30 * time.Second,              // Reasonable timeout
			RetryDelay:    1 * time.Second,               // Short delay
			MaxRetryDelay: 5 * time.Second,               // Max delay
		},
		Telemetry: azblob.TelemetryOptions{
			Value: "s3proxy-speed/1.0", // SDK SPEED: Minimal user agent
		},
		// SDK AZURE: Use default HTTP sender (simpler approach)
		HTTPSender: nil,
	}
	pipeline := azblob.NewPipeline(credential, pipelineOptions)
	serviceURL := azblob.NewServiceURL(*u, pipeline)

	return &AzureBackend{
		serviceURL:    serviceURL,
		credential:    credential,
		accountName:   cfg.AccountName,
		containerName: containerName,
		bufferPool: sync.Pool{
			New: func() interface{} {
				return make([]byte, 1024*1024) // 1MB buffers for better performance (16x increase)
			},
		},
	}, nil
}

func (a *AzureBackend) ListBuckets(ctx context.Context) ([]BucketInfo, error) {
	var buckets []BucketInfo

	for marker := (azblob.Marker{}); marker.NotDone(); {
		listContainer, err := a.serviceURL.ListContainersSegment(ctx, marker, azblob.ListContainersSegmentOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to list containers: %w", err)
		}

		for _, container := range listContainer.ContainerItems {
			buckets = append(buckets, BucketInfo{
				Name:         container.Name,
				CreationDate: container.Properties.LastModified,
			})
		}

		marker = listContainer.NextMarker
	}

	return buckets, nil
}

func (a *AzureBackend) CreateBucket(ctx context.Context, bucket string) error {
	containerURL := a.serviceURL.NewContainerURL(bucket)
	_, err := containerURL.Create(ctx, azblob.Metadata{}, azblob.PublicAccessNone)
	if err != nil {
		return fmt.Errorf("failed to create container: %w", err)
	}
	return nil
}

func (a *AzureBackend) DeleteBucket(ctx context.Context, bucket string) error {
	containerURL := a.serviceURL.NewContainerURL(bucket)
	_, err := containerURL.Delete(ctx, azblob.ContainerAccessConditions{})
	if err != nil {
		return fmt.Errorf("failed to delete container: %w", err)
	}
	return nil
}

func (a *AzureBackend) BucketExists(ctx context.Context, bucket string) (bool, error) {
	containerURL := a.serviceURL.NewContainerURL(bucket)
	_, err := containerURL.GetProperties(ctx, azblob.LeaseAccessConditions{})
	if err != nil {
		if stgErr, ok := err.(azblob.StorageError); ok {
			if stgErr.ServiceCode() == azblob.ServiceCodeContainerNotFound {
				return false, nil
			}
		}
		return false, err
	}
	return true, nil
}

func (a *AzureBackend) ListObjects(ctx context.Context, bucket, prefix, marker string, maxKeys int) (*ListObjectsResult, error) {
	// Default to using delimiter for backward compatibility
	return a.ListObjectsWithDelimiter(ctx, bucket, prefix, marker, "/", maxKeys)
}

func (a *AzureBackend) ListObjectsWithDelimiter(ctx context.Context, bucket, prefix, marker, delimiter string, maxKeys int) (*ListObjectsResult, error) {
	containerURL := a.serviceURL.NewContainerURL(bucket)

	options := azblob.ListBlobsSegmentOptions{
		Prefix:     prefix,
		MaxResults: int32(maxKeys),
	}

	result := &ListObjectsResult{
		Contents:       make([]ObjectInfo, 0),
		CommonPrefixes: make([]string, 0),
	}

	azMarker := azblob.Marker{}
	if marker != "" {
		azMarker.Val = &marker
	}

	// Azure Blob Storage uses hierarchical listing
	if delimiter != "" {
		resp, err := containerURL.ListBlobsHierarchySegment(ctx, azMarker, delimiter, options)
		if err != nil {
			return nil, fmt.Errorf("failed to list blobs: %w", err)
		}

		// Add blob items
		for _, blob := range resp.Segment.BlobItems {
			result.Contents = append(result.Contents, ObjectInfo{
				Key:          blob.Name,
				Size:         *blob.Properties.ContentLength,
				ETag:         string(blob.Properties.Etag),
				LastModified: blob.Properties.LastModified,
			})
		}

		// Add blob prefixes (directories)
		for _, prefix := range resp.Segment.BlobPrefixes {
			result.CommonPrefixes = append(result.CommonPrefixes, prefix.Name)
		}

		result.IsTruncated = resp.NextMarker.NotDone()
		if resp.NextMarker.Val != nil {
			result.NextMarker = *resp.NextMarker.Val
		}
	} else {
		// Flat listing without delimiter
		resp, err := containerURL.ListBlobsFlatSegment(ctx, azMarker, options)
		if err != nil {
			return nil, fmt.Errorf("failed to list blobs: %w", err)
		}

		for _, blob := range resp.Segment.BlobItems {
			result.Contents = append(result.Contents, ObjectInfo{
				Key:          blob.Name,
				Size:         *blob.Properties.ContentLength,
				ETag:         string(blob.Properties.Etag),
				LastModified: blob.Properties.LastModified,
			})
		}

		result.IsTruncated = resp.NextMarker.NotDone()
		if resp.NextMarker.Val != nil {
			result.NextMarker = *resp.NextMarker.Val
		}
	}

	return result, nil
}

func (a *AzureBackend) GetObject(ctx context.Context, bucket, key string) (*Object, error) {
	containerURL := a.serviceURL.NewContainerURL(bucket)
	blobURL := containerURL.NewBlobURL(key)

	// AZURE SPECIFIC: Optimized download with aggressive settings
	resp, err := blobURL.Download(ctx, 0, azblob.CountToEnd, azblob.BlobAccessConditions{}, false, azblob.ClientProvidedKeyOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to download blob: %w", err)
	}

	// AZURE OPTIMIZATION: Use parallel download for smaller files (5MB threshold)
	if resp.ContentLength() > 5*1024*1024 { // > 5MB (reduced from 10MB)
		// Close the initial response
		resp.Body(azblob.RetryReaderOptions{}).Close()

		// AZURE SPECIFIC: Ultra-optimized reader for large blob downloads
		reader := &optimizedReader{
			ctx:       ctx,
			blobURL:   blobURL,
			totalSize: resp.ContentLength(),
			chunkSize: 16 * 1024 * 1024, // 16MB chunks for Azure optimal throughput (doubled)
			prefetch:  4,                // Prefetch 4 chunks ahead for better pipelining (doubled)
		}

		// Start prefetching immediately
		if err := reader.Start(); err != nil {
			return nil, fmt.Errorf("failed to start optimized reader: %w", err)
		}

		return &Object{
			Body:         reader,
			ContentType:  resp.ContentType(),
			Size:         resp.ContentLength(),
			ETag:         string(resp.ETag()),
			LastModified: resp.LastModified(),
			Metadata:     resp.NewMetadata(),
		}, nil
	}

	// For small files, use direct streaming
	bodyStream := resp.Body(azblob.RetryReaderOptions{
		MaxRetryRequests: 3,
	})

	return &Object{
		Body:         bodyStream,
		ContentType:  resp.ContentType(),
		Size:         resp.ContentLength(),
		ETag:         string(resp.ETag()),
		LastModified: resp.LastModified(),
		Metadata:     resp.NewMetadata(),
	}, nil
}

func (a *AzureBackend) PutObject(ctx context.Context, bucket, key string, reader io.Reader, size int64, metadata map[string]string) error {
	containerURL := a.serviceURL.NewContainerURL(bucket)
	blobURL := containerURL.NewBlockBlobURL(key)

	// S3-COMPATIBLE AZURE OPTIMIZATION: Match S3 behavior for consistency
	if size > 0 && size <= 100*1024*1024 { // Match S3 100MB threshold
		// S3-STYLE: Single PUT for files under 100MB
		data := make([]byte, size)
		_, err := io.ReadFull(reader, data)
		if err != nil {
			return fmt.Errorf("failed to read data: %w", err)
		}

		// AZURE S3-COMPATIBLE: Optimize headers to match S3 behavior
		_, err = blobURL.Upload(ctx, bytes.NewReader(data), azblob.BlobHTTPHeaders{
			ContentType:        "application/octet-stream", // Match S3 content type
			ContentEncoding:    "",                         // No encoding for speed
			ContentLanguage:    "",                         // No language detection
			ContentDisposition: "",                         // No disposition processing
			CacheControl:       "",                         // No cache control overhead
		}, azblob.Metadata(metadata), azblob.BlobAccessConditions{}, azblob.AccessTierNone, nil, azblob.ClientProvidedKeyOptions{}, azblob.ImmutabilityPolicyOptions{})
		if err != nil {
			return fmt.Errorf("failed to upload blob: %w", err)
		}
	} else {
		// AZURE STREAMING OPTIMIZATION: For very large objects
		_, err := azblob.UploadStreamToBlockBlob(ctx, reader, blobURL, azblob.UploadStreamToBlockBlobOptions{
			BufferSize: 16 * 1024 * 1024, // 16MB buffers for Azure optimal throughput
			MaxBuffers: 32,               // Maximum buffers for Azure performance
			Metadata:   azblob.Metadata(metadata),
			// AZURE SPECIFIC: Content type optimization
			BlobHTTPHeaders: azblob.BlobHTTPHeaders{
				ContentType: "application/octet-stream",
			},
		})
		if err != nil {
			return fmt.Errorf("failed to upload blob: %w", err)
		}
	}

	return nil
}

func (a *AzureBackend) DeleteObject(ctx context.Context, bucket, key string) error {
	containerURL := a.serviceURL.NewContainerURL(bucket)
	blobURL := containerURL.NewBlobURL(key)

	_, err := blobURL.Delete(ctx, azblob.DeleteSnapshotsOptionInclude, azblob.BlobAccessConditions{})
	if err != nil {
		return fmt.Errorf("failed to delete blob: %w", err)
	}

	return nil
}

func (a *AzureBackend) HeadObject(ctx context.Context, bucket, key string) (*ObjectInfo, error) {
	containerURL := a.serviceURL.NewContainerURL(bucket)
	blobURL := containerURL.NewBlobURL(key)

	props, err := blobURL.GetProperties(ctx, azblob.BlobAccessConditions{}, azblob.ClientProvidedKeyOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get blob properties: %w", err)
	}

	return &ObjectInfo{
		Key:          key,
		Size:         props.ContentLength(),
		ETag:         string(props.ETag()),
		LastModified: props.LastModified(),
		Metadata:     props.NewMetadata(),
	}, nil
}

func (a *AzureBackend) GetObjectACL(ctx context.Context, bucket, key string) (*ACL, error) {
	// Azure doesn't have per-object ACLs like S3
	// Return a default ACL
	return &ACL{
		Owner: Owner{
			ID:          a.accountName,
			DisplayName: a.accountName,
		},
		Grants: []Grant{
			{
				Grantee: Grantee{
					Type:        "CanonicalUser",
					ID:          a.accountName,
					DisplayName: a.accountName,
				},
				Permission: "FULL_CONTROL",
			},
		},
	}, nil
}

func (a *AzureBackend) PutObjectACL(ctx context.Context, bucket, key string, acl *ACL) error {
	// Azure doesn't support per-object ACLs
	// Log and ignore
	logrus.Debug("PutObjectACL not supported for Azure backend")
	return nil
}

func (a *AzureBackend) InitiateMultipartUpload(ctx context.Context, bucket, key string, metadata map[string]string) (string, error) {
	// Azure doesn't have the same multipart upload concept as S3
	// Generate a unique upload ID
	uploadID := fmt.Sprintf("%s-%d", key, time.Now().UnixNano())
	return uploadID, nil
}

func (a *AzureBackend) UploadPart(ctx context.Context, bucket, key, uploadID string, partNumber int, reader io.Reader, size int64) (string, error) {
	containerURL := a.serviceURL.NewContainerURL(bucket)
	blobURL := containerURL.NewBlockBlobURL(key)

	data, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("failed to read part data: %w", err)
	}

	blockID := fmt.Sprintf("%05d", partNumber)
	_, err = blobURL.StageBlock(ctx, blockID, bytes.NewReader(data), azblob.LeaseAccessConditions{}, nil, azblob.ClientProvidedKeyOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to stage block: %w", err)
	}

	return fmt.Sprintf("\"%s\"", blockID), nil
}

func (a *AzureBackend) CompleteMultipartUpload(ctx context.Context, bucket, key, uploadID string, parts []CompletedPart) error {
	containerURL := a.serviceURL.NewContainerURL(bucket)
	blobURL := containerURL.NewBlockBlobURL(key)

	blockList := make([]string, len(parts))
	for i, part := range parts {
		blockList[i] = fmt.Sprintf("%05d", part.PartNumber)
	}

	_, err := blobURL.CommitBlockList(ctx, blockList, azblob.BlobHTTPHeaders{}, azblob.Metadata{}, azblob.BlobAccessConditions{}, azblob.AccessTierNone, nil, azblob.ClientProvidedKeyOptions{}, azblob.ImmutabilityPolicyOptions{})
	if err != nil {
		return fmt.Errorf("failed to commit block list: %w", err)
	}

	return nil
}

func (a *AzureBackend) AbortMultipartUpload(ctx context.Context, bucket, key, uploadID string) error {
	// No specific action needed for Azure
	logrus.Debug("AbortMultipartUpload called for Azure backend")
	return nil
}

func (a *AzureBackend) ListParts(ctx context.Context, bucket, key, uploadID string, maxParts int, partNumberMarker int) (*ListPartsResult, error) {
	containerURL := a.serviceURL.NewContainerURL(bucket)
	blobURL := containerURL.NewBlockBlobURL(key)

	resp, err := blobURL.GetBlockList(ctx, azblob.BlockListUncommitted, azblob.LeaseAccessConditions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get block list: %w", err)
	}

	result := &ListPartsResult{
		Bucket:   bucket,
		Key:      key,
		UploadID: uploadID,
		Parts:    make([]Part, 0),
	}

	for i, block := range resp.UncommittedBlocks {
		if i+1 > partNumberMarker && len(result.Parts) < maxParts {
			result.Parts = append(result.Parts, Part{
				PartNumber: i + 1,
				Size:       block.Size,
				ETag:       block.Name,
			})
		}
	}

	result.IsTruncated = len(resp.UncommittedBlocks) > partNumberMarker+maxParts
	if result.IsTruncated {
		result.NextPartNumberMarker = partNumberMarker + len(result.Parts)
	}

	return result, nil
}

// optimizedReader implements efficient chunk-based reading with prefetching
type optimizedReader struct {
	ctx       context.Context
	blobURL   azblob.BlobURL
	totalSize int64
	chunkSize int64
	prefetch  int

	currentChunk  []byte
	currentOffset int64
	chunkIndex    int64

	prefetchChan   chan *chunk
	prefetchCancel context.CancelFunc
	wg             sync.WaitGroup
}

type chunk struct {
	index int64
	data  []byte
	err   error
}

func (r *optimizedReader) Start() error {
	// Create prefetch context
	prefetchCtx, cancel := context.WithCancel(r.ctx)
	r.prefetchCancel = cancel

	// Start prefetch channel
	r.prefetchChan = make(chan *chunk, r.prefetch)

	// Start prefetching
	r.wg.Add(1)
	go r.prefetchLoop(prefetchCtx)

	return nil
}

func (r *optimizedReader) prefetchLoop(ctx context.Context) {
	defer r.wg.Done()
	defer close(r.prefetchChan)

	nextChunk := int64(0)
	numChunks := (r.totalSize + r.chunkSize - 1) / r.chunkSize

	// Prefetch initial chunks
	for i := 0; i < r.prefetch && nextChunk < numChunks; i++ {
		select {
		case <-ctx.Done():
			return
		case r.prefetchChan <- r.downloadChunk(ctx, nextChunk):
			nextChunk++
		}
	}

	// Continue prefetching as chunks are consumed
	for nextChunk < numChunks {
		select {
		case <-ctx.Done():
			return
		case r.prefetchChan <- r.downloadChunk(ctx, nextChunk):
			nextChunk++
		}
	}
}

func (r *optimizedReader) downloadChunk(ctx context.Context, index int64) *chunk {
	offset := index * r.chunkSize
	count := r.chunkSize
	if offset+count > r.totalSize {
		count = r.totalSize - offset
	}

	// Download with retry
	for retry := 0; retry < 3; retry++ {
		select {
		case <-ctx.Done():
			return &chunk{index: index, err: ctx.Err()}
		default:
		}

		resp, err := r.blobURL.Download(ctx, offset, count, azblob.BlobAccessConditions{}, false, azblob.ClientProvidedKeyOptions{})
		if err != nil {
			if retry < 2 {
				time.Sleep(time.Millisecond * 50 * time.Duration(retry+1))
				continue
			}
			return &chunk{index: index, err: err}
		}

		bodyStream := resp.Body(azblob.RetryReaderOptions{MaxRetryRequests: 1})
		data, err := io.ReadAll(bodyStream)
		bodyStream.Close()

		if err == nil {
			return &chunk{index: index, data: data}
		}

		if retry < 2 {
			time.Sleep(time.Millisecond * 50 * time.Duration(retry+1))
		}
	}

	return &chunk{index: index, err: fmt.Errorf("failed after retries")}
}

func (r *optimizedReader) Read(p []byte) (n int, err error) {
	if r.currentOffset >= r.totalSize {
		return 0, io.EOF
	}

	totalRead := 0

	for totalRead < len(p) && r.currentOffset < r.totalSize {
		// Load next chunk if needed
		if r.currentChunk == nil || int64(len(r.currentChunk)) <= r.currentOffset%r.chunkSize {
			select {
			case chunk := <-r.prefetchChan:
				if chunk == nil {
					return totalRead, io.ErrUnexpectedEOF
				}
				if chunk.err != nil {
					return totalRead, chunk.err
				}
				r.currentChunk = chunk.data
				r.chunkIndex = chunk.index
			case <-r.ctx.Done():
				return totalRead, r.ctx.Err()
			}
		}

		// Copy from current chunk
		chunkOffset := r.currentOffset % r.chunkSize
		available := int64(len(r.currentChunk)) - chunkOffset
		toCopy := int64(len(p) - totalRead)
		if toCopy > available {
			toCopy = available
		}

		copy(p[totalRead:], r.currentChunk[chunkOffset:chunkOffset+toCopy])
		totalRead += int(toCopy)
		r.currentOffset += toCopy

		// Clear chunk if fully consumed
		if chunkOffset+toCopy >= int64(len(r.currentChunk)) {
			r.currentChunk = nil
		}
	}

	return totalRead, nil
}

func (r *optimizedReader) Close() error {
	if r.prefetchCancel != nil {
		r.prefetchCancel()
	}
	r.wg.Wait()
	return nil
}
