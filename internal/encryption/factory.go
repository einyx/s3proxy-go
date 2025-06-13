package encryption

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"

	"github.com/einyx/s3proxy-go/internal/config"
	"github.com/einyx/s3proxy-go/internal/encryption/keys"
	"github.com/einyx/s3proxy-go/internal/encryption/stream"
	"github.com/einyx/s3proxy-go/internal/encryption/types"
)

// NewFromConfig creates an encryption manager from configuration
func NewFromConfig(ctx context.Context, cfg *config.EncryptionConfig) (*Manager, error) {
	if !cfg.Enabled {
		return &Manager{enabled: false}, nil
	}

	// Create key provider
	keyProvider, err := createKeyProvider(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create key provider: %w", err)
	}

	// Create encryptor
	encryptor, err := createEncryptor(cfg, keyProvider)
	if err != nil {
		return nil, fmt.Errorf("failed to create encryptor: %w", err)
	}

	return NewManager(keyProvider, encryptor, true), nil
}

func createKeyProvider(ctx context.Context, cfg *config.EncryptionConfig) (types.KeyProvider, error) {
	switch cfg.KeyProvider {
	case "local":
		if cfg.Local == nil || cfg.Local.MasterKey == "" {
			return nil, fmt.Errorf("local key provider requires master key")
		}

		// Decode base64 master key
		masterKey, err := base64.StdEncoding.DecodeString(cfg.Local.MasterKey)
		if err != nil {
			return nil, fmt.Errorf("failed to decode master key: %w", err)
		}

		return keys.NewLocalKeyProvider(masterKey)

	case "kms", "aws-kms":
		if cfg.KMS == nil || cfg.KMS.KeyID == "" {
			return nil, fmt.Errorf("KMS key provider requires key ID")
		}

		// Load AWS configuration
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to load AWS config: %w", err)
		}

		// Override region if specified
		if cfg.KMS.Region != "" {
			awsCfg.Region = cfg.KMS.Region
		}

		provider, err := keys.NewKMSKeyProvider(awsCfg, cfg.KMS.KeyID)
		if err != nil {
			return nil, err
		}

		// Set cache TTL if specified
		if cfg.KMS.CacheTTL > 0 {
			provider.SetCacheExpiry(time.Duration(cfg.KMS.CacheTTL) * time.Second)
		}

		return provider, nil

	default:
		return nil, fmt.Errorf("unsupported key provider: %s", cfg.KeyProvider)
	}
}

func createEncryptor(cfg *config.EncryptionConfig, keyProvider types.KeyProvider) (types.Encryptor, error) {
	switch types.Algorithm(cfg.Algorithm) {
	case types.AlgorithmAES256GCM:
		// Use simple implementation for now
		return stream.NewSimpleAESGCMEncryptor(keyProvider), nil

	case types.AlgorithmChaCha20Poly1305:
		// TODO: Implement ChaCha20-Poly1305 encryptor
		return nil, fmt.Errorf("ChaCha20-Poly1305 not yet implemented")

	default:
		return nil, fmt.Errorf("unsupported encryption algorithm: %s", cfg.Algorithm)
	}
}
