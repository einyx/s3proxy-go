package config

import (
	"fmt"
	"time"

	"github.com/kelseyhightower/envconfig"
	"github.com/spf13/viper"
)

type Config struct {
	Server  ServerConfig  `mapstructure:"server"`
	S3      S3Config      `mapstructure:"s3"`
	Storage StorageConfig `mapstructure:"storage"`
	Auth    AuthConfig    `mapstructure:"auth"`
}

type ServerConfig struct {
	Listen       string        `mapstructure:"listen" envconfig:"SERVER_LISTEN" default:":8080"`
	ReadTimeout  time.Duration `mapstructure:"read_timeout" envconfig:"SERVER_READ_TIMEOUT" default:"60s"`
	WriteTimeout time.Duration `mapstructure:"write_timeout" envconfig:"SERVER_WRITE_TIMEOUT" default:"60s"`
	IdleTimeout  time.Duration `mapstructure:"idle_timeout" envconfig:"SERVER_IDLE_TIMEOUT" default:"120s"`
	MaxBodySize  int64         `mapstructure:"max_body_size" envconfig:"SERVER_MAX_BODY_SIZE" default:"5368709120"` // 5GB
}

type S3Config struct {
	Region               string `mapstructure:"region" envconfig:"S3_REGION" default:"us-east-1"`
	VirtualHost          bool   `mapstructure:"virtual_host" envconfig:"S3_VIRTUAL_HOST" default:"false"`
	PathStyle            bool   `mapstructure:"path_style" envconfig:"S3_PATH_STYLE" default:"true"`
	ServicePath          string `mapstructure:"service_path" envconfig:"S3_SERVICE_PATH" default:""`
	IgnoreUnknownHeaders bool   `mapstructure:"ignore_unknown_headers" envconfig:"S3_IGNORE_UNKNOWN_HEADERS" default:"true"`
}

type StorageConfig struct {
	Provider   string              `mapstructure:"provider" envconfig:"STORAGE_PROVIDER" required:"true"`
	Azure      *AzureStorageConfig `mapstructure:"azure"`
	S3         *S3StorageConfig    `mapstructure:"s3"`
	FileSystem *FileSystemConfig   `mapstructure:"filesystem"`
}

type AzureStorageConfig struct {
	AccountName   string `mapstructure:"account_name" envconfig:"AZURE_ACCOUNT_NAME"`
	AccountKey    string `mapstructure:"account_key" envconfig:"AZURE_ACCOUNT_KEY"`
	ContainerName string `mapstructure:"container_name" envconfig:"AZURE_CONTAINER_NAME"`
	Endpoint      string `mapstructure:"endpoint" envconfig:"AZURE_ENDPOINT"`
	UseSAS        bool   `mapstructure:"use_sas" envconfig:"AZURE_USE_SAS" default:"false"`
	SASToken      string `mapstructure:"sas_token" envconfig:"AZURE_SAS_TOKEN"`
}

type S3StorageConfig struct {
	Endpoint     string `mapstructure:"endpoint" envconfig:"S3_ENDPOINT"`
	Region       string `mapstructure:"region" envconfig:"S3_REGION" default:"us-east-1"`
	AccessKey    string `mapstructure:"access_key" envconfig:"S3_ACCESS_KEY"`
	SecretKey    string `mapstructure:"secret_key" envconfig:"S3_SECRET_KEY"`
	Profile      string `mapstructure:"profile" envconfig:"AWS_PROFILE"`
	UsePathStyle bool   `mapstructure:"use_path_style" envconfig:"S3_USE_PATH_STYLE" default:"true"`
	DisableSSL   bool   `mapstructure:"disable_ssl" envconfig:"S3_DISABLE_SSL" default:"false"`
}

type FileSystemConfig struct {
	BaseDir string `mapstructure:"base_dir" envconfig:"FS_BASE_DIR" default:"/data"`
}

type AuthConfig struct {
	Type       string `mapstructure:"type" envconfig:"AUTH_TYPE" default:"none"` // none, basic, awsv2, awsv4
	Identity   string `mapstructure:"identity" envconfig:"AUTH_IDENTITY"`
	Credential string `mapstructure:"credential" envconfig:"AUTH_CREDENTIAL"`
	// AWS-style environment variables (take precedence if set)
	AWSAccessKeyID     string `mapstructure:"-" envconfig:"AWS_ACCESS_KEY_ID"`
	AWSSecretAccessKey string `mapstructure:"-" envconfig:"AWS_SECRET_ACCESS_KEY"`
}

func Load(configFile string) (*Config, error) {
	cfg := &Config{}

	if configFile != "" {
		viper.SetConfigFile(configFile)
		if err := viper.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
		if err := viper.Unmarshal(cfg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal config: %w", err)
		}
	}

	if err := envconfig.Process("", cfg); err != nil {
		return nil, fmt.Errorf("failed to process env vars: %w", err)
	}

	// AWS environment variables take precedence
	if cfg.Auth.AWSAccessKeyID != "" {
		cfg.Auth.Identity = cfg.Auth.AWSAccessKeyID
	}
	if cfg.Auth.AWSSecretAccessKey != "" {
		cfg.Auth.Credential = cfg.Auth.AWSSecretAccessKey
	}

	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return cfg, nil
}

func validate(cfg *Config) error {
	if cfg.Storage.Provider == "" {
		return fmt.Errorf("storage provider is required")
	}

	switch cfg.Storage.Provider {
	case "azure", "azureblob":
		if cfg.Storage.Azure == nil {
			return fmt.Errorf("azure storage config is required")
		}
		if cfg.Storage.Azure.AccountName == "" || cfg.Storage.Azure.AccountKey == "" {
			if !cfg.Storage.Azure.UseSAS || cfg.Storage.Azure.SASToken == "" {
				return fmt.Errorf("azure account name and key or SAS token are required")
			}
		}
	case "s3":
		if cfg.Storage.S3 == nil {
			return fmt.Errorf("s3 storage config is required")
		}
		// When using AWS profile, credentials are not required
		if cfg.Storage.S3.Profile == "" && cfg.Storage.S3.AccessKey == "" && cfg.Storage.S3.SecretKey == "" {
			// Check if AWS environment variables are set
			if cfg.Auth.AWSAccessKeyID == "" || cfg.Auth.AWSSecretAccessKey == "" {
				return fmt.Errorf("s3 credentials are required: specify profile, access/secret keys, or AWS environment variables")
			}
		}
	case "filesystem":
		if cfg.Storage.FileSystem == nil {
			return fmt.Errorf("filesystem storage config is required")
		}
	default:
		return fmt.Errorf("unsupported storage provider: %s", cfg.Storage.Provider)
	}

	return nil
}
