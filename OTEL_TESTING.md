# Testing OpenTelemetry Integration

This guide explains how to test Spegel's OpenTelemetry (OTEL) tracing integration with a real OTEL backend.

## Prerequisites

- Docker or Docker Compose installed
- Build with OTEL enabled (requires `otel` build tag)

## Quick Start with Jaeger

1. **Start Jaeger** (All-in-One for local testing):

```bash
docker compose -f docker-compose.otel.yaml up -d jaeger
```

Or manually:

```bash
docker run -d --name jaeger \
  -p 16686:16686 \
  -p 4317:4317 \
  -p 4318:4318 \
  -e COLLECTOR_OTLP_ENABLED=true \
  jaegertracing/all-in-one:latest
```

2. **Build Spegel Docker image with OTEL support**:

```bash
# Build for Linux AMD64
GOOS=linux GOARCH=amd64 goreleaser build --snapshot --clean --single-target --skip before

# Build Docker image
docker buildx build --platform linux/amd64 -t ghcr.io/spegel-org/spegel:local .
```

**Note**: The OTEL build tag is included in the GoReleaser configuration, so building with goreleaser automatically includes OTEL support.

3. **Deploy to a local Kind cluster and test**:

```bash
# Start Jaeger (if not already running)
docker compose -f docker-compose.otel.yaml up -d

# Deploy Spegel with dev-deploy
IMG_REF=ghcr.io/spegel-org/spegel:local make dev-deploy

# Configure Spegel with OTEL
kubectl -n spegel set env daemonset/spegel \
  OTEL_ENABLED=true \
  OTEL_ENDPOINT=http://host.docker.internal:4318 \
  OTEL_SERVICE_NAME=spegel \
  OTEL_SAMPLER=always_on

# Wait for pods to restart
kubectl -n spegel rollout status daemonset/spegel

# Make some requests to generate traces (e.g., via test pods)
kubectl -n pull-test create deployment test --image=busybox --replicas=1
```

4. **View traces in Jaeger UI**:

Open http://localhost:16686 in your browser.

5. **Port-forward to access from your machine** (optional):

```bash
# Port-forward Jaeger to make it accessible from your machine
kubectl -n default port-forward svc/jaeger 16686:16686
```

Then access Jaeger at http://localhost:16686

## Command-Line Arguments

- `--otel-enabled` (or `OTEL_ENABLED=true`): Enable OTEL tracing
- `--otel-endpoint` (or `OTEL_ENDPOINT`): OTEL collector endpoint (default: empty)
  - HTTP endpoint: `http://otel-collector:4318/v1/traces`
  - gRPC endpoint: `otel-collector:4317`
- `--otel-insecure` (or `OTEL_INSECURE=true`): Use insecure connection (for dev)
- `--otel-service-name` (or `OTEL_SERVICE_NAME`): Service name for traces (default: "spegel")
- `--otel-sampler` (or `OTEL_SAMPLER`): Sampling strategy
  - `always_on`: All traces
  - `always_off`: No traces
  - `parentbased_always_on`: Follow parent context or trace everything
  - `parentbased_always_off`: Follow parent context or trace nothing
  - `0.0` to `1.0`: Trace a percentage (e.g., `0.1` = 10%)

## Environment Variables

Instead of command-line args, you can use environment variables:

```bash
export OTEL_ENABLED=true
export OTEL_ENDPOINT=http://localhost:4318
export OTEL_SERVICE_NAME=spegel
export OTEL_SAMPLER=always_on

./spegel registry
```

## Helm Deployment with OTEL

Update `values.yaml`:

```yaml
spegel:
  otel:
    enabled: true
    endpoint: "http://otel-collector.default.svc.cluster.local:4318"
    insecure: false
    serviceName: "spegel"
    sampler: "parentbased_always_off"
```

Then deploy:

```bash
helm upgrade --install spegel ./charts/spegel -f values.yaml
```

## What Gets Traced

When OTEL is enabled, Spegel instruments:

1. **HTTP Handlers**: All registry and metrics HTTP handlers
2. **HTTP Transport**: All outgoing requests to OCI registries
3. **P2P Operations**: Lookup and routing operations
4. **Log Correlation**: Adds `trace_id` and `span_id` to structured logs

Example log output:

```json
{
  "level": "info",
  "trace_id": "1234567890abcdef",
  "span_id": "abcdef1234567890",
  "msg": "processing request",
  "path": "/v2/image/blobs/sha256:..."
}
```

## Testing with Different Backends

### Using OpenTelemetry Collector

```bash
docker run -d --name otel-collector \
  -p 4317:4317 \
  -p 4318:4318 \
  -v /path/to/config.yaml:/etc/otel-collector.yaml \
  otel/opentelemetry-collector:latest
```

### Using Zipkin

Update Jaeger to export to Zipkin or use an OTEL Collector with a Zipkin exporter.

## Viewing Traces

### Jaeger UI

1. Go to http://localhost:16686
2. Select `spegel` from the service dropdown
3. Click "Find Traces"
4. Select a trace to view spans and timing

### Grafana Tempo + Loki

If using Grafana Cloud or self-hosted:

```bash
# In Grafana, go to Explore
# Select Tempo data source
# Query by service name: spegel
```

## Performance Testing

Run e2e tests with OTEL enabled:

```bash
# Build with OTEL
go build -tags otel -o spegel ./...

# Run e2e tests
make test-e2e
```

## Troubleshooting

### No traces appearing

1. Verify OTEL collector is running:
   ```bash
   curl http://localhost:4318/v1/traces
   ```

2. Check Spegel logs for OTEL setup errors:
   ```bash
   kubectl logs -l app=spegel | grep -i otel
   ```

3. Verify endpoint is reachable:
   ```bash
   kubectl exec -it <spegel-pod> -- wget -O- http://otel-collector:4318/v1/traces
   ```

### High cardinality issues

Use sampling to reduce trace volume:

```yaml
sampler: "0.1"  # Only trace 10% of requests
```

### Missing spans

Ensure you're building with the `otel` tag:

```bash
go build -tags otel ...
```

## Production Considerations

- Use `parentbased_always_off` sampler by default to reduce noise
- Configure appropriate trace retention in your backend
- Consider using a dedicated OTEL collector instead of direct exporter
- Monitor OTEL exporter performance metrics

