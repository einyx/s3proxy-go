# S3 Proxy Production Deployment Guide

## Overview

S3 Proxy is a high-performance, cloud-agnostic storage gateway that provides S3-compatible API access to various storage backends including Azure Blob Storage, AWS S3, and local filesystem.

## Production Features

- **Multi-region bucket mapping** with virtual host support
- **High availability** with pod anti-affinity and disruption budgets
- **Auto-scaling** based on CPU and memory metrics
- **Comprehensive monitoring** with Prometheus metrics
- **Security hardening** with pod security contexts and network policies
- **Authentication support** for AWS Signature V4
- **Encryption at rest** (optional) with local keys or KMS

## Deployment

### Prerequisites

- Kubernetes 1.19+
- Helm 3.0+
- Prometheus Operator (for monitoring)
- External Secrets Operator (optional, for secret management)

### Quick Start

1. **Add Helm repository**:
```bash
helm repo add s3proxy https://einyx.github.io/s3proxy-go
helm repo update
```

2. **Install with production values**:
```bash
helm install s3proxy s3proxy/s3proxy \
  --namespace storage \
  --create-namespace \
  -f values-production.yaml
```

### Configuration

Key production configurations:

```yaml
# Replica count for HA
replicaCount: 3

# Resource allocation
resources:
  requests:
    cpu: 1000m
    memory: 2Gi
  limits:
    cpu: 2000m
    memory: 4Gi

# Auto-scaling
autoscaling:
  enabled: true
  minReplicas: 3
  maxReplicas: 20
  targetCPUUtilizationPercentage: 70

# Authentication (required for production)
config:
  auth:
    type: "awsv4"
    identity: ""  # Set via secrets
    credential: "" # Set via secrets

# Monitoring
monitoring:
  enabled: true
  serviceMonitor:
    enabled: true
```

### Storage Backend Configuration

#### Azure Blob Storage
```yaml
config:
  storage:
    provider: "azure"
    azure:
      accountName: "myaccount"
      containerName: "mycontainer"
```

#### AWS S3
```yaml
config:
  storage:
    provider: "s3"
    s3:
      region: "us-east-1"
      endpoint: "" # Leave empty for AWS
```

### Security Best Practices

1. **Enable authentication**:
   - Always use `awsv4` authentication in production
   - Store credentials in Kubernetes secrets or external secret stores

2. **Network policies**:
   - Enable network policies to restrict traffic
   - Only allow ingress from authorized namespaces

3. **Pod security**:
   - Run as non-root user (UID 1000)
   - Read-only root filesystem
   - Drop all capabilities

4. **TLS/SSL**:
   - Use ingress with TLS termination
   - Enable SSL verification for backend connections

### Monitoring

The S3 proxy exposes comprehensive metrics:

- `s3proxy_requests_total` - Total requests by method, bucket, status
- `s3proxy_request_duration_seconds` - Request latency histogram
- `s3proxy_response_size_bytes` - Response size distribution
- `s3proxy_requests_in_flight` - Current concurrent requests
- `s3proxy_storage_operations_total` - Backend storage operations
- `s3proxy_cache_hits_total` - Cache performance metrics

### Grafana Dashboard

Import the included Grafana dashboard for visualization:

```bash
kubectl create configmap s3proxy-dashboard \
  --from-file=dashboard.json=charts/s3proxy/dashboards/s3proxy.json \
  -n monitoring
```

### Troubleshooting

1. **Check pod status**:
```bash
kubectl get pods -n storage -l app.kubernetes.io/name=s3proxy
```

2. **View logs**:
```bash
kubectl logs -n storage -l app.kubernetes.io/name=s3proxy
```

3. **Check metrics**:
```bash
kubectl port-forward -n storage svc/s3proxy 8080:8080
curl http://localhost:8080/metrics
```

### Performance Tuning

1. **Connection settings**:
   - Adjust `readTimeout`, `writeTimeout` based on workload
   - Increase `maxBodySize` for large objects

2. **Resource allocation**:
   - Monitor actual usage and adjust requests/limits
   - Enable horizontal pod autoscaling

3. **Cache configuration** (if enabled):
   - Set appropriate cache size limits
   - Tune TTL based on access patterns

### Upgrade Process

1. **Test in staging**:
   - Always test upgrades in a staging environment first
   - Verify metrics and functionality

2. **Rolling upgrade**:
```bash
helm upgrade s3proxy s3proxy/s3proxy \
  --namespace storage \
  -f values-production.yaml \
  --wait
```

3. **Rollback if needed**:
```bash
helm rollback s3proxy -n storage
```

## Support

For issues and feature requests, please visit:
https://github.com/einyx/s3proxy-go/issues