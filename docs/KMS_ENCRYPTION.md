# Multi-Provider Encryption for S3 Proxy

This document describes the encryption implementation in s3proxy-go, supporting multiple key management providers including AWS KMS, Azure Key Vault, and custom encryption solutions.

## Overview

The S3 proxy supports multiple encryption providers, allowing you to:
- **AWS KMS**: Use AWS-managed or customer-managed KMS keys
- **Azure Key Vault**: Integrate with Azure's key management service
- **Custom Encryption**: Use your own encryption keys and algorithms
- **Local Keys**: Simple local key management for development/testing
- Configure per-bucket encryption policies with different providers
- Maintain compliance with data protection regulations
- Implement envelope encryption for optimal performance

## Architecture

### Components

1. **KMS Manager** (`internal/kms/manager.go`)
   - Handles KMS client initialization and configuration
   - Validates KMS keys and permissions
   - Manages data key caching for performance
   - Provides encryption headers for S3 operations

2. **Envelope Encryptor** (`internal/kms/envelope.go`)
   - Implements envelope encryption using KMS data keys
   - Supports streaming encryption for large objects
   - Ensures secure key handling and cleanup

3. **S3 Integration** (`pkg/s3/handler_kms.go`)
   - Extends base S3 handler with KMS support
   - Applies encryption headers to PUT operations
   - Returns encryption metadata in GET responses

## Configuration

### Multi-Provider Support

S3Proxy supports multiple key management providers that can be configured globally or per-bucket:

#### AWS KMS Provider
```yaml
encryption:
  key_provider: aws-kms
  kms:
    enabled: true
    default_key_id: "arn:aws:kms:us-east-1:123456789012:key/12345678"
    region: "us-east-1"
    key_spec: "AES_256"
    encryption_context:
      app: "s3proxy"
```

#### Azure Key Vault Provider
```yaml
encryption:
  key_provider: azure-keyvault
  azure_keyvault:
    vault_url: "https://myvault.vault.azure.net"
    # Use managed identity or specify credentials:
    client_id: "your-client-id"
    client_secret: "your-client-secret"
    tenant_id: "your-tenant-id"
    key_size: 256
```

#### Custom Key Provider
```yaml
encryption:
  key_provider: custom
  custom:
    # Base64 encoded master key (32 bytes for AES-256)
    master_key: "YmFzZTY0ZW5jb2RlZDMyYnl0ZW1hc3RlcmtleWhlcmU="
    # Optional: key derivation salt
    key_derivation_salt: "YmFzZTY0ZW5jb2RlZHNhbHQ="
```

#### Named Providers for Advanced Scenarios
```yaml
encryption:
  # Default provider
  key_provider: prod-kms

  # Define multiple named providers
  key_providers:
    prod-kms:
      type: aws-kms
      config:
        default_key_id: "alias/production-key"
        region: "us-east-1"

    dev-azure:
      type: azure-keyvault
      config:
        vault_url: "https://dev-vault.vault.azure.net"

    test-local:
      type: local
      config:
        master_key: "dGVzdGtleQ=="

  # Apply different providers to different buckets
  policies:
    - bucket_pattern: "prod-*"
      key_provider: prod-kms
      mandatory: true

    - bucket_pattern: "dev-*"
      key_provider: dev-azure
      mandatory: false
```

### Global KMS Configuration (Legacy)

```yaml
encryption:
  kms:
    enabled: true
    default_key_id: "arn:aws:kms:us-east-1:123456789012:key/12345678-1234-1234-1234-123456789012"
    key_spec: "AES_256"  # or RSA_2048, RSA_3072, RSA_4096
    region: "us-east-1"
    encryption_context:
      application: "s3proxy"
      environment: "production"
    data_key_cache_ttl: "5m"
    validate_keys: true
    enable_key_rotation: false
```

### Per-Bucket Configuration

```yaml
storage:
  s3:
    bucket_configs:
      sensitive-data:
        real_name: "prod-sensitive-data"
        region: "us-east-1"
        kms_key_id: "alias/sensitive-data-key"
        kms_encryption_context:
          bucket: "sensitive-data"
          classification: "confidential"
```

### Environment Variables

- `KMS_ENABLED`: Enable/disable KMS encryption
- `KMS_DEFAULT_KEY_ID`: Default KMS key ID or alias
- `KMS_KEY_SPEC`: Key specification (default: AES_256)
- `KMS_REGION`: AWS region for KMS
- `KMS_DATA_KEY_CACHE_TTL`: Cache TTL for data keys
- `KMS_VALIDATE_KEYS`: Validate keys on startup
- `KMS_ENABLE_KEY_ROTATION`: Enable automatic key rotation

## Security Best Practices

### 1. Key Management

- **Use CMKs**: Always use Customer Master Keys (CMKs) for better control
- **Key Aliases**: Use key aliases instead of key IDs for easier rotation
- **Separate Keys**: Use different keys for different data classifications
- **Key Policies**: Implement least-privilege key policies

Example key policy:
```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "Enable S3 Proxy Encryption",
      "Effect": "Allow",
      "Principal": {
        "AWS": "arn:aws:iam::123456789012:role/s3proxy-role"
      },
      "Action": [
        "kms:Decrypt",
        "kms:Encrypt",
        "kms:GenerateDataKey",
        "kms:DescribeKey"
      ],
      "Resource": "*",
      "Condition": {
        "StringEquals": {
          "kms:EncryptionContext:application": "s3proxy"
        }
      }
    }
  ]
}
```

### 2. Encryption Context

Always use encryption context for additional authenticated data:

```yaml
encryption_context:
  application: "s3proxy"
  environment: "production"
  data_classification: "sensitive"
```

### 3. IAM Permissions

Required IAM permissions for the S3 proxy:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "kms:Decrypt",
        "kms:Encrypt",
        "kms:GenerateDataKey",
        "kms:DescribeKey",
        "kms:GetKeyRotationStatus"
      ],
      "Resource": [
        "arn:aws:kms:*:*:key/*"
      ]
    },
    {
      "Effect": "Allow",
      "Action": [
        "s3:PutObject",
        "s3:GetObject",
        "s3:DeleteObject",
        "s3:ListBucket"
      ],
      "Resource": [
        "arn:aws:s3:::*"
      ]
    }
  ]
}
```

### 4. Key Rotation

Enable automatic key rotation for enhanced security:

```yaml
encryption:
  kms:
    enable_key_rotation: true
```

Monitor key rotation status:
```bash
aws kms get-key-rotation-status --key-id alias/my-key
```

### 5. Audit and Compliance

- **CloudTrail**: Enable CloudTrail logging for all KMS operations
- **Monitoring**: Set up CloudWatch alarms for unauthorized KMS access
- **Compliance**: Regularly audit encryption status of objects

Example CloudWatch alarm for KMS failures:
```yaml
AlarmName: KMS-Decryption-Failures
MetricName: DecryptionFailures
Namespace: AWS/KMS
Statistic: Sum
Period: 300
EvaluationPeriods: 1
Threshold: 1
ComparisonOperator: GreaterThanThreshold
```

## Usage Examples

### 1. Upload with Default KMS Encryption

```bash
# Upload will use default KMS key from configuration
aws s3 cp file.txt s3://mybucket/file.txt --endpoint-url http://localhost:8080
```

### 2. Upload with Specific KMS Key

```bash
# Specify KMS key in request
aws s3 cp file.txt s3://mybucket/file.txt \
  --endpoint-url http://localhost:8080 \
  --sse aws:kms \
  --sse-kms-key-id arn:aws:kms:us-east-1:123456789012:key/custom-key
```

### 3. Verify Encryption Status

```bash
# Check object metadata
aws s3api head-object --bucket mybucket --key file.txt \
  --endpoint-url http://localhost:8080

# Response includes:
# "ServerSideEncryption": "aws:kms",
# "SSEKMSKeyId": "arn:aws:kms:us-east-1:123456789012:key/12345678-1234-1234-1234-123456789012"
```

## Performance Considerations

### Data Key Caching

The proxy implements data key caching to reduce KMS API calls:

```yaml
data_key_cache_ttl: "5m"  # Cache data keys for 5 minutes
```

Benefits:
- Reduces latency for encryption operations
- Lowers KMS API costs
- Improves throughput for high-volume operations

### Envelope Encryption

The proxy uses envelope encryption for optimal performance:
1. Generate a data key from KMS (cached)
2. Encrypt object data with the data key (fast)
3. Store encrypted data key with object metadata
4. Decrypt data key when retrieving objects

## Troubleshooting

### Common Issues

1. **Permission Denied**
   ```
   Error: failed to generate data key: AccessDeniedException
   ```
   Solution: Verify IAM permissions and key policies

2. **Key Not Found**
   ```
   Error: failed to describe key: NotFoundException
   ```
   Solution: Check key ID/alias and region configuration

3. **Invalid Encryption Context**
   ```
   Error: encryption context mismatch
   ```
   Solution: Ensure consistent encryption context between encrypt/decrypt

### Debug Logging

Enable debug logging for KMS operations:
```yaml
logging:
  level: debug
  kms_operations: true
```

## Security Checklist

- [ ] Use customer-managed KMS keys
- [ ] Implement key rotation
- [ ] Configure encryption context
- [ ] Apply least-privilege IAM policies
- [ ] Enable CloudTrail logging
- [ ] Monitor KMS usage with CloudWatch
- [ ] Regularly audit encrypted objects
- [ ] Test key deletion procedures
- [ ] Document recovery procedures
- [ ] Implement break-glass access

## References

- [AWS KMS Best Practices](https://docs.aws.amazon.com/kms/latest/developerguide/best-practices.html)
- [S3 Encryption](https://docs.aws.amazon.com/AmazonS3/latest/userguide/UsingEncryption.html)
- [KMS Encryption Context](https://docs.aws.amazon.com/kms/latest/developerguide/concepts.html#encrypt_context)
