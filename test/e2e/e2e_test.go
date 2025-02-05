//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sync/errgroup"

	"github.com/stretchr/testify/require"
)

func TestE2E(t *testing.T) {
	t.Parallel()
	t.Log("Running E2E tests")

	ctx := context.Background()

	imageRef := os.Getenv("IMG_REF")
	require.NotEmpty(t, imageRef)
	proxyMode := os.Getenv("E2E_PROXY_MODE")
	require.NotEmpty(t, proxyMode)
	ipFamily := os.Getenv("E2E_IP_FAMILY")
	require.NotEmpty(t, ipFamily)

	kcPath := filepath.Join(t.TempDir(), "kind.kubeconfig")
	kindName := "spegel-e2e"

	// Create kind cluster.
	t.Log("Creating Kind cluster", "proxy mode", proxyMode, "ip family", ipFamily)
	kindCfgPath := generateKindConfiguration(t, proxyMode, ipFamily)
	command(ctx, t, fmt.Sprintf("kind create cluster --kubeconfig %s --config %s --name %s", kcPath, kindCfgPath, kindName))
	t.Cleanup(func() {
		t.Log("Deleting Kind cluster")
		command(ctx, t, fmt.Sprintf("kind delete cluster --name %s", kindName))
	})

	// Pull test images.
	g, gCtx := errgroup.WithContext(ctx)
	images := []string{
		"ghcr.io/spegel-org/conformance:75d2816",
		"docker.io/library/nginx:1.23.0",
		"docker.io/library/nginx@sha256:b3a676a9145dc005062d5e79b92d90574fb3bf2396f4913dc1732f9065f55c4b",
		"mcr.microsoft.com/containernetworking/azure-cns@sha256:7944413c630746a35d5596f56093706e8d6a3db0569bec0c8e58323f965f7416",
	}
	for _, image := range images {
		g.Go(func() error {
			t.Logf("Pulling image %s", image)
			_, err := commandWithError(gCtx, t, fmt.Sprintf("docker exec %s-worker ctr -n k8s.io image pull %s", kindName, image))
			if err != nil {
				return err
			}
			return nil
		})
	}
	err := g.Wait()
	require.NoError(t, err)

	// Write existing configuration to test backup.
	hostsToml := `server = https://docker.io

[host.https://registry-1.docker.io]
  capabilities = [push]`
	command(ctx, t, fmt.Sprintf("docker exec %s-worker2 bash -c \"mkdir -p /etc/containerd/certs.d/docker.io; echo -e '%s' > /etc/containerd/certs.d/docker.io/hosts.toml\"", kindName, hostsToml))

	// Taint nodes
	t.Log("Tainting nodes")
	command(ctx, t, fmt.Sprintf("kubectl --kubeconfig %s taint nodes --all spegel/not-ready=true:NoSchedule", kcPath))

	// Deploy Spegel.
	t.Log("Deploying Spegel")
	command(ctx, t, fmt.Sprintf("kind load docker-image --name %s %s", kindName, imageRef))
	imageDigest := command(ctx, t, fmt.Sprintf("docker exec %s-worker crictl inspecti -o 'go-template' --template '{{ index .status.repoDigests 0 }}' %s", kindName, imageRef))
	imageDigest = strings.Split(imageDigest, "@")[1]
	nodes := []string{
		"control-plane",
		"worker",
		"worker2",
		"worker3",
		"worker4",
	}
	for _, node := range nodes {
		command(ctx, t, fmt.Sprintf("docker exec %s-%s ctr -n k8s.io image tag %s ghcr.io/spegel-org/spegel@%s", kindName, node, imageRef, imageDigest))
	}
	command(ctx, t, fmt.Sprintf("helm --kubeconfig %s upgrade --timeout 60s --create-namespace --wait --install --namespace=\"spegel\" spegel ../../charts/spegel --set \"image.pullPolicy=Never\" --set \"image.digest=%s\" --set \"nodeSelector.spegel=schedule\" --set \"spegel.notReadyTaint.removeWhenReady=true\"", kcPath, imageDigest))
	podOutput := command(ctx, t, fmt.Sprintf("kubectl --kubeconfig %s --namespace spegel get pods --no-headers", kcPath))
	require.Len(t, strings.Split(podOutput, "\n"), 5)

	// Verify that configuration has been backed up.
	backupHostToml := command(ctx, t, fmt.Sprintf("docker exec %s-worker2 cat /etc/containerd/certs.d/_backup/docker.io/hosts.toml", kindName))
	require.Equal(t, hostsToml, backupHostToml)

	// Verify that spegel has removed the not-ready taint from nodes.
	taintsTpl := `{{range .items}}{{$hasTaint := false}}{{range .spec.taints}}{{if eq .key "spegel/not-ready"}}{{$hasTaint = true}}{{end}}{{end}}{{if $hasTaint}}{{.metadata.name}}{{"\n"}}{{end}}{{end}}`
	nodeOutput := command(ctx, t, fmt.Sprintf("kubectl --kubeconfig %s get nodes -o go-template='%s'", kcPath, taintsTpl))
	require.Empty(t, nodeOutput)

	// Run conformance tests.
	t.Log("Running conformance tests")
	command(ctx, t, fmt.Sprintf("kubectl --kubeconfig %s create namespace conformance --dry-run=client -o yaml | kubectl --kubeconfig %s apply -f -", kcPath, kcPath))
	command(ctx, t, fmt.Sprintf("kubectl --kubeconfig %s apply --namespace conformance -f ./testdata/conformance-job.yaml", kcPath))
	command(ctx, t, fmt.Sprintf("kubectl --kubeconfig %s --namespace conformance wait --for=condition=complete job/conformance", kcPath))

	// Remove Spegel from the last node to test that the mirror fallback is working.
	workerPod := command(ctx, t, fmt.Sprintf("kubectl --kubeconfig %s --namespace spegel get pods --no-headers -o name --field-selector spec.nodeName=%s-worker4", kcPath, kindName))
	command(ctx, t, fmt.Sprintf("kubectl --kubeconfig %s label nodes %s-worker4 spegel-", kcPath, kindName))
	command(ctx, t, fmt.Sprintf("kubectl --kubeconfig %s --namespace spegel wait --for=delete %s --timeout=60s", kcPath, workerPod))

	// Pull image from registry after Spegel has started.
	command(ctx, t, fmt.Sprintf("docker exec %s-worker ctr -n k8s.io image pull docker.io/library/nginx:1.21.0@sha256:2f1cd90e00fe2c991e18272bb35d6a8258eeb27785d121aa4cc1ae4235167cfd", kindName))

	// Verify that both local and external ports are working.
	tests := []struct {
		node     string
		port     string
		expected string
	}{
		{
			node:     "worker",
			port:     "30020",
			expected: "200",
		},
		{
			node:     "worker",
			port:     "30021",
			expected: "200",
		},
		{
			node:     "worker4",
			port:     "30020",
			expected: "000",
		},
		{
			node:     "worker4",
			port:     "30021",
			expected: "200",
		},
	}
	for _, tt := range tests {
		hostIP := command(ctx, t, fmt.Sprintf("kubectl --kubeconfig %s --namespace spegel get nodes %s-%s -o jsonpath='{.status.addresses[?(@.type==\"InternalIP\")].address}'", kcPath, kindName, tt.node))
		addr, err := netip.ParseAddr(hostIP)
		require.NoError(t, err)
		if addr.Is6() {
			hostIP = fmt.Sprintf("[%s]", hostIP)
		}
		httpCode := command(ctx, t, fmt.Sprintf("docker exec %s-worker curl -s -o /dev/null -w \"%%{http_code}\" http://%s:%s/healthz || true", kindName, hostIP, tt.port))
		require.Equal(t, tt.expected, httpCode)
	}

	// Block internet access by only allowing RFC1918 CIDR.
	for _, node := range nodes {
		command(ctx, t, fmt.Sprintf("docker exec %s-%s iptables -A OUTPUT -o eth0 -d 10.0.0.0/8 -j ACCEPT", kindName, node))
		command(ctx, t, fmt.Sprintf("docker exec %s-%s iptables -A OUTPUT -o eth0 -d 172.16.0.0/12 -j ACCEPT", kindName, node))
		command(ctx, t, fmt.Sprintf("docker exec %s-%s iptables -A OUTPUT -o eth0 -d 192.168.0.0/16 -j ACCEPT", kindName, node))
		command(ctx, t, fmt.Sprintf("docker exec %s-%s iptables -A OUTPUT -o eth0 -j REJECT", kindName, node))
	}

	// Pull test image that does not contain any media types.
	command(ctx, t, fmt.Sprintf("docker exec %s-worker3 crictl pull mcr.microsoft.com/containernetworking/azure-cns@sha256:7944413c630746a35d5596f56093706e8d6a3db0569bec0c8e58323f965f7416", kindName))

	// Deploy test Nginx pods and verify deployment status.
	t.Log("Deploy test Nginx pods")
	command(ctx, t, fmt.Sprintf("kubectl --kubeconfig %s apply -f ./testdata/test-nginx.yaml", kcPath))
	command(ctx, t, fmt.Sprintf("kubectl --kubeconfig %s --namespace nginx wait --timeout=30s deployment/nginx-tag --for condition=available", kcPath))
	command(ctx, t, fmt.Sprintf("kubectl --kubeconfig %s --namespace nginx wait --timeout=30s deployment/nginx-digest --for condition=available", kcPath))
	command(ctx, t, fmt.Sprintf("kubectl --kubeconfig %s --namespace nginx wait --timeout=30s deployment/nginx-tag-and-digest --for condition=available", kcPath))
	command(ctx, t, fmt.Sprintf("kubectl --kubeconfig %s --namespace nginx wait --timeout=30s -l app=nginx-not-present --for jsonpath='{.status.containerStatuses[*].state.waiting.reason}'=ImagePullBackOff pod", kcPath))

	// Verify that Spegel has never restarted.
	restartOutput := command(ctx, t, fmt.Sprintf("kubectl --kubeconfig %s --namespace spegel get pods -o=jsonpath='{.items[*].status.containerStatuses[0].restartCount}'", kcPath))
	require.Equal(t, "0 0 0 0", restartOutput)

	// Remove all Spegel Pods and only restart one to verify that running a single instance works.
	t.Log("Scale down Spegel to single instance")
	command(ctx, t, fmt.Sprintf("kubectl --kubeconfig %s label nodes %s-control-plane %s-worker %s-worker2 spegel-", kcPath, kindName, kindName, kindName))
	command(ctx, t, fmt.Sprintf("kubectl --kubeconfig %s --namespace spegel delete pods --all", kcPath))
	command(ctx, t, fmt.Sprintf("kubectl --kubeconfig %s --namespace spegel rollout status daemonset spegel --timeout 60s", kcPath))
	podOutput = command(ctx, t, fmt.Sprintf("kubectl --kubeconfig %s --namespace spegel get pods --no-headers", kcPath))
	require.Len(t, strings.Split(podOutput, "\n"), 1)

	// Verify that Spegel has never restarted
	restartOutput = command(ctx, t, fmt.Sprintf("kubectl --kubeconfig %s --namespace spegel get pods -o=jsonpath='{.items[*].status.containerStatuses[0].restartCount}'", kcPath))
	require.Equal(t, "0", restartOutput)
}

func generateKindConfiguration(t *testing.T, proxyMode, ipFamily string) string {
	t.Helper()

	kindConfig := fmt.Sprintf(`apiVersion: kind.x-k8s.io/v1alpha4
kind: Cluster
networking:
  kubeProxyMode: %s
  ipFamily: %s
containerdConfigPatches:
- |-
  [plugins."io.containerd.grpc.v1.cri".registry]
    config_path = "/etc/containerd/certs.d"
  # Discarding unpacked layers causes them to be removed, which defeats the purpose of a local cache.
  # Aditioanlly nodes will report having layers which no long exist.
  # This is by default false in containerd.
  [plugins."io.containerd.grpc.v1.cri".containerd]
    discard_unpacked_layers = false
  # This is just to make sure that images are not shared between namespaces.
  [plugins."io.containerd.metadata.v1.bolt"]
    content_sharing_policy = "isolated"
nodes:
  - role: control-plane
    labels:
      spegel: schedule
  - role: worker
    labels:
      spegel: schedule
  - role: worker
    labels:
      spegel: schedule
      test: true
  - role: worker
    labels:
      spegel: schedule
      test: true
  - role: worker
    labels:
      spegel: schedule
      test: true`, proxyMode, ipFamily)

	path := filepath.Join(t.TempDir(), "kind-config.yaml")
	err := os.WriteFile(path, []byte(kindConfig), 0o644)
	require.NoError(t, err)
	return path
}

func commandWithError(ctx context.Context, t *testing.T, e string) (string, error) {
	t.Helper()

	cmd := exec.CommandContext(ctx, "bash", "-c", e)
	stdout := bytes.NewBuffer(nil)
	cmd.Stdout = stdout
	stderr := bytes.NewBuffer(nil)
	cmd.Stderr = stderr
	err := cmd.Run()
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(stdout.String(), "\n"), nil
}

func command(ctx context.Context, t *testing.T, e string) string {
	t.Helper()

	cmd := exec.CommandContext(ctx, "bash", "-c", e)
	stdout := bytes.NewBuffer(nil)
	cmd.Stdout = stdout
	stderr := bytes.NewBuffer(nil)
	cmd.Stderr = stderr
	err := cmd.Run()
	require.NoError(t, err, "command: %s\nstderr: %s", e, stderr.String())
	return strings.TrimSuffix(stdout.String(), "\n")
}
