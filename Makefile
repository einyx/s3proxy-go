.PHONY: all build run test clean docker-build docker-run lint fmt install-tools pre-commit encryption-test help

# Variables
BINARY_NAME=s3proxy
DOCKER_IMAGE=s3proxy-go
DOCKER_TAG=latest
GO_VERSION=1.21
GO=go
GOLINT=golangci-lint
GOTEST=$(GO) test
GOBUILD=$(GO) build
GOCLEAN=$(GO) clean
GOGET=$(GO) get
GOMOD=$(GO) mod

# Default target
.DEFAULT_GOAL := help

## Build the binary (optimized for speed)
build:
	$(GOBUILD) -ldflags "-s -w" -o bin/$(BINARY_NAME) ./cmd/s3proxy

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

## Run all tests
test:
	$(GOTEST) -v -race -coverprofile=coverage.txt -covermode=atomic ./...

## Run only encryption tests
encryption-test:
	$(GOTEST) -v ./internal/encryption/... ./internal/storage -run "Encrypt"

## Run Azure encryption tests (requires Azure credentials)
azure-encryption-test:
	@echo "Running Azure encryption tests..."
	@if [ -z "$$AZURE_STORAGE_ACCOUNT" ] || [ -z "$$AZURE_STORAGE_KEY" ]; then \
		echo "Error: AZURE_STORAGE_ACCOUNT and AZURE_STORAGE_KEY must be set"; \
		exit 1; \
	fi
	$(GOTEST) -v ./internal/storage -run "TestAzureEncryption"

## Run Azure encryption test script
test-azure-encryption:
	@echo "Running Azure encryption test script..."
	@./test-azure-encryption.sh

## Clean build artifacts and test files
clean:
	$(GOCLEAN)
	rm -rf bin/
	rm -f coverage.txt coverage.html
	rm -rf test-data/ debug-fs/ test-fs-encryption/
	rm -f test-*.go debug-*.go
	rm -f *.dat
	rm -f .test-master-key

## Run linters
lint:
	$(GOLINT) run ./...

## Format code
fmt:
	$(GO) fmt ./...
	goimports -w -local github.com/einyx/s3proxy-go .

## Install development tools
install-tools:
	@echo "Installing development tools..."
	$(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	$(GO) install golang.org/x/tools/cmd/goimports@latest
	@command -v pre-commit >/dev/null 2>&1 || pip install pre-commit
	@echo "Tools installed successfully"

## Install pre-commit hooks
install-hooks: install-tools
	pre-commit install
	pre-commit install --hook-type commit-msg
	@echo "Pre-commit hooks installed"

## Run pre-commit on all files
pre-commit:
	pre-commit run --all-files

## Update dependencies
deps:
	$(GOMOD) download
	$(GOMOD) tidy

## Run the encryption test script
test-encryption-local:
	@echo "Running local encryption tests..."
	@./test-encryption.sh

## Generate test coverage report
coverage: test
	$(GO) tool cover -html=coverage.txt -o coverage.html
	@echo "Coverage report generated: coverage.html"

## Run benchmarks
bench:
	$(GOTEST) -bench=. -benchmem ./...

## Security scan
security:
	@echo "Running security scan..."
	@command -v gosec >/dev/null 2>&1 || $(GO) install github.com/securego/gosec/v2/cmd/gosec@latest
	gosec -fmt=json -out=security-report.json ./... || true
	@echo "Security report generated: security-report.json"

## Docker build
docker-build:
	docker build -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

## Docker run with filesystem backend
docker-run:
	docker run -it --rm \
		-p 8080:8080 \
		-e STORAGE_PROVIDER=filesystem \
		-e FS_BASE_DIR=/data \
		-e AUTH_TYPE=none \
		-v $${PWD}/data:/data \
		$(DOCKER_IMAGE):$(DOCKER_TAG)

## Help
help:
	@echo "Available targets:"
	@echo "  make build                  - Build the binary"
	@echo "  make run                   - Run the application locally"
	@echo "  make run-minio             - Run with MinIO backend"
	@echo "  make run-aws               - Run with AWS S3 backend"
	@echo "  make run-azure             - Run with Azure Blob backend"
	@echo "  make test                  - Run all tests"
	@echo "  make encryption-test       - Run encryption tests only"
	@echo "  make azure-encryption-test - Run Azure encryption tests"
	@echo "  make test-azure-encryption - Run Azure encryption test script"
	@echo "  make clean                 - Clean build artifacts"
	@echo "  make lint                  - Run linters"
	@echo "  make fmt                   - Format code"
	@echo "  make install-tools         - Install development tools"
	@echo "  make install-hooks         - Install pre-commit hooks"
	@echo "  make pre-commit            - Run pre-commit on all files"
	@echo "  make deps                  - Update dependencies"
	@echo "  make coverage              - Generate test coverage report"
	@echo "  make bench                 - Run benchmarks"
	@echo "  make security              - Run security scan"
	@echo "  make docker-build          - Build Docker image"
	@echo "  make docker-run            - Run Docker container"
	@echo "  make help                  - Show this help message"
