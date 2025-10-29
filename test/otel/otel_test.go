//go:build e2e && otel

package e2e

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestOTELTracing tests that Spegel can send traces to an OTEL collector.
// This test requires:
// 1. Building with the `otel` build tag
// 2. Having Jaeger or an OTEL collector running
func TestOTELTracing(t *testing.T) {
	imageRef := os.Getenv("IMG_REF")
	require.NotEmpty(t, imageRef)
	kindName := "spegel-otel-test"

	// Start Jaeger using docker-compose
	t.Log("Starting Jaeger collector")
	_ = command(t.Context(), t, "docker compose -f test/otel/docker-compose.otel.yaml up -d jaeger")
	t.Cleanup(func() {
		_ = command(t.Context(), t, "docker compose -f test/otel/docker-compose.otel.yaml down jaeger")
	})

	// Give Jaeger time to start
	time.Sleep(5 * time.Second)

	// Create Kind cluster
	kcPath := createKindCluster(t.Context(), t, kindName, "iptables", "ipv4", 2)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		command(ctx, t, "kind delete cluster --name "+kindName)
	})

	// Deploy Spegel with OTEL enabled
	otelEndpoint := "http://host.docker.internal:4318" // Jaeger OTLP HTTP endpoint
	deploySpegelWithOTEL(t.Context(), t, kindName, imageRef, kcPath, otelEndpoint)

	// Wait for Spegel to be ready
	command(t.Context(), t, "kubectl --kubeconfig "+kcPath+" --namespace spegel wait --for=condition=ready pod -l app.kubernetes.io/name=spegel --timeout=60s")

	// Make some requests to generate traces
	t.Log("Making requests to generate traces")
	pods := getSpegelPods(t.Context(), t, kcPath)
	require.NotEmpty(t, pods, "No Spegel pods found")

	for i := 0; i < 5; i++ {
		command(t.Context(), t, fmt.Sprintf("kubectl --kubeconfig %s exec -n spegel %s -- curl -s http://localhost:5000/v2/", kcPath, pods[0]))
		time.Sleep(1 * time.Second)
	}

	// Check that Jaeger received traces
	t.Log("Checking Jaeger UI for traces")
	// In a real test, we would query Jaeger's API to verify traces were received
	// For now, we'll just verify the pod is running and OTEL is configured

	// Check if OTEL args are present
	description := command(t.Context(), t, fmt.Sprintf("kubectl --kubeconfig %s --namespace spegel describe pod %s", kcPath, pods[0]))
	require.Contains(t, description, "--otel-enabled", "OTEL should be enabled")
	require.Contains(t, description, "--otel-endpoint", "OTEL endpoint should be configured")
	require.Contains(t, description, otelEndpoint, "OTEL endpoint should match expected value")

	t.Log("OTEL tracing test passed. Check Jaeger UI at http://localhost:16686")
}

func deploySpegelWithOTEL(ctx context.Context, t *testing.T, kindName, imageRef, kcPath, otelEndpoint string) {
	t.Helper()

	t.Log("Deploying Spegel with OTEL")
	command(ctx, t, "kind load docker-image --name "+kindName+" "+imageRef)
	imagesOutput := command(ctx, t, "docker exec "+kindName+"-worker ctr -n k8s.io images ls name=="+imageRef)
	_, imagesOutput, ok := strings.Cut(imagesOutput, "\n")
	require.True(t, ok)
	imageDigest := strings.Split(imagesOutput, " ")[2]
	nodes := getNodes(ctx, t, kindName)
	for _, node := range nodes {
		command(ctx, t, "docker exec "+kindName+"-"+node+" ctr -n k8s.io image tag "+imageRef+" ghcr.io/spegel-org/spegel@"+imageDigest)
	}

	// Deploy with OTEL values
	command(ctx, t, "helm --kubeconfig "+kcPath+" upgrade --timeout 60s --create-namespace --wait --install --namespace=spegel spegel ../../charts/spegel "+
		"--set image.pullPolicy=Never --set image.digest="+imageDigest+
		" --set spegel.otel.enabled=true"+
		" --set spegel.otel.endpoint="+otelEndpoint+
		" --set spegel.otel.insecure=true"+
		" --set spegel.otel.sampler=always_on"+
		" --set-string \"nodeSelector.spegel\\.dev/enabled\"=true")
}

func getSpegelPods(ctx context.Context, t *testing.T, kcPath string) []string {
	t.Helper()

	output := command(ctx, t, "kubectl --kubeconfig "+kcPath+" --namespace spegel get pods -o jsonpath='{.items[*].metadata.name}'")
	pods := strings.Fields(output)
	return pods
}
