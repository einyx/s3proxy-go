#!/bin/bash

# Test script for Docker KMS setup
set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}Testing S3Proxy Docker Setup${NC}"

# Function to check if service is running
check_service() {
    local service=$1
    local url=$2
    local max_retries=30
    local retry=0

    echo -e "${YELLOW}Checking $service...${NC}"

    while [ $retry -lt $max_retries ]; do
        if curl -sf "$url" > /dev/null 2>&1; then
            echo -e "${GREEN}✓ $service is running${NC}"
            return 0
        fi

        retry=$((retry + 1))
        echo -n "."
        sleep 2
    done

    echo -e "${RED}✗ $service failed to start${NC}"
    return 1
}

# Test basic functionality
test_basic_operations() {
    echo -e "${YELLOW}Testing basic S3 operations...${NC}"

    # Create bucket
    echo "Creating bucket..."
    if curl -sf -X PUT http://admin:secret@${DOCKER_HOST}:8082/test-bucket > /dev/null; then
        echo -e "${GREEN}✓ Bucket created${NC}"
    else
        echo -e "${RED}✗ Failed to create bucket${NC}"
        return 1
    fi

    # Upload file
    echo "Uploading file..."
    if curl -sf -X PUT http://admin:secret@${DOCKER_HOST}:8082/test-bucket/hello.txt -d "Hello, World!" > /dev/null; then
        echo -e "${GREEN}✓ File uploaded${NC}"
    else
        echo -e "${RED}✗ Failed to upload file${NC}"
        return 1
    fi

    # Download file
    echo "Downloading file..."
    CONTENT=$(curl -sf http://admin:secret@${DOCKER_HOST}:8082/test-bucket/hello.txt)
    if [ "$CONTENT" = "Hello, World!" ]; then
        echo -e "${GREEN}✓ File downloaded correctly${NC}"
    else
        echo -e "${RED}✗ File content mismatch. Got: $CONTENT${NC}"
        return 1
    fi

    echo -e "${GREEN}✓ Basic operations successful${NC}"
    return 0
}

# Main test function
main() {
    echo -e "${BLUE}===========================================${NC}"
    echo -e "${BLUE}S3Proxy Docker Test Suite${NC}"
    echo -e "${BLUE}===========================================${NC}"

    # Check if services are running
    # Note: If using remote Docker context, replace localhost with the remote host IP
    DOCKER_HOST="${DOCKER_REMOTE_HOST:-localhost}"

    if ! check_service "S3Proxy" "http://${DOCKER_HOST}:8082/health"; then
        echo -e "${RED}S3Proxy is not running. Please start with:${NC}"
        echo "docker-compose -f docker-compose.kms.yml up -d"
        if [ "$DOCKER_HOST" != "localhost" ]; then
            echo -e "${YELLOW}Note: Using remote Docker host: ${DOCKER_HOST}${NC}"
        fi
        exit 1
    fi

    if ! check_service "MinIO" "http://${DOCKER_HOST}:9000/minio/health/live"; then
        echo -e "${RED}MinIO is not running. Please start with:${NC}"
        echo "docker-compose -f docker-compose.kms.yml up -d"
        exit 1
    fi

    # Test basic operations
    if test_basic_operations; then
        echo -e "${GREEN}===========================================${NC}"
        echo -e "${GREEN}All tests passed!${NC}"
        echo -e "${GREEN}===========================================${NC}"

        echo -e "${BLUE}Next steps:${NC}"
        echo "1. Test with encrypted buckets:"
        echo "   curl -X PUT http://admin:secret@${DOCKER_HOST}:8082/encrypted-bucket"
        echo "   curl -X PUT http://admin:secret@${DOCKER_HOST}:8082/encrypted-bucket/secret.txt -d 'Secret data'"
        echo ""
        echo "2. Check logs:"
        echo "   docker-compose -f docker-compose.kms.yml logs -f s3proxy"
        echo ""
        echo "3. Access MinIO console: http://${DOCKER_HOST}:9001"
        echo "   Credentials: minioadmin / minioadmin"
        if [ "$DOCKER_HOST" != "localhost" ]; then
            echo ""
            echo -e "${YELLOW}Note: Services are running on remote Docker host: ${DOCKER_HOST}${NC}"
        fi

        exit 0
    else
        echo -e "${RED}===========================================${NC}"
        echo -e "${RED}Tests failed!${NC}"
        echo -e "${RED}===========================================${NC}"

        echo -e "${YELLOW}Troubleshooting:${NC}"
        echo "1. Check container logs:"
        echo "   docker-compose -f docker-compose.kms.yml logs"
        echo ""
        echo "2. Check container status:"
        echo "   docker-compose -f docker-compose.kms.yml ps"
        echo ""
        echo "3. Restart services:"
        echo "   docker-compose -f docker-compose.kms.yml restart"

        exit 1
    fi
}

# Run main function
main "$@"
