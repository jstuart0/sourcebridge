# Air-Gapped Deployment

## Overview

An air-gapped deployment runs SourceBridge.ai in an environment with no outbound internet access. This requires:

1. Pre-loading all container images into a local registry
2. Running a local LLM instead of OpenAI/Anthropic APIs
3. Using an internal CA for TLS certificates
4. Running the Indexing Agent on-premises (no GitHub/GitLab cloud webhooks)

## Required Container Images

Pull these images on an internet-connected machine and transfer them to your air-gapped registry:

```bash
# Core SourceBridge.ai images
docker pull ghcr.io/sourcebridge/sourcebridge:1.4.0
docker pull ghcr.io/sourcebridge/sourcebridge-web:1.4.0
docker pull ghcr.io/sourcebridge/sourcebridge-worker:1.4.0

# Dependencies
docker pull surrealdb/surrealdb:v2.0
docker pull redis:7-alpine

# Local LLM (choose one)
docker pull ollama/ollama:latest
# or
docker pull vllm/vllm-openai:latest
```

Save images to a tarball:

```bash
docker save \
  ghcr.io/sourcebridge/sourcebridge:1.4.0 \
  ghcr.io/sourcebridge/sourcebridge-web:1.4.0 \
  ghcr.io/sourcebridge/sourcebridge-worker:1.4.0 \
  surrealdb/surrealdb:v2.0 \
  redis:7-alpine \
  ollama/ollama:latest \
  | gzip > sourcebridge-images-1.4.0.tar.gz
```

Load into your air-gapped registry:

```bash
# On the air-gapped network
docker load < sourcebridge-images-1.4.0.tar.gz

# Re-tag and push to internal registry
for img in sourcebridge sourcebridge-web sourcebridge-worker; do
  docker tag ghcr.io/sourcebridge/${img}:1.4.0 registry.internal/sourcebridge/${img}:1.4.0
  docker push registry.internal/sourcebridge/${img}:1.4.0
done

docker tag surrealdb/surrealdb:v2.0 registry.internal/surrealdb/surrealdb:v2.0
docker push registry.internal/surrealdb/surrealdb:v2.0

docker tag redis:7-alpine registry.internal/redis:7-alpine
docker push registry.internal/redis:7-alpine

docker tag ollama/ollama:latest registry.internal/ollama/ollama:latest
docker push registry.internal/ollama/ollama:latest
```

## Local LLM Setup

### Ollama (Recommended)

Ollama provides the simplest setup for local inference:

```bash
# Start Ollama server
docker run -d --name ollama \
  --gpus all \
  -v ollama_models:/root/.ollama \
  -p 11434:11434 \
  registry.internal/ollama/ollama:latest

# Pre-download a model on an internet-connected machine, then copy
ollama pull llama3.1:8b
# Copy ~/.ollama/models to the air-gapped host

# Or load from a file
docker cp ./models/ ollama:/root/.ollama/models/
```

Configure SourceBridge.ai workers:

```yaml
worker:
  env:
    SOURCEBRIDGE_LLM_PROVIDER: ollama
    OLLAMA_URL: http://ollama.internal:11434
    SOURCEBRIDGE_LLM_MODEL: llama3.1:8b
```

### vLLM (High Throughput)

For production workloads with GPU hardware:

```bash
docker run -d --name vllm \
  --gpus all \
  -v /path/to/models:/models \
  -p 8000:8000 \
  registry.internal/vllm/vllm-openai:latest \
  --model /models/Meta-Llama-3.1-8B-Instruct \
  --served-model-name llama3.1
```

Configure workers to use vLLM's OpenAI-compatible API:

```yaml
worker:
  env:
    SOURCEBRIDGE_LLM_PROVIDER: openai
    OPENAI_API_KEY: not-needed
    OPENAI_API_BASE: http://vllm.internal:8000/v1
    SOURCEBRIDGE_LLM_MODEL: llama3.1
```

## Local Indexing Agent Configuration

In an air-gapped environment, the Indexing Agent runs inside your network and pushes metadata (never source code) to the SourceBridge.ai API:

```bash
# Install the agent binary on a machine with repo access
sourcebridge agent install

# Configure agent
cat > /etc/sourcebridge/agent.yaml <<EOF
apiURL: https://sourcebridge.internal/api/v1/agent/ingest
apiToken: ${AGENT_TOKEN}
repoPath: /srv/repos/my-project
excludePatterns:
  - .git/
  - node_modules/
  - vendor/
  - "*.test.*"
schedule: "*/30 * * * *"  # every 30 minutes
EOF

# Run once to verify
sourcebridge agent index --config /etc/sourcebridge/agent.yaml

# Enable the systemd timer for scheduled runs
systemctl enable --now sourcebridge-agent.timer
```

The agent extracts file paths, symbol names, kinds, and line numbers. It transmits zero source code to the server.

## Network Requirements

Air-gapped SourceBridge.ai requires only internal network connectivity:

| Source | Destination | Port | Protocol | Purpose |
|--------|-------------|------|----------|---------|
| Users | Web UI | 443 | HTTPS | Browser access |
| Web UI | API Server | 8080 | HTTP | GraphQL API |
| API Server | SurrealDB | 8000 | WS | Database |
| API Server | Redis | 6379 | TCP | Job queue, cache |
| Worker | SurrealDB | 8000 | WS | Database |
| Worker | Redis | 6379 | TCP | Job queue |
| Worker | Ollama/vLLM | 11434/8000 | HTTP | LLM inference |
| Agent | API Server | 443 | HTTPS | Metadata ingest |

No outbound internet access is required for any component.

## Certificate Management Without Let's Encrypt

### Using an Internal CA

Generate certificates with your organization's CA:

```bash
# Generate a CSR
openssl req -new -newkey rsa:2048 -nodes \
  -keyout sourcebridge.key -out sourcebridge.csr \
  -subj "/CN=sourcebridge.internal/O=MyOrg"

# Sign with your internal CA
openssl x509 -req -in sourcebridge.csr \
  -CA /path/to/ca.crt -CAkey /path/to/ca.key \
  -CAcreateserial -out sourcebridge.crt -days 365

# Create Kubernetes TLS secret
kubectl -n sourcebridge create secret tls sourcebridge-tls \
  --cert=sourcebridge.crt --key=sourcebridge.key
```

### Using cert-manager with a Private Issuer

If cert-manager is deployed in the air-gapped cluster:

```yaml
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: internal-ca
spec:
  ca:
    secretName: internal-ca-keypair

---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: sourcebridge-cert
  namespace: sourcebridge
spec:
  secretName: sourcebridge-tls
  issuerRef:
    name: internal-ca
    kind: ClusterIssuer
  dnsNames:
    - sourcebridge.internal
    - "*.sourcebridge.internal"
```

### Distributing the CA Bundle

Ensure all components trust your internal CA:

```yaml
# Mount CA bundle into pods
volumes:
  - name: ca-certs
    configMap:
      name: internal-ca-bundle
volumeMounts:
  - name: ca-certs
    mountPath: /etc/ssl/certs/internal-ca.crt
    subPath: ca.crt
env:
  - name: SSL_CERT_FILE
    value: /etc/ssl/certs/internal-ca.crt
```

## Verification

After deployment, confirm all components are operational:

```bash
# Health check
curl --cacert /path/to/ca.crt https://sourcebridge.internal/readyz

# Verify LLM connectivity
curl http://ollama.internal:11434/api/tags

# Test indexing agent
sourcebridge agent index --config /etc/sourcebridge/agent.yaml --dry-run

# Confirm no outbound connections (from any SourceBridge.ai pod)
kubectl -n sourcebridge exec deploy/sourcebridge-api -- \
  curl -s --connect-timeout 3 https://api.openai.com 2>&1 || echo "No outbound: OK"
```
