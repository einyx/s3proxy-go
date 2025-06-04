package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/einyx/s3proxy-go/internal/config"
)

type FileSystemBackend struct {
	baseDir    string
	bufferPool sync.Pool
}

func NewFileSystemBackend(cfg *config.FileSystemConfig) (*FileSystemBackend, error) {
	if cfg.BaseDir == "" {
		return nil, fmt.Errorf("base directory is required for filesystem backend")
	}

	// Ensure base directory exists
	if err := os.MkdirAll(cfg.BaseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create base directory: %w", err)
	}

	return &FileSystemBackend{
		baseDir: cfg.BaseDir,
		bufferPool: sync.Pool{
			New: func() interface{} {
				return make([]byte, 64*1024) // 64KB buffers
			},
		},
	}, nil
}

func (fs *FileSystemBackend) ListBuckets(ctx context.Context) ([]BucketInfo, error) {
	entries, err := os.ReadDir(fs.baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read base directory: %w", err)
	}

	var buckets []BucketInfo
	for _, entry := range entries {
		if entry.IsDir() {
			info, err := entry.Info()
			if err == nil {
				buckets = append(buckets, BucketInfo{
					Name:         entry.Name(),
					CreationDate: info.ModTime(),
				})
			}
		}
	}

	return buckets, nil
}

func (fs *FileSystemBackend) CreateBucket(ctx context.Context, bucket string) error {
	bucketPath := filepath.Join(fs.baseDir, bucket)
	return os.MkdirAll(bucketPath, 0755)
}

func (fs *FileSystemBackend) DeleteBucket(ctx context.Context, bucket string) error {
	bucketPath := filepath.Join(fs.baseDir, bucket)
	return os.RemoveAll(bucketPath)
}

func (fs *FileSystemBackend) BucketExists(ctx context.Context, bucket string) (bool, error) {
	bucketPath := filepath.Join(fs.baseDir, bucket)
	info, err := os.Stat(bucketPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

func (fs *FileSystemBackend) ListObjects(ctx context.Context, bucket, prefix, marker string, maxKeys int) (*ListObjectsResult, error) {
	// Default to using delimiter for backward compatibility
	return fs.ListObjectsWithDelimiter(ctx, bucket, prefix, marker, "/", maxKeys)
}

func (fs *FileSystemBackend) ListObjectsWithDelimiter(ctx context.Context, bucket, prefix, marker, delimiter string, maxKeys int) (*ListObjectsResult, error) {
	bucketPath := filepath.Join(fs.baseDir, bucket)

	result := &ListObjectsResult{
		Contents:       make([]ObjectInfo, 0),
		CommonPrefixes: make([]string, 0),
	}

	// Track common prefixes for delimiter support
	prefixSet := make(map[string]bool)
	count := 0

	err := filepath.Walk(bucketPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || count >= maxKeys {
			return nil
		}

		// Get relative path from bucket
		relPath, err := filepath.Rel(bucketPath, path)
		if err != nil {
			return nil
		}

		// Convert to Unix-style path for S3 compatibility
		key := filepath.ToSlash(relPath)

		// Skip the root directory itself
		if key == "." {
			return nil
		}

		// Apply prefix filter
		if prefix != "" && !strings.HasPrefix(key, prefix) {
			return nil
		}

		// Apply marker filter
		if marker != "" && key <= marker {
			return nil
		}

		// Handle delimiter logic
		if delimiter != "" && prefix != "" {
			// Remove prefix to find the next component
			afterPrefix := strings.TrimPrefix(key, prefix)
			if afterPrefix != key { // Has the prefix
				delimIndex := strings.Index(afterPrefix, delimiter)
				if delimIndex >= 0 {
					// This is a "directory" - add to common prefixes
					commonPrefix := prefix + afterPrefix[:delimIndex+len(delimiter)]
					if !prefixSet[commonPrefix] {
						prefixSet[commonPrefix] = true
						result.CommonPrefixes = append(result.CommonPrefixes, commonPrefix)
						count++
					}
					return nil
				}
			}
		} else if delimiter != "" {
			// No prefix, check for delimiter from start
			delimIndex := strings.Index(key, delimiter)
			if delimIndex >= 0 {
				// This is a "directory" - add to common prefixes
				commonPrefix := key[:delimIndex+len(delimiter)]
				if !prefixSet[commonPrefix] {
					prefixSet[commonPrefix] = true
					result.CommonPrefixes = append(result.CommonPrefixes, commonPrefix)
					count++
				}
				return nil
			}
		}

		// Regular file - add to contents if not a directory
		if !info.IsDir() {
			result.Contents = append(result.Contents, ObjectInfo{
				Key:          key,
				Size:         info.Size(),
				ETag:         fmt.Sprintf("\"%x\"", info.ModTime().UnixNano()),
				LastModified: info.ModTime(),
				ContentType:  "application/octet-stream",
			})
			count++
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to walk bucket directory: %w", err)
	}

	result.IsTruncated = count >= maxKeys
	return result, nil
}

func (fs *FileSystemBackend) GetObject(ctx context.Context, bucket, key string) (*Object, error) {
	objectPath := filepath.Join(fs.baseDir, bucket, key)

	info, err := os.Stat(objectPath)
	if err != nil {
		return nil, fmt.Errorf("object not found: %w", err)
	}

	file, err := os.Open(objectPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open object: %w", err)
	}

	return &Object{
		Body:         file,
		ContentType:  "application/octet-stream",
		Size:         info.Size(),
		ETag:         fmt.Sprintf("\"%x\"", info.ModTime().UnixNano()),
		LastModified: info.ModTime(),
		Metadata:     make(map[string]string),
	}, nil
}

func (fs *FileSystemBackend) PutObject(ctx context.Context, bucket, key string, reader io.Reader, size int64, metadata map[string]string) error {
	objectPath := filepath.Join(fs.baseDir, bucket, key)

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(objectPath), 0755); err != nil {
		return fmt.Errorf("failed to create object directory: %w", err)
	}

	file, err := os.Create(objectPath)
	if err != nil {
		return fmt.Errorf("failed to create object file: %w", err)
	}
	defer file.Close()

	// Use buffer pool for efficient copying
	buf := fs.bufferPool.Get().([]byte)
	defer fs.bufferPool.Put(buf)

	_, err = io.CopyBuffer(file, reader, buf)
	if err != nil {
		return fmt.Errorf("failed to write object data: %w", err)
	}

	return nil
}

func (fs *FileSystemBackend) DeleteObject(ctx context.Context, bucket, key string) error {
	objectPath := filepath.Join(fs.baseDir, bucket, key)
	err := os.Remove(objectPath)
	if os.IsNotExist(err) {
		return nil // S3 behavior: deleting non-existent object succeeds
	}
	return err
}

func (fs *FileSystemBackend) HeadObject(ctx context.Context, bucket, key string) (*ObjectInfo, error) {
	objectPath := filepath.Join(fs.baseDir, bucket, key)

	info, err := os.Stat(objectPath)
	if err != nil {
		return nil, fmt.Errorf("object not found: %w", err)
	}

	return &ObjectInfo{
		Key:          key,
		Size:         info.Size(),
		ETag:         fmt.Sprintf("\"%x\"", info.ModTime().UnixNano()),
		LastModified: info.ModTime(),
		ContentType:  "application/octet-stream",
		Metadata:     make(map[string]string),
	}, nil
}

func (fs *FileSystemBackend) GetObjectACL(ctx context.Context, bucket, key string) (*ACL, error) {
	return &ACL{
		Owner: Owner{
			ID:          "filesystem",
			DisplayName: "FileSystem",
		},
		Grants: []Grant{
			{
				Grantee: Grantee{
					Type:        "CanonicalUser",
					ID:          "filesystem",
					DisplayName: "FileSystem",
				},
				Permission: "FULL_CONTROL",
			},
		},
	}, nil
}

func (fs *FileSystemBackend) PutObjectACL(ctx context.Context, bucket, key string, acl *ACL) error {
	// No-op for filesystem backend
	return nil
}

func (fs *FileSystemBackend) InitiateMultipartUpload(ctx context.Context, bucket, key string, metadata map[string]string) (string, error) {
	uploadID := fmt.Sprintf("%s-%d", key, time.Now().UnixNano())

	// Create a temporary directory for multipart upload
	uploadDir := filepath.Join(fs.baseDir, bucket, ".uploads", uploadID)
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create upload directory: %w", err)
	}

	return uploadID, nil
}

func (fs *FileSystemBackend) UploadPart(ctx context.Context, bucket, key, uploadID string, partNumber int, reader io.Reader, size int64) (string, error) {
	uploadDir := filepath.Join(fs.baseDir, bucket, ".uploads", uploadID)
	partPath := filepath.Join(uploadDir, fmt.Sprintf("part-%d", partNumber))

	file, err := os.Create(partPath)
	if err != nil {
		return "", fmt.Errorf("failed to create part file: %w", err)
	}
	defer file.Close()

	buf := fs.bufferPool.Get().([]byte)
	defer fs.bufferPool.Put(buf)

	_, err = io.CopyBuffer(file, reader, buf)
	if err != nil {
		return "", fmt.Errorf("failed to write part data: %w", err)
	}

	return fmt.Sprintf("\"%d\"", partNumber), nil
}

func (fs *FileSystemBackend) CompleteMultipartUpload(ctx context.Context, bucket, key, uploadID string, parts []CompletedPart) error {
	uploadDir := filepath.Join(fs.baseDir, bucket, ".uploads", uploadID)
	objectPath := filepath.Join(fs.baseDir, bucket, key)

	// Ensure object directory exists
	if err := os.MkdirAll(filepath.Dir(objectPath), 0755); err != nil {
		return fmt.Errorf("failed to create object directory: %w", err)
	}

	file, err := os.Create(objectPath)
	if err != nil {
		return fmt.Errorf("failed to create object file: %w", err)
	}
	defer file.Close()

	// Concatenate parts in order
	for _, part := range parts {
		partPath := filepath.Join(uploadDir, fmt.Sprintf("part-%d", part.PartNumber))
		partFile, err := os.Open(partPath)
		if err != nil {
			return fmt.Errorf("failed to open part %d: %w", part.PartNumber, err)
		}

		buf := fs.bufferPool.Get().([]byte)
		_, err = io.CopyBuffer(file, partFile, buf)
		partFile.Close()
		fs.bufferPool.Put(buf)

		if err != nil {
			return fmt.Errorf("failed to copy part %d: %w", part.PartNumber, err)
		}
	}

	// Clean up upload directory
	os.RemoveAll(uploadDir)

	return nil
}

func (fs *FileSystemBackend) AbortMultipartUpload(ctx context.Context, bucket, key, uploadID string) error {
	uploadDir := filepath.Join(fs.baseDir, bucket, ".uploads", uploadID)
	return os.RemoveAll(uploadDir)
}

func (fs *FileSystemBackend) ListParts(ctx context.Context, bucket, key, uploadID string, maxParts int, partNumberMarker int) (*ListPartsResult, error) {
	uploadDir := filepath.Join(fs.baseDir, bucket, ".uploads", uploadID)

	entries, err := os.ReadDir(uploadDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read upload directory: %w", err)
	}

	result := &ListPartsResult{
		Bucket:   bucket,
		Key:      key,
		UploadID: uploadID,
		Parts:    make([]Part, 0),
	}

	count := 0
	for _, entry := range entries {
		if count >= maxParts {
			break
		}

		if strings.HasPrefix(entry.Name(), "part-") {
			var partNumber int
			if _, err := fmt.Sscanf(entry.Name(), "part-%d", &partNumber); err == nil {
				if partNumber > partNumberMarker {
					info, err := entry.Info()
					if err == nil {
						result.Parts = append(result.Parts, Part{
							PartNumber:   partNumber,
							ETag:         fmt.Sprintf("\"%d\"", partNumber),
							Size:         info.Size(),
							LastModified: info.ModTime(),
						})
						count++
					}
				}
			}
		}
	}

	result.IsTruncated = count >= maxParts
	return result, nil
}
