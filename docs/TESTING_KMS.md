# Testing S3 Proxy with KMS Encryption

This guide walks you through setting up and testing the S3 proxy with AWS KMS encryption.

## Prerequisites

1. **AWS CLI**: Install and configure AWS CLI with appropriate credentials
2. **AWS Account**: Access to create KMS keys and S3 buckets
3. **Go**: Go 1.21+ for running tests
4. **jq**: JSON processor for scripts

## Quick Setup

### 1. Create Test Infrastructure

Run the setup script to create KMS keys and S3 buckets:

```bash
./scripts/create-kms-test-key.sh
```

This creates:
- 3 KMS keys: `alias/s3proxy-test`, `alias/s3proxy-sensitive`, `alias/s3proxy-financial`
- 4 S3 buckets with different encryption configurations
- A test configuration file: `test-config-kms.yaml`

### 2. Start the S3 Proxy

```bash
go run cmd/s3proxy/main.go -c test-config-kms.yaml
```

The proxy will start on port 8080 with KMS encryption enabled.

### 3. Run Basic Tests

```bash
# Unit tests
go test ./internal/kms/...

# Integration tests (requires real AWS resources)
TEST_KMS_KEY_ID=alias/s3proxy-test go test -tags=integration ./internal/kms/...

# Stress tests
./tests/stress/run_stress_test.sh
```

## Test Scenarios

### 1. Basic KMS Operations

Test KMS key validation and data key generation:

```bash
# Set environment variables
export AWS_REGION=us-east-1
export TEST_KMS_KEY_ID=alias/s3proxy-test

# Run integration tests
go test -tags=integration -v ./internal/kms/... -run TestKMSIntegration
```

### 2. S3 Upload with KMS Encryption

Upload a file using the AWS CLI through the proxy:

```bash
# Create test file
echo "Hello, encrypted world!" > test-file.txt

# Upload to internal-data bucket (uses default KMS key)
aws s3 cp test-file.txt s3://internal-data/test-file.txt \
  --endpoint-url http://localhost:8080

# Upload to sensitive-data bucket (uses bucket-specific KMS key)
aws s3 cp test-file.txt s3://sensitive-data/sensitive-file.txt \
  --endpoint-url http://localhost:8080

# Verify encryption
aws s3api head-object \
  --bucket internal-data \
  --key test-file.txt \
  --endpoint-url http://localhost:8080
```

Expected response should include:
```json
{
  "ServerSideEncryption": "aws:kms",
  "SSEKMSKeyId": "arn:aws:kms:us-east-1:...:key/..."
}
```

### 3. Performance Testing

#### Quick Performance Test

```bash
# Create 100 small files
for i in {1..100}; do
  echo "Test data $i" | aws s3 cp - s3://internal-data/perf-test-$i.txt \
    --endpoint-url http://localhost:8080
done

# Time a batch download
time for i in {1..100}; do
  aws s3 cp s3://internal-data/perf-test-$i.txt /tmp/downloaded-$i.txt \
    --endpoint-url http://localhost:8080
done
```

#### Comprehensive Stress Test

```bash
# Run all stress test scenarios
./tests/stress/run_stress_test.sh

# Run custom stress test
export STRESS_CONCURRENCY=25
export STRESS_DURATION=1m
export OBJECT_SIZE_MIN=1024
export OBJECT_SIZE_MAX=102400
export READ_WRITE_RATIO=0.6
export RUN_STRESS_TESTS=true

go test -tags=stress -v ./tests/stress/... -timeout 10m
```

### 4. Monitoring and Metrics

#### CloudWatch Metrics

Monitor these AWS KMS metrics:
- `NumberOfRequestsSucceeded`
- `NumberOfRequestsFailed`
- `Decrypt` operation count
- `GenerateDataKey` operation count

#### Proxy Metrics

The proxy exposes Prometheus metrics at `http://localhost:9090/metrics`:

```bash
# Check key metrics
curl -s http://localhost:9090/metrics | grep -E "(s3proxy|kms)"
```

#### Application Logs

Enable debug logging to see KMS operations:

```yaml
logging:
  level: debug
```

### 5. Security Testing

#### Test Encryption Context

```bash
# Upload with custom encryption context
aws s3api put-object \
  --bucket sensitive-data \
  --key context-test.txt \
  --body test-file.txt \
  --server-side-encryption aws:kms \
  --ssekms-key-id alias/s3proxy-sensitive \
  --endpoint-url http://localhost:8080
```

#### Test Access Patterns

```bash
# Test different bucket configurations
buckets=("public-data" "internal-data" "sensitive-data" "financial-data")

for bucket in "${buckets[@]}"; do
  echo "Testing bucket: $bucket"

  # Upload test file
  echo "Test data for $bucket" | aws s3 cp - s3://$bucket/test.txt \
    --endpoint-url http://localhost:8080

  # Check encryption status
  aws s3api head-object \
    --bucket $bucket \
    --key test.txt \
    --endpoint-url http://localhost:8080 \
    --query 'ServerSideEncryption'
done
```

## Troubleshooting

### Common Issues

1. **Permission Denied**
   ```
   Error: AccessDenied: User is not authorized to perform: kms:GenerateDataKey
   ```

   **Solution**: Verify IAM permissions and KMS key policy
   ```bash
   aws iam get-user
   aws kms describe-key --key-id alias/s3proxy-test
   ```

2. **Key Not Found**
   ```
   Error: NotFoundException: Key 'alias/s3proxy-test' does not exist
   ```

   **Solution**: Ensure KMS key was created successfully
   ```bash
   aws kms list-aliases --query 'Aliases[?starts_with(AliasName, `alias/s3proxy`)]'
   ```

3. **Bucket Not Found**
   ```
   Error: NoSuchBucket: The specified bucket does not exist
   ```

   **Solution**: Verify bucket configuration in test config
   ```bash
   aws s3 ls | grep s3proxy-test
   ```

### Debug Commands

```bash
# Check KMS key status
aws kms describe-key --key-id alias/s3proxy-test

# List KMS key grants
aws kms list-grants --key-id alias/s3proxy-test

# Check S3 bucket encryption
aws s3api get-bucket-encryption --bucket your-test-bucket

# Verify proxy configuration
curl -s http://localhost:8080/health

# Check proxy logs
docker logs s3proxy-container

# Monitor KMS usage
aws logs filter-log-events \
  --log-group-name "/aws/kms/your-key-id" \
  --start-time $(date -d "1 hour ago" +%s)000
```

## Cleanup

To clean up test resources:

```bash
./scripts/cleanup-kms-test.sh
```

This will:
- Schedule KMS keys for deletion (7-day wait period)
- Delete all S3 buckets and their contents
- Remove test configuration files

**Note**: KMS keys cannot be immediately deleted. They are scheduled for deletion with a minimum 7-day waiting period.

## Best Practices for Testing

1. **Use Separate Test Account**: Run tests in a dedicated AWS account
2. **Monitor Costs**: KMS operations and S3 storage incur charges
3. **Cleanup Regularly**: Remove test resources when not needed
4. **Test Error Scenarios**: Simulate permission errors and key failures
5. **Load Test Gradually**: Start with low concurrency and increase
6. **Monitor Throttling**: Watch for KMS rate limits
7. **Verify Encryption**: Always validate that objects are actually encrypted

## Advanced Testing

### Custom Test Scenarios

Create custom test scenarios by modifying environment variables:

```bash
# Test with different object sizes
export OBJECT_SIZE_MIN=10485760  # 10MB
export OBJECT_SIZE_MAX=52428800  # 50MB

# Test different concurrency levels
export STRESS_CONCURRENCY=200

# Test for longer duration
export STRESS_DURATION=10m

# Run the test
go test -tags=stress -v ./tests/stress/...
```

### Chaos Testing

Test system resilience:

```bash
# Temporarily disable KMS key (requires careful coordination)
aws kms disable-key --key-id alias/s3proxy-test

# Run operations (should fail gracefully)
# ... test operations ...

# Re-enable key
aws kms enable-key --key-id alias/s3proxy-test
```

### Performance Benchmarking

Compare performance with and without KMS:

```bash
# Disable KMS in config
sed -i 's/enabled: true/enabled: false/' test-config-kms.yaml

# Run benchmark without KMS
./tests/stress/run_stress_test.sh > no-kms-results.txt

# Re-enable KMS
sed -i 's/enabled: false/enabled: true/' test-config-kms.yaml

# Run benchmark with KMS
./tests/stress/run_stress_test.sh > with-kms-results.txt

# Compare results
diff no-kms-results.txt with-kms-results.txt
```
