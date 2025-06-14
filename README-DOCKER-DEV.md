# S3Proxy Docker Development Setup

This guide shows how to run S3Proxy with KMS encryption using Docker Compose and AWS profile 'dev' for local development.

## Quick Start

### 1. Prerequisites

Ensure you have:
- Docker and Docker Compose installed
- AWS CLI configured with a `dev` profile
- Valid AWS credentials with KMS permissions

### 2. Automated Setup

Run the setup script:

```bash
./scripts/setup-docker-kms.sh
```

This will:
- Check prerequisites
- Configure AWS profile 'dev' if needed
- Create/verify KMS key `alias/s3proxy-dev`
- Start all services
- Test the setup

### 3. Manual Setup

If you prefer manual configuration:

```bash
# Configure AWS profile 'dev'
aws configure --profile dev

# Set environment variables
export KMS_KEY_ID="alias/s3proxy-dev"
export AWS_REGION="us-east-1"

# Start services
docker-compose -f docker-compose.kms.yml up --build -d
```

## Services

| Service | Port | Credentials | Purpose |
|---------|------|-------------|---------|
| S3Proxy | 8080 | admin/secret | KMS-enabled S3 proxy |
| MinIO | 9000 | minioadmin/minioadmin | Storage backend |
| MinIO Console | 9001 | minioadmin/minioadmin | Management UI |

## Testing

### Basic Test

```bash
# Run automated test
./scripts/test-docker-setup.sh
```

### Manual Testing

```bash
# Create encrypted bucket
curl -X PUT http://admin:secret@localhost:8080/encrypted-bucket

# Upload encrypted file
curl -X PUT http://admin:secret@localhost:8080/encrypted-bucket/secret.txt \
  -d "This will be encrypted with KMS!"

# Download and decrypt file
curl http://admin:secret@localhost:8080/encrypted-bucket/secret.txt
```

## Configuration Features

### Multi-Provider Support

The setup includes configurations for:

- **AWS KMS**: Using your `dev` profile credentials
- **Local Provider**: For fallback/testing
- **Custom Provider**: For your own encryption keys

### Bucket Policies

Different encryption policies apply to different bucket patterns:

- `sensitive-*`: Mandatory AWS KMS encryption
- `test-*`: Local provider (for testing)
- Other buckets: Default AWS KMS

### Example Buckets

```bash
# AWS KMS encrypted (mandatory)
curl -X PUT http://admin:secret@localhost:8080/sensitive-data

# Local provider
curl -X PUT http://admin:secret@localhost:8080/test-bucket

# Default AWS KMS
curl -X PUT http://admin:secret@localhost:8080/my-bucket
```

## Monitoring

### Logs

```bash
# All services
docker-compose -f docker-compose.kms.yml logs -f

# S3Proxy only
docker-compose -f docker-compose.kms.yml logs -f s3proxy

# MinIO only
docker-compose -f docker-compose.kms.yml logs -f minio
```

### Health Checks

```bash
# S3Proxy health
curl http://localhost:8080/health

# MinIO health
curl http://localhost:9000/minio/health/live
```

## AWS Configuration

### Required Permissions

Your AWS `dev` profile needs these KMS permissions:

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
        "kms:DescribeKey"
      ],
      "Resource": "*"
    }
  ]
}
```

### KMS Key Policy

Your KMS key should allow your dev user/role:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "Enable S3Proxy Development",
      "Effect": "Allow",
      "Principal": {
        "AWS": "arn:aws:iam::YOUR-ACCOUNT:user/your-dev-user"
      },
      "Action": [
        "kms:Decrypt",
        "kms:Encrypt",
        "kms:GenerateDataKey",
        "kms:DescribeKey"
      ],
      "Resource": "*"
    }
  ]
}
```

## Troubleshooting

### Common Issues

1. **AWS Credentials**
   ```bash
   # Test your dev profile
   aws sts get-caller-identity --profile dev
   ```

2. **KMS Permissions**
   ```bash
   # Test KMS access
   aws kms describe-key --key-id alias/s3proxy-dev --profile dev
   ```

3. **Container Issues**
   ```bash
   # Check container status
   docker-compose -f docker-compose.kms.yml ps

   # Restart services
   docker-compose -f docker-compose.kms.yml restart
   ```

4. **Port Conflicts**
   ```bash
   # Check what's using ports
   lsof -i :8080
   lsof -i :9000
   lsof -i :9001
   ```

### Debug Mode

Enable debug logging:

```bash
# Edit docker-compose.kms.yml and change:
environment:
  - LOG_LEVEL=debug

# Restart services
docker-compose -f docker-compose.kms.yml restart s3proxy
```

## Development Workflow

### Making Changes

1. **Code Changes**
   ```bash
   # Rebuild and restart
   docker-compose -f docker-compose.kms.yml up --build -d s3proxy
   ```

2. **Configuration Changes**
   ```bash
   # Edit examples/config-kms-docker.yaml
   # Restart services
   docker-compose -f docker-compose.kms.yml restart s3proxy
   ```

3. **Testing Changes**
   ```bash
   # Run tests
   ./scripts/test-docker-setup.sh
   ```

### Environment Switching

```bash
# Switch to staging KMS key
export KMS_KEY_ID="alias/s3proxy-staging"
docker-compose -f docker-compose.kms.yml restart s3proxy

# Switch to production KMS key
export KMS_KEY_ID="alias/s3proxy-prod"
docker-compose -f docker-compose.kms.yml restart s3proxy
```

## Cleanup

### Stop Services

```bash
docker-compose -f docker-compose.kms.yml down
```

### Remove Data

```bash
# Remove all volumes and data
docker-compose -f docker-compose.kms.yml down -v
```

### Clean Images

```bash
# Remove built images
docker-compose -f docker-compose.kms.yml down --rmi all
```

## Production Considerations

This setup is for **development only**. For production:

1. **Use IAM Roles** instead of profiles
2. **Enable TLS/HTTPS** for all communications
3. **Use dedicated KMS keys** per environment
4. **Implement proper monitoring** and alerting
5. **Set up log aggregation**
6. **Use secrets management** for credentials
7. **Enable audit logging**

## Next Steps

- [Full Configuration Reference](./docs/KMS_ENCRYPTION.md)
- [Multi-Provider Setup](./examples/config-multi-provider.yaml)
- [Production Deployment Guide](./docs/PRODUCTION.md)
- [Security Best Practices](./docs/SECURITY.md)
