# S3Proxy Docker Development Setup

This guide explains how to run S3Proxy with AWS KMS encryption support using Docker Compose.

## Prerequisites

- Docker and Docker Compose installed
- AWS CLI configured with a profile named 'dev'
- AWS KMS key created or alias `alias/s3proxy-dev` available

## Important: Remote Docker Context

**Note**: If you're using a remote Docker context (e.g., Docker running on a remote host via SSH), you'll need to adjust the endpoints accordingly. Check your Docker context with:

```bash
docker context ls
```

If your context is remote (e.g., `ssh://user@remote-host`), replace `localhost` with the remote host IP in all commands.

## Quick Start

### 1. Setup AWS KMS and Start Services

```bash
# This script will:
# - Check AWS profile 'dev'
# - Create KMS key if needed
# - Start Docker services
./scripts/setup-docker-kms.sh
```

### 2. Test the Setup

```bash
# For local Docker:
./scripts/test-docker-setup.sh

# For remote Docker (e.g., frank context):
export DOCKER_REMOTE_HOST=192.168.170.243  # Replace with your Docker host IP
./scripts/test-docker-setup.sh
```

### 3. Run Stress Tests

```bash
# First, create the test buckets
export DOCKER_REMOTE_HOST=192.168.170.243  # If using remote Docker
./scripts/setup-stress-test.sh

# Then run the stress tests
export PROXY_ENDPOINT=http://192.168.170.243:8082  # Replace with your Docker host
./tests/stress/run_stress_test.sh
```

## Service Endpoints

- **S3Proxy**: http://[DOCKER_HOST]:8082
  - Credentials: `admin` / `secret`
- **MinIO**: http://[DOCKER_HOST]:9000
  - Console: http://[DOCKER_HOST]:9001
  - Credentials: `minioadmin` / `minioadmin`

## Configuration

The Docker setup uses environment variables for configuration. Key settings:

- `ENCRYPTION_ENABLED=true` - Enables KMS encryption
- `ENCRYPTION_KEY_PROVIDER=aws-kms` - Uses AWS KMS
- `KMS_DEFAULT_KEY_ID=alias/s3proxy-dev` - Default KMS key
- `AWS_PROFILE=dev` - AWS profile to use

Configuration can be customized in:
- `docker-compose.kms.yml` - Docker Compose configuration
- `examples/config-kms-docker.yaml` - S3Proxy configuration file

## Testing Encryption

### Basic Test
```bash
# Create an encrypted bucket
curl -X PUT http://admin:secret@[DOCKER_HOST]:8082/encrypted-bucket

# Upload encrypted data
curl -X PUT http://admin:secret@[DOCKER_HOST]:8082/encrypted-bucket/secret.txt \
  -d "This data will be encrypted with KMS"

# Download and verify
curl http://admin:secret@[DOCKER_HOST]:8082/encrypted-bucket/secret.txt
```

### Verify KMS Usage
Check CloudWatch metrics in AWS Console to see KMS API calls for encryption/decryption operations.

## LocalStack Option

For development without real AWS resources:

```bash
# Start with LocalStack (when prompted during setup)
docker-compose -f docker-compose.kms.yml --profile localstack up -d
```

LocalStack simulates AWS services locally, including KMS.

## Monitoring and Logs

### View Logs
```bash
# All services
docker-compose -f docker-compose.kms.yml logs -f

# S3Proxy only
docker-compose -f docker-compose.kms.yml logs -f s3proxy
```

### Check Service Status
```bash
docker-compose -f docker-compose.kms.yml ps
```

### Health Check
```bash
curl http://[DOCKER_HOST]:8082/health
```

## Troubleshooting

### Port Already in Use
If port 8082 is already in use:
1. Check what's using it: `lsof -i :8082`
2. Change the port in `docker-compose.kms.yml`
3. Update scripts to use the new port

### Access Denied Errors
- Ensure AWS credentials are properly configured: `aws configure list --profile dev`
- Verify KMS key permissions: `aws kms describe-key --key-id alias/s3proxy-dev --profile dev`
- Check S3Proxy logs for detailed error messages

### Remote Docker Issues
- Ensure Docker daemon is accessible: `docker info`
- Check network connectivity to Docker host
- Verify port forwarding if using SSH tunnels

### Connection Refused
- For remote Docker: Use the Docker host IP, not localhost
- Ensure firewall allows connections to ports 8082, 9000, 9001
- Check if services are running: `docker-compose -f docker-compose.kms.yml ps`

## Cleanup

```bash
# Stop services
docker-compose -f docker-compose.kms.yml down

# Remove volumes (careful - deletes all data)
docker-compose -f docker-compose.kms.yml down -v

# Clean up Docker system
docker system prune -f
```

## Advanced Configuration

### Multi-Provider Setup
Edit `examples/config-kms-docker.yaml` to configure multiple encryption providers:
- AWS KMS
- Azure Key Vault
- Custom encryption
- Local keys

### Performance Tuning
Adjust in `docker-compose.kms.yml`:
- `KMS_DATA_KEY_CACHE_TTL` - Cache duration for data keys
- `RATE_LIMIT_RPS` - Requests per second limit
- Container resource limits

## Development Workflow

1. Make code changes
2. Rebuild and restart: `docker-compose -f docker-compose.kms.yml up --build -d`
3. Test changes: `./scripts/test-docker-setup.sh`
4. Check logs: `docker-compose -f docker-compose.kms.yml logs -f s3proxy`

## Security Notes

- The setup uses AWS profile 'dev' - ensure this has appropriate KMS permissions
- Default credentials (admin/secret) are for development only
- KMS keys should have proper access policies in production
- Enable audit logging for production use
