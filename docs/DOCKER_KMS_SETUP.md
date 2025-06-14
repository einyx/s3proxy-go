# Docker KMS Setup Guide

This guide explains how to run S3Proxy with KMS encryption using Docker Compose for local development.

## Prerequisites

- Docker and Docker Compose installed
- AWS CLI configured with a `dev` profile
- Valid AWS credentials with KMS permissions

## Quick Start

### Automated Setup

Run the automated setup script:

```bash
./scripts/setup-docker-kms.sh
```

This script will:
1. Check prerequisites
2. Verify AWS profile 'dev'
3. Create/verify KMS key with alias `s3proxy-dev`
4. Start Docker services
5. Test the setup

### Manual Setup

If you prefer manual setup:

1. **Configure AWS Profile**
   ```bash
   aws configure --profile dev
   ```

2. **Create KMS Key (if needed)**
   ```bash
   # Create key
   KEY_ID=$(aws kms create-key \
     --description "S3Proxy Development Key" \
     --key-usage ENCRYPT_DECRYPT \
     --profile dev \
     --query KeyMetadata.KeyId \
     --output text)

   # Create alias
   aws kms create-alias \
     --alias-name alias/s3proxy-dev \
     --target-key-id $KEY_ID \
     --profile dev
   ```

3. **Set Environment Variables**
   ```bash
   export KMS_KEY_ID="alias/s3proxy-dev"
   export AWS_REGION="us-east-1"  # or your preferred region
   ```

4. **Start Services**
   ```bash
   docker-compose -f docker-compose.kms.yml up --build -d
   ```

## Services

### S3Proxy with KMS
- **Port**: 8080
- **Credentials**: admin / secret
- **Features**: KMS encryption, multi-provider support
- **Health**: http://localhost:8080/health

### MinIO (Storage Backend)
- **Port**: 9000
- **Console**: 9001
- **Credentials**: minioadmin / minioadmin

### LocalStack (Optional)
For testing without real AWS KMS:

```bash
docker-compose -f docker-compose.kms.yml --profile localstack up -d
```

## Testing the Setup

### Basic Operations

1. **Create an encrypted bucket**
   ```bash
   curl -X PUT http://admin:secret@localhost:8080/encrypted-bucket
   ```

2. **Upload a file (automatically encrypted)**
   ```bash
   curl -X PUT http://admin:secret@localhost:8080/encrypted-bucket/test.txt \
     -d "Hello, encrypted world!"
   ```

3. **Download the file (automatically decrypted)**
   ```bash
   curl http://admin:secret@localhost:8080/encrypted-bucket/test.txt
   ```

### Advanced Testing

1. **Test with sensitive data bucket**
   ```bash
   # This bucket uses mandatory encryption per policy
   curl -X PUT http://admin:secret@localhost:8080/sensitive-data
   curl -X PUT http://admin:secret@localhost:8080/sensitive-data/secret.txt \
     -d "Top secret information"
   ```

2. **Test multi-provider fallback**
   ```bash
   # This bucket uses local provider per policy
   curl -X PUT http://admin:secret@localhost:8080/test-bucket
   curl -X PUT http://admin:secret@localhost:8080/test-bucket/local.txt \
     -d "Locally encrypted data"
   ```

## Configuration

### Environment Variables

The Docker setup supports these environment variables:

#### AWS/KMS Configuration
- `AWS_PROFILE`: AWS profile to use (default: dev)
- `KMS_KEY_ID`: KMS key ID or alias
- `AWS_REGION`: AWS region

#### Storage Configuration
- `S3_ENDPOINT`: MinIO endpoint (default: http://minio:9000)
- `S3_ACCESS_KEY`: MinIO access key
- `S3_SECRET_KEY`: MinIO secret key

#### Authentication
- `AUTH_TYPE`: Authentication type (default: basic)
- `AUTH_IDENTITY`: Username (default: admin)
- `AUTH_CREDENTIAL`: Password (default: secret)

### Configuration File

The setup uses `/examples/config-kms-docker.yaml` which includes:

- AWS KMS provider configuration
- Multi-provider setup with local fallback
- Bucket-specific encryption policies
- Development-friendly logging

## Troubleshooting

### Common Issues

1. **AWS Credentials Not Working**
   ```bash
   # Test your AWS profile
   aws sts get-caller-identity --profile dev

   # Reconfigure if needed
   aws configure --profile dev
   ```

2. **KMS Permission Denied**

   Ensure your AWS user/role has these permissions:
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

3. **Service Not Starting**
   ```bash
   # Check logs
   docker-compose -f docker-compose.kms.yml logs s3proxy

   # Check container status
   docker-compose -f docker-compose.kms.yml ps
   ```

4. **Connection Refused**
   ```bash
   # Wait for services to fully start
   sleep 10

   # Check health endpoints
   curl http://localhost:8080/health
   curl http://localhost:9000/minio/health/live
   ```

### Debugging

1. **Enable Debug Logging**
   ```bash
   # Set in docker-compose.kms.yml
   environment:
     - LOG_LEVEL=debug
   ```

2. **Access Container Shell**
   ```bash
   docker exec -it s3proxy-kms sh
   ```

3. **View AWS Configuration in Container**
   ```bash
   docker exec s3proxy-kms cat /root/.aws/config
   docker exec s3proxy-kms cat /root/.aws/credentials
   ```

## Cleanup

### Stop Services
```bash
docker-compose -f docker-compose.kms.yml down
```

### Remove Volumes
```bash
docker-compose -f docker-compose.kms.yml down -v
```

### Clean Images
```bash
docker-compose -f docker-compose.kms.yml down --rmi all
```

### Remove KMS Resources (Optional)
```bash
# Delete alias
aws kms delete-alias --alias-name alias/s3proxy-dev --profile dev

# Schedule key deletion (7-day waiting period)
KEY_ID=$(aws kms describe-key --key-id alias/s3proxy-dev --profile dev --query KeyMetadata.KeyId --output text)
aws kms schedule-key-deletion --key-id $KEY_ID --pending-window-in-days 7 --profile dev
```

## Security Considerations

### Development vs Production

This setup is designed for **development only**. For production:

1. **Use IAM Roles**: Instead of AWS profiles, use IAM roles
2. **Separate Keys**: Use different KMS keys per environment
3. **Network Security**: Use proper network isolation
4. **Secrets Management**: Use external secret management
5. **TLS**: Enable TLS for all communications

### Local Development Security

- AWS credentials are mounted read-only
- Non-root user in container
- Health checks for service monitoring
- Resource limits can be added if needed

## Next Steps

1. **Integrate with CI/CD**: Use this setup for automated testing
2. **Add Monitoring**: Integrate with Prometheus/Grafana
3. **Multi-Environment**: Create separate configs for staging/prod
4. **Backup Testing**: Test encryption/decryption with real data
5. **Performance Testing**: Use the setup for load testing

## References

- [Multi-Provider Encryption Documentation](./KMS_ENCRYPTION.md)
- [Configuration Examples](../examples/)
- [Docker Compose Reference](https://docs.docker.com/compose/)
- [AWS KMS Documentation](https://docs.aws.amazon.com/kms/)
