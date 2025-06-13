#!/bin/bash
set -e

echo "Azure Blob Storage Encryption Test"
echo "=================================="
echo

# Check if Azure CLI is configured
if ! command -v az &> /dev/null; then
    echo "Error: Azure CLI is not installed or not in PATH"
    exit 1
fi

# Check if logged in
if ! az account show &> /dev/null; then
    echo "Error: Not logged into Azure CLI. Please run 'az login' first."
    exit 1
fi

# Get or prompt for Azure storage account details
if [ -z "$AZURE_STORAGE_ACCOUNT" ]; then
    echo "AZURE_STORAGE_ACCOUNT not set."
    echo "Available storage accounts:"
    az storage account list --query "[].name" -o tsv | head -10
    echo
    read -r -p "Enter storage account name: " AZURE_STORAGE_ACCOUNT
    export AZURE_STORAGE_ACCOUNT
fi

if [ -z "$AZURE_STORAGE_KEY" ]; then
    echo "Fetching storage account key..."
    # Get the resource group for the storage account
    RG=$(az storage account list --query "[?name=='$AZURE_STORAGE_ACCOUNT'].resourceGroup" -o tsv)
    if [ -z "$RG" ]; then
        echo "Error: Could not find resource group for storage account $AZURE_STORAGE_ACCOUNT"
        exit 1
    fi

    # Get the first key
    AZURE_STORAGE_KEY=$(az storage account keys list \
        --account-name "$AZURE_STORAGE_ACCOUNT" \
        --resource-group "$RG" \
        --query "[0].value" -o tsv)
    export AZURE_STORAGE_KEY

    if [ -z "$AZURE_STORAGE_KEY" ]; then
        echo "Error: Could not fetch storage account key"
        exit 1
    fi
    echo "✓ Storage account key retrieved"
fi

# Set container name if not set
if [ -z "$AZURE_CONTAINER_NAME" ]; then
    AZURE_CONTAINER_NAME="test-encryption-$(date +%s)"
    export AZURE_CONTAINER_NAME
    echo "Using container name: $AZURE_CONTAINER_NAME"
fi

# Show configuration
echo
echo "Configuration:"
echo "  Storage Account: $AZURE_STORAGE_ACCOUNT"
echo "  Container: $AZURE_CONTAINER_NAME"
echo

# Run the test
echo "Running Azure encryption test..."
echo "--------------------------------"
./test-azure-encryption

# Optionally delete the container
echo
read -p "Delete test container '$AZURE_CONTAINER_NAME'? (y/N) " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
    echo "Deleting container..."
    az storage container delete \
        --name "$AZURE_CONTAINER_NAME" \
        --account-name "$AZURE_STORAGE_ACCOUNT" \
        --account-key "$AZURE_STORAGE_KEY" \
        --yes
    echo "✓ Container deleted"
fi

echo
echo "Test completed!"
