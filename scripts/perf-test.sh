#!/bin/bash

# Performance test script for S3 Proxy
# Tests throughput and latency with different object sizes

set -e

ENDPOINT="${ENDPOINT:-http://localhost:8080}"
BUCKET="perf-test-bucket"
ITERATIONS=100

# Set AWS credentials that match AUTH_IDENTITY and AUTH_CREDENTIAL
if [ -z "$S3PROXY_ACCESS_KEY" ] || [ -z "$S3PROXY_SECRET_KEY" ]; then
    echo "Error: S3PROXY_ACCESS_KEY and S3PROXY_SECRET_KEY must be set"
    exit 1
fi
export AWS_ACCESS_KEY_ID=${S3PROXY_ACCESS_KEY}
export AWS_SECRET_ACCESS_KEY=${S3PROXY_SECRET_KEY}

echo "S3 Proxy Performance Test"
echo "========================="
echo "Endpoint: $ENDPOINT"
echo "Iterations: $ITERATIONS"
echo ""

# Create test bucket
echo "Creating test bucket..."
aws --endpoint-url $ENDPOINT s3 mb s3://$BUCKET 2>/dev/null || true

# Test different file sizes
for SIZE in 1KB 1MB 10MB 100MB; do
    # Create test file
    case $SIZE in
        1KB)  dd if=/dev/urandom of=/tmp/test-$SIZE bs=1024 count=1 2>/dev/null ;;
        1MB)  dd if=/dev/urandom of=/tmp/test-$SIZE bs=1M count=1 2>/dev/null ;;
        10MB) dd if=/dev/urandom of=/tmp/test-$SIZE bs=1M count=10 2>/dev/null ;;
        100MB) dd if=/dev/urandom of=/tmp/test-$SIZE bs=1M count=100 2>/dev/null ;;
    esac

    echo ""
    echo "Testing with $SIZE file..."
    echo "-------------------------"

    # Upload test
    echo -n "Upload: "
    START=$(date +%s.%N)
    for i in $(seq 1 $ITERATIONS); do
        aws --endpoint-url $ENDPOINT s3 cp /tmp/test-$SIZE s3://$BUCKET/object-$i --no-progress >/dev/null 2>&1
    done
    END=$(date +%s.%N)
    DURATION=$(echo "$END - $START" | bc)
    THROUGHPUT=$(echo "scale=2; $ITERATIONS / $DURATION" | bc)
    echo "$THROUGHPUT ops/sec ($(echo "scale=2; $DURATION / $ITERATIONS * 1000" | bc) ms/op)"

    # Download test
    echo -n "Download: "
    START=$(date +%s.%N)
    for i in $(seq 1 $ITERATIONS); do
        aws --endpoint-url $ENDPOINT s3 cp s3://$BUCKET/object-1 /tmp/download-$i --no-progress >/dev/null 2>&1
    done
    END=$(date +%s.%N)
    DURATION=$(echo "$END - $START" | bc)
    THROUGHPUT=$(echo "scale=2; $ITERATIONS / $DURATION" | bc)
    echo "$THROUGHPUT ops/sec ($(echo "scale=2; $DURATION / $ITERATIONS * 1000" | bc) ms/op)"

    # Cleanup
    rm -f /tmp/test-$SIZE /tmp/download-*
    aws --endpoint-url $ENDPOINT s3 rm s3://$BUCKET --recursive --no-progress >/dev/null 2>&1
done

# Cleanup
echo ""
echo "Cleaning up..."
aws --endpoint-url $ENDPOINT s3 rb s3://$BUCKET --force 2>/dev/null || true

echo ""
echo "Performance test completed!"
