package main

import (
	"context"
	"log"
	"os"

	"github.com/einyx/s3proxy-go/internal/config"
	"github.com/einyx/s3proxy-go/internal/encryption"
	"github.com/einyx/s3proxy-go/internal/storage"
)

// Example of how to integrate encryption into the S3 proxy server
func main() {
	// Load configuration
	cfg, err := config.Load(os.Getenv("CONFIG_FILE"))
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Create the base storage backend
	var backend storage.Backend
	switch cfg.Storage.Provider {
	case "s3":
		backend, err = storage.NewS3Backend(cfg.Storage.S3)
	case "azure", "azureblob":
		backend, err = storage.NewAzureBackend(cfg.Storage.Azure)
	case "filesystem":
		backend, err = storage.NewFileSystemBackend(cfg.Storage.FileSystem)
	default:
		log.Fatalf("Unknown storage provider: %s", cfg.Storage.Provider)
	}

	if err != nil {
		log.Fatalf("Failed to create storage backend: %v", err)
	}

	// Create encryption manager if enabled
	if cfg.Encryption.Enabled {
		ctx := context.Background()
		encryptionManager, err := encryption.NewFromConfig(ctx, &cfg.Encryption)
		if err != nil {
			log.Fatalf("Failed to create encryption manager: %v", err)
		}

		// Wrap the backend with encryption
		backend = storage.NewEncryptedBackend(backend, encryptionManager)

		log.Printf("Encryption enabled with %s algorithm and %s key provider",
			cfg.Encryption.Algorithm, cfg.Encryption.KeyProvider)
	}

	// Now use the backend (potentially encrypted) for S3 operations
	// Example: List buckets
	ctx := context.Background()
	buckets, err := backend.ListBuckets(ctx)
	if err != nil {
		log.Fatalf("Failed to list buckets: %v", err)
	}

	log.Printf("Found %d buckets", len(buckets))

	// Example: Put an object (will be encrypted if encryption is enabled)
	// putResult, err := backend.PutObject(ctx, "mybucket", "mykey",
	//     strings.NewReader("Hello, encrypted world!"), 23, storage.PutOptions{})

	// The rest of your S3 proxy server implementation...
}
