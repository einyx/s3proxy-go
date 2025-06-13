# Secrets Management Guide

This repository has been sanitized to remove all hardcoded secrets and credentials. Follow these guidelines to maintain security:

## Environment Variables

All sensitive configuration has been replaced with environment variables. Before running the application, set the following variables:

### Required Variables

```bash
# Authentication
export AUTH_IDENTITY="your-auth-identity"
export AUTH_CREDENTIAL="your-secure-password"

# AWS Configuration (if using S3 backend)
export AWS_ACCESS_KEY_ID="your-aws-access-key"
export AWS_SECRET_ACCESS_KEY="your-aws-secret-key"
export AWS_IAM_ROLE_ARN="arn:aws:iam::ACCOUNT_ID:role/your-role"

# Azure Configuration (if using Azure backend)
export AZURE_STORAGE_ACCOUNT="your-storage-account"
export AZURE_STORAGE_KEY="your-storage-key"

# Container Registry
export ECR_REGISTRY="ACCOUNT_ID.dkr.ecr.REGION.amazonaws.com"
```

## Configuration Files

All configuration files now use environment variable placeholders:

- `${VARIABLE_NAME}` - Required variable
- `${VARIABLE_NAME:-default}` - Variable with default value

## Security Scanning

This repository includes multiple layers of secret scanning:

### Pre-commit Hooks

- `detect-secrets` - Scans for potential secrets before commit
- `gitleaks` - Git history scanner for secrets
- Run `pre-commit install` to enable automatic scanning

### CI/CD Pipeline

- TruffleHog - Scans for verified secrets
- Gitleaks - Comprehensive secret detection
- detect-secrets - Python-based secret scanner
- Gosec - Go security analyzer

### Manual Scanning

```bash
# Scan with detect-secrets
detect-secrets scan --all-files

# Scan with gitleaks
gitleaks detect --source . -v

# Scan with trufflehog
trufflehog git file://. --only-verified
```

## Best Practices

1. **Never commit secrets** - Use environment variables or secret management systems
2. **Use .env files locally** - Copy `.env.template` to `.env` and fill in your values
3. **Use secret managers in production** - AWS Secrets Manager, Azure Key Vault, HashiCorp Vault
4. **Rotate credentials regularly** - Implement a rotation policy
5. **Audit access** - Log and monitor secret access
6. **Use least privilege** - Grant minimal required permissions

## Secret Storage Options

### Local Development

- Use `.env` files (ignored by git)
- Use environment variables
- Use local secret managers

### Production

- AWS Secrets Manager
- Azure Key Vault
- HashiCorp Vault
- Kubernetes Secrets (with encryption at rest)
- External secret operators

## If You Find a Secret

If you discover a hardcoded secret:

1. **Do NOT commit it**
2. Replace with environment variable placeholder
3. Add the pattern to `.gitignore` if needed
4. Report to security team if it was already committed
5. Rotate the compromised credential immediately
