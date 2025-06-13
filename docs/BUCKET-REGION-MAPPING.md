# Bucket Region Mapping Configuration

This document explains the new per-bucket region mapping feature in s3proxy-go.

## Problem Solved

When using s3proxy-go as a gateway, you may have S3 buckets in different AWS regions. Previously, all buckets had to be in the same region as configured globally. This caused issues when trying to proxy buckets from multiple regions.

## Solution

The new `bucket_configs` configuration allows you to specify per-bucket settings including:

- The real bucket name (mapping virtual to real)
- The AWS region for that specific bucket
- Optional prefix (subdirectory) within the bucket
- Optional per-bucket credentials
- Optional custom endpoints (for MinIO, etc.)

## Configuration Examples

### Basic Multi-Region Setup

```yaml
storage:
  provider: s3
  s3:
    # Default settings
    region: us-east-1
    access_key: ${AWS_ACCESS_KEY_ID}
    secret_key: ${AWS_SECRET_ACCESS_KEY}

    # Per-bucket configurations
    bucket_configs:
      # Virtual bucket "data" maps to real bucket in us-east-1
      data:
        real_name: production-data-bucket
        region: us-east-1

      # Virtual bucket "backups" maps to real bucket in us-west-2
      backups:
        real_name: production-backups-bucket
        region: us-west-2

      # Virtual bucket "eu-data" maps to real bucket in eu-west-1
      eu-data:
        real_name: production-eu-bucket
        region: eu-west-1
```

### Advanced Configuration

```yaml
storage:
  provider: s3
  s3:
    # Default configuration
    region: us-east-1
    use_path_style: false

    # Mix of simple and advanced mappings
    bucket_mapping:
      # Simple mapping (uses default region)
      temp: temp-bucket-2024

    bucket_configs:
      # Multi-region setup
      us-data:
        real_name: prod-us-data
        region: us-east-1

      eu-data:
        real_name: prod-eu-data
        region: eu-west-1

      # Bucket prefix mapping - map to subdirectory
      dev-env:
        real_name: shared-environments-bucket
        prefix: development/data
        region: us-east-1

      staging-env:
        real_name: shared-environments-bucket
        prefix: staging/data
        region: us-east-1

      # Different AWS account
      partner-data:
        real_name: partner-shared-bucket
        region: us-east-2
        access_key: ${PARTNER_ACCESS_KEY}
        secret_key: ${PARTNER_SECRET_KEY}

      # MinIO backend
      archive:
        real_name: archive-bucket
        region: us-east-1
        endpoint: https://minio.local:9000
```

### Bucket Prefix Mapping

You can map virtual buckets to subdirectories within real S3 buckets:

```yaml
bucket_configs:
  # Map team buckets to subdirectories
  team-frontend:
    real_name: company-shared-bucket
    prefix: teams/frontend
    region: us-east-1

  team-backend:
    real_name: company-shared-bucket
    prefix: teams/backend
    region: us-east-1

  # Map environment data to subdirectories
  dev-data:
    real_name: company-data-bucket
    prefix: environments/development
    region: us-east-1

  prod-data:
    real_name: company-data-bucket
    prefix: environments/production
    region: us-east-1
```

With prefix mapping:

- Client writes to `team-frontend/app.js` → stored as `teams/frontend/app.js` in `company-shared-bucket`
- Client lists `team-frontend/` → only sees objects under the `teams/frontend/` prefix
- Provides isolation between virtual buckets sharing the same real bucket

## Benefits

1. **Correct Region Access**: Each bucket is accessed using a client configured for its specific region, avoiding cross-region access issues and improving performance.

2. **Cost Optimization**: Accessing buckets in their native region avoids cross-region data transfer charges.

3. **Compliance**: Keep data in specific regions for compliance requirements while providing a unified access interface.

4. **Multi-Account Support**: Different buckets can use different AWS credentials.

5. **Hybrid Storage**: Mix AWS S3 buckets with S3-compatible storage (MinIO, Ceph, etc.).

## How It Works

1. When a request comes in for a virtual bucket, s3proxy-go checks if there's a specific configuration for that bucket.

2. If found, it creates or reuses an S3 client configured for that bucket's region and settings.

3. The request is then forwarded using the appropriate client, ensuring it goes to the correct region.

4. Clients are cached per region/endpoint combination for efficiency.

## Migration from Old Configuration

The old `bucket_mapping` configuration still works:

```yaml
# Old style - still supported
bucket_mapping:
  virtual-name: real-bucket-name
```

You can use both styles together. The new `bucket_configs` takes precedence if both are defined for the same virtual bucket.

## Example Use Case

Imagine you have:

- User data in `us-east-1` for US customers
- User data in `eu-west-1` for EU customers
- Media files in `us-west-2` for global CDN distribution
- Archive data in on-premise MinIO

With the new configuration, you can expose all of these through a single s3proxy-go instance:

```yaml
bucket_configs:
  us-users:
    real_name: prod-us-user-data
    region: us-east-1

  eu-users:
    real_name: prod-eu-user-data
    region: eu-west-1

  media:
    real_name: global-media-bucket
    region: us-west-2

  archive:
    real_name: archive-bucket
    region: us-east-1
    endpoint: https://minio.datacenter.local:9000
```

Clients can now access all buckets through your s3proxy-go endpoint without worrying about regions or different storage backends.
