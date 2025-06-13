#!/bin/bash

echo "Testing S3 proxy with minio/minio123 authentication..."
echo ""

# Configure AWS CLI with proxy credentials
echo "Configuring AWS CLI with minio credentials..."
aws configure set aws_access_key_id minio
aws configure set aws_secret_access_key minio123
aws configure set default.region us-east-1

echo "Current AWS CLI configuration:"
aws configure list
echo ""

# Test the proxy
echo "Testing proxy connection..."
echo "Make sure to run: kubectl -n dev port-forward svc/s3proxy 9000:9000"
echo ""

echo "Testing commands:"
echo "1. List buckets:"
echo "   aws s3 ls --endpoint-url http://localhost:9000"
echo ""
echo "2. List objects in bucket:"
echo "   aws s3 ls s3://dev-terraform-managed-bucket --endpoint-url http://localhost:9000"
echo ""
echo "3. With debug output:"
echo "   aws s3 ls s3://dev-terraform-managed-bucket --endpoint-url http://localhost:9000 --debug"
echo ""

read -r -p "Press Enter to test now (make sure port-forward is running)..."

echo "Testing bucket list..."
aws s3 ls --endpoint-url http://localhost:9000

echo ""
echo "Testing specific bucket..."
aws s3 ls s3://dev-terraform-managed-bucket --endpoint-url http://localhost:9000
