.PHONY: build run test clean docker-build docker-run help

# Variables
BINARY_NAME=s3proxy
DOCKER_IMAGE=s3proxy-go
DOCKER_TAG=latest
GO_VERSION=1.21

# Default target
.DEFAULT_GOAL := help

## Build the binary (optimized for speed)
build:
	go build -ldflags "-s -w" -o bin/$(BINARY_NAME) ./cmd/s3proxy

## Run the application locally
run: build
	./bin/$(BINARY_NAME)

## Run with MinIO backend (local development)
# Required: S3PROXY_ACCESS_KEY and S3PROXY_SECRET_KEY must be set
run-minio:
	SERVER_LISTEN=:8080 \
	STORAGE_PROVIDER=s3 \
	S3_ENDPOINT=http://localhost:9000 \
	S3_ACCESS_KEY=$${MINIO_ACCESS_KEY} \
	S3_SECRET_KEY=$${MINIO_SECRET_KEY} \
	S3_DISABLE_SSL=true \
	AUTH_TYPE=awsv4 \
	AUTH_IDENTITY=$${S3PROXY_ACCESS_KEY} \
	AUTH_CREDENTIAL=$${S3PROXY_SECRET_KEY} \
	go run ./cmd/s3proxy

# Required environment variables:
#   - AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY (takes precedence)
#   - AUTH_IDENTITY / AUTH_CREDENTIAL
#   - S3PROXY_ACCESS_KEY / S3PROXY_SECRET_KEY
run-aws:
	SERVER_LISTEN=:8080 \
	STORAGE_PROVIDER=s3 \
	S3_REGION=$${AWS_REGION:-us-east-1} \
	S3_ACCESS_KEY=$${AWS_ACCESS_KEY_ID} \
	S3_SECRET_KEY=$${AWS_SECRET_ACCESS_KEY} \
	AUTH_TYPE=awsv4 \
	AUTH_IDENTITY=$${S3PROXY_ACCESS_KEY} \
	AUTH_CREDENTIAL=$${S3PROXY_SECRET_KEY} \
	go run ./cmd/s3proxy

# Required environment variables:
#   - AZURE_STORAGE_ACCOUNT: Azure storage account name
#   - AZURE_STORAGE_KEY: Azure storage account key
#   - S3PROXY_ACCESS_KEY: Access key for S3 API authentication
#   - S3PROXY_SECRET_KEY: Secret key for S3 API authentication
run-azure:
	SERVER_LISTEN=:8080 \
	STORAGE_PROVIDER=azure \
	AZURE_ACCOUNT_NAME=$${AZURE_STORAGE_ACCOUNT} \
	AZURE_ACCOUNT_KEY=$${AZURE_STORAGE_KEY} \
	AZURE_CONTAINER_NAME=$${AZURE_CONTAINER_NAME} \
	AUTH_TYPE=awsv4 \
	AUTH_IDENTITY=$${S3PROXY_ACCESS_KEY} \
	AUTH_CREDENTIAL=$${S3PROXY_SECRET_KEY} \
	go run ./cmd/s3proxy --listen :8081

## Run tests
test:
	go test -v ./...
