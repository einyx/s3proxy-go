package storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	"github.com/einyx/s3proxy-go/internal/config"
)

func TestNewFileSystemBackend(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *config.FileSystemConfig
		wantErr     bool
		errContains string
	}{
		{
			name: "valid config",
			cfg: &config.FileSystemConfig{
				BaseDir: "/tmp/test",
			},
			wantErr: false,
		},
		{
			name:        "missing base dir",
			cfg:         &config.FileSystemConfig{},
			wantErr:     true,
			errContains: "base directory is required",
		},
		{
			name: "empty base dir",
			cfg: &config.FileSystemConfig{
				BaseDir: "",
			},
			wantErr:     true,
			errContains: "base directory is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp directory for valid configs
			if !tt.wantErr && tt.cfg.BaseDir != "" {
				tempDir, err := os.MkdirTemp("", "fs-test-*")
				if err != nil {
					t.Fatalf("Failed to create temp dir: %v", err)
				}
				defer os.RemoveAll(tempDir)
				tt.cfg.BaseDir = tempDir
			}

			_, err := NewFileSystemBackend(tt.cfg)

			if tt.wantErr {
				if err == nil {
					t.Errorf("NewFileSystemBackend() expected error but got none")
				} else if tt.errContains != "" && !contains(err.Error(), tt.errContains) {
					t.Errorf("NewFileSystemBackend() error = %v, want error containing %v", err, tt.errContains)
				}
			} else {
				if err != nil {
					t.Errorf("NewFileSystemBackend() unexpected error = %v", err)
				}
			}
		})
	}
}

func TestFileSystemBackend_Operations(t *testing.T) {
	ctx := context.Background()

	// Create temporary directory for tests
	tempDir, err := os.MkdirTemp("", "s3proxy-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	backend, err := NewFileSystemBackend(&config.FileSystemConfig{
		BaseDir: tempDir,
	})
	if err != nil {
		t.Fatalf("Failed to create filesystem backend: %v", err)
	}

	t.Run("CreateBucket and BucketExists", func(t *testing.T) {
		bucket := "test-bucket"

		// Create bucket
		err := backend.CreateBucket(ctx, bucket)
		if err != nil {
			t.Fatalf("CreateBucket() error = %v", err)
		}

		// Check exists
		exists, err := backend.BucketExists(ctx, bucket)
		if err != nil {
			t.Fatalf("BucketExists() error = %v", err)
		}
		if !exists {
			t.Error("BucketExists() = false, want true")
		}
	})

	t.Run("ListBuckets", func(t *testing.T) {
		// Create a few buckets
		buckets := []string{"bucket1", "bucket2", "bucket3"}
		for _, b := range buckets {
			if err := backend.CreateBucket(ctx, b); err != nil {
				t.Fatalf("CreateBucket(%s) error = %v", b, err)
			}
		}

		// List buckets
		result, err := backend.ListBuckets(ctx)
		if err != nil {
			t.Fatalf("ListBuckets() error = %v", err)
		}

		// Should have at least the buckets we created
		if len(result) < len(buckets) {
			t.Errorf("ListBuckets() returned %d buckets, want at least %d", len(result), len(buckets))
		}
	})

	t.Run("PutObject and GetObject", func(t *testing.T) {
		bucket := "put-get-bucket"
		key := "test-key.txt"
		content := []byte("test content")

		// Create bucket
		backend.CreateBucket(ctx, bucket)

		// Put object
		err := backend.PutObject(ctx, bucket, key, bytes.NewReader(content), int64(len(content)), nil)
		if err != nil {
			t.Fatalf("PutObject() error = %v", err)
		}

		// Get object
		obj, err := backend.GetObject(ctx, bucket, key)
		if err != nil {
			t.Fatalf("GetObject() error = %v", err)
		}
		defer obj.Body.Close()

		if obj.Size != int64(len(content)) {
			t.Errorf("GetObject() size = %v, want %v", obj.Size, len(content))
		}

		data, err := io.ReadAll(obj.Body)
		if err != nil {
			t.Fatalf("Failed to read object: %v", err)
		}

		if !bytes.Equal(data, content) {
			t.Errorf("GetObject() content = %v, want %v", data, content)
		}
	})

	t.Run("DeleteObject", func(t *testing.T) {
		bucket := "delete-bucket"
		key := "delete-test.txt"
		content := []byte("delete me")

		// Create bucket
		backend.CreateBucket(ctx, bucket)

		// Put object
		err := backend.PutObject(ctx, bucket, key, bytes.NewReader(content), int64(len(content)), nil)
		if err != nil {
			t.Fatalf("PutObject() error = %v", err)
		}

		// Delete object
		err = backend.DeleteObject(ctx, bucket, key)
		if err != nil {
			t.Fatalf("DeleteObject() error = %v", err)
		}

		// Verify deleted
		_, err = backend.GetObject(ctx, bucket, key)
		if err == nil {
			t.Error("GetObject() expected error after delete")
		}
	})

	t.Run("HeadObject", func(t *testing.T) {
		bucket := "head-bucket"
		key := "head-test.txt"
		content := []byte("head test content")

		// Create bucket
		backend.CreateBucket(ctx, bucket)

		// Put object
		err := backend.PutObject(ctx, bucket, key, bytes.NewReader(content), int64(len(content)), nil)
		if err != nil {
			t.Fatalf("PutObject() error = %v", err)
		}

		// Head object
		info, err := backend.HeadObject(ctx, bucket, key)
		if err != nil {
			t.Fatalf("HeadObject() error = %v", err)
		}

		if info.Size != int64(len(content)) {
			t.Errorf("HeadObject() size = %v, want %v", info.Size, len(content))
		}

		if info.Key != key {
			t.Errorf("HeadObject() key = %v, want %v", info.Key, key)
		}
	})

	t.Run("ListObjects", func(t *testing.T) {
		bucket := "list-bucket"

		// Create bucket
		backend.CreateBucket(ctx, bucket)

		// Put multiple objects
		objects := []string{
			"dir1/file1.txt",
			"dir1/file2.txt",
			"dir2/file3.txt",
			"file4.txt",
		}

		for _, key := range objects {
			content := []byte("content for " + key)
			err := backend.PutObject(ctx, bucket, key, bytes.NewReader(content), int64(len(content)), nil)
			if err != nil {
				t.Fatalf("PutObject() error = %v", err)
			}
		}

		// List all objects (note: ListObjects uses delimiter by default)
		result, err := backend.ListObjects(ctx, bucket, "", "", 1000)
		if err != nil {
			t.Fatalf("ListObjects() error = %v", err)
		}

		// Since ListObjects uses delimiter by default, we expect only root-level objects
		// which is just "file4.txt"
		if len(result.Contents) != 1 {
			t.Errorf("ListObjects() objects = %v, want 1", len(result.Contents))
		}

		// Should have 2 common prefixes (dir1/ and dir2/)
		if len(result.CommonPrefixes) != 2 {
			t.Errorf("ListObjects() prefixes = %v, want 2", len(result.CommonPrefixes))
		}
	})

	t.Run("ListObjectsWithDelimiter", func(t *testing.T) {
		bucket := "list-delim-bucket"

		// Create bucket
		backend.CreateBucket(ctx, bucket)

		// Put objects
		objects := []string{
			"dir1/file1.txt",
			"dir1/file2.txt",
			"dir2/file3.txt",
			"file4.txt",
		}

		for _, key := range objects {
			content := []byte("content")
			err := backend.PutObject(ctx, bucket, key, bytes.NewReader(content), int64(len(content)), nil)
			if err != nil {
				t.Fatalf("PutObject() error = %v", err)
			}
		}

		// List with delimiter
		result, err := backend.ListObjectsWithDelimiter(ctx, bucket, "", "", "/", 1000)
		if err != nil {
			t.Fatalf("ListObjectsWithDelimiter() error = %v", err)
		}

		// Should have 1 object at root and 2 prefixes
		if len(result.Contents) != 1 {
			t.Errorf("ListObjectsWithDelimiter() objects = %v, want 1", len(result.Contents))
		}

		if len(result.CommonPrefixes) != 2 {
			t.Errorf("ListObjectsWithDelimiter() prefixes = %v, want 2", len(result.CommonPrefixes))
		}
	})

	t.Run("DeleteBucket", func(t *testing.T) {
		bucket := "delete-bucket-test"

		// Create bucket
		err := backend.CreateBucket(ctx, bucket)
		if err != nil {
			t.Fatalf("CreateBucket() error = %v", err)
		}

		// Delete bucket
		err = backend.DeleteBucket(ctx, bucket)
		if err != nil {
			t.Fatalf("DeleteBucket() error = %v", err)
		}

		// Verify deleted
		exists, err := backend.BucketExists(ctx, bucket)
		if err != nil {
			t.Fatalf("BucketExists() error = %v", err)
		}
		if exists {
			t.Error("BucketExists() = true after delete")
		}
	})
}

func TestFileSystemBackend_Multipart(t *testing.T) {
	ctx := context.Background()

	tempDir, err := os.MkdirTemp("", "s3proxy-multipart-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	backend, err := NewFileSystemBackend(&config.FileSystemConfig{
		BaseDir: tempDir,
	})
	if err != nil {
		t.Fatalf("Failed to create filesystem backend: %v", err)
	}

	bucket := "multipart-bucket"
	key := "multipart.txt"

	// Create bucket
	backend.CreateBucket(ctx, bucket)

	t.Run("InitiateMultipartUpload", func(t *testing.T) {
		uploadID, err := backend.InitiateMultipartUpload(ctx, bucket, key, nil)
		if err != nil {
			t.Fatalf("InitiateMultipartUpload() error = %v", err)
		}

		if uploadID == "" {
			t.Error("InitiateMultipartUpload() returned empty upload ID")
		}

		// Abort for cleanup
		backend.AbortMultipartUpload(ctx, bucket, key, uploadID)
	})

	t.Run("Complete multipart upload flow", func(t *testing.T) {
		// Initiate
		uploadID, err := backend.InitiateMultipartUpload(ctx, bucket, key, nil)
		if err != nil {
			t.Fatalf("InitiateMultipartUpload() error = %v", err)
		}

		// Upload parts
		part1 := []byte("part 1 content")
		part2 := []byte("part 2 content")

		etag1, err := backend.UploadPart(ctx, bucket, key, uploadID, 1, bytes.NewReader(part1), int64(len(part1)))
		if err != nil {
			t.Fatalf("UploadPart(1) error = %v", err)
		}

		etag2, err := backend.UploadPart(ctx, bucket, key, uploadID, 2, bytes.NewReader(part2), int64(len(part2)))
		if err != nil {
			t.Fatalf("UploadPart(2) error = %v", err)
		}

		// Complete upload
		parts := []CompletedPart{
			{PartNumber: 1, ETag: etag1},
			{PartNumber: 2, ETag: etag2},
		}

		err = backend.CompleteMultipartUpload(ctx, bucket, key, uploadID, parts)
		if err != nil {
			t.Fatalf("CompleteMultipartUpload() error = %v", err)
		}

		// Verify object exists
		obj, err := backend.GetObject(ctx, bucket, key)
		if err != nil {
			t.Fatalf("GetObject() after multipart error = %v", err)
		}
		defer obj.Body.Close()

		data, _ := io.ReadAll(obj.Body)
		expected := append(part1, part2...)
		if !bytes.Equal(data, expected) {
			t.Errorf("Multipart object content = %v, want %v", data, expected)
		}
	})
}
