package host

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-openapi/testify/v2/assert"
	"github.com/go-openapi/testify/v2/require"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

const (
	defaultTestImageTag = "spegel-host-test:dev"
	pulledImageRef      = "ghcr.io/spegel-org/benchmark:v2-10MB-4@sha256:735223c59bb4df293176337f84f42b58ac53cb5a4740752b7aa56c19c0f6ec5b"
	pulledImageRegistry = "ghcr.io"
	systemReadyTimeout  = 30 * time.Second
	spegelReadyTimeout  = 30 * time.Second
	portsTimeout        = 30 * time.Second
)

type node struct {
	name           string
	containerID    string
	ip             string
	peer           *node
	metricsURL     string
	containerdSock string
}

type metricSnapshot struct {
	mirrorHits   map[string]uint64
	mirrorMisses map[string]uint64
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func preflight(t *testing.T) *client.Client {
	t.Helper()

	strategy := os.Getenv("INTEGRATION_TEST_STRATEGY")
	if strategy == "" {
		t.Skip("INTEGRATION_TEST_STRATEGY not set; invoke via `make test-integration-host`")
	}
	switch strategy {
	case "fast", "all":
	default:
		t.Fatalf("unknown test strategy %q", strategy)
	}

	if runtime.GOOS != "linux" {
		t.Skip("host integration test requires Linux (cgroup v2 + systemd-in-container)")
	}

	mobyClient, err := client.New(client.FromEnv)
	if err != nil {
		t.Skipf("docker daemon not reachable: %v", err)
	}
	pingCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := mobyClient.Ping(pingCtx, client.PingOptions{}); err != nil {
		t.Skipf("docker daemon not reachable: %v", err)
	}

	if !cgroupV2() {
		t.Skip("cgroup v2 unified hierarchy required")
	}

	if err := privilegedProbe(t.Context(), mobyClient); err != nil {
		t.Skipf("cannot run privileged containers (need sudo or docker group membership): %v", err)
	}

	return mobyClient
}

func cgroupV2() bool {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "cgroup2" && fields[1] == "/sys/fs/cgroup" && fields[2] == "cgroup2" {
			return true
		}
	}
	return false
}

func privilegedProbe(ctx context.Context, mobyClient *client.Client) error {
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	_, err := mobyClient.ImageInspect(probeCtx, "debian:bookworm-slim")
	if err != nil {
		rc, pullErr := mobyClient.ImagePull(probeCtx, "debian:bookworm-slim", client.ImagePullOptions{})
		if pullErr != nil {
			return fmt.Errorf("pull probe image: %w", pullErr)
		}
		_, _ = io.Copy(io.Discard, rc)
		_ = rc.Close()
	}

	createResp, err := mobyClient.ContainerCreate(probeCtx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: "debian:bookworm-slim",
			Cmd:   []string{"true"},
		},
		HostConfig: &container.HostConfig{
			Privileged: true,
			AutoRemove: true,
		},
	})
	if err != nil {
		return fmt.Errorf("create privileged probe: %w", err)
	}
	if _, err := mobyClient.ContainerStart(probeCtx, createResp.ID, client.ContainerStartOptions{}); err != nil {
		_, _ = mobyClient.ContainerRemove(probeCtx, createResp.ID, client.ContainerRemoveOptions{Force: true})
		return fmt.Errorf("start privileged probe: %w", err)
	}
	waitResult := mobyClient.ContainerWait(probeCtx, createResp.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case <-waitResult.Result:
	case err := <-waitResult.Error:
		return fmt.Errorf("wait privileged probe: %w", err)
	case <-probeCtx.Done():
		return probeCtx.Err()
	}
	return nil
}

func locateSpegelBinary(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("SPEGEL_BINARY"); p != "" {
		require.FileExists(t, p, "SPEGEL_BINARY does not exist: %s", p)
		return p
	}
	candidates := []string{
		"../../../dist/spegel_linux_amd64/spegel",
		"../../../dist/spegel_linux_arm64/spegel",
	}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if _, err := os.Stat(abs); err == nil {
			return abs
		}
	}
	t.Skip("spegel binary not found under ./dist; run `make build` first")
	return ""
}

func ensureTestImage(t *testing.T, mobyClient *client.Client) string {
	t.Helper()
	tag := os.Getenv("SPEGEL_HOST_TEST_IMAGE")
	if tag == "" {
		tag = defaultTestImageTag
	}

	if _, err := mobyClient.ImageInspect(t.Context(), tag); err == nil {
		return tag
	}

	wd, err := os.Getwd()
	require.NoError(t, err)
	ctxTar, err := buildContext(wd)
	require.NoError(t, err, "build context tar")

	resp, err := mobyClient.ImageBuild(t.Context(), ctxTar, client.ImageBuildOptions{
		Tags:        []string{tag},
		Remove:      true,
		ForceRemove: true,
		Dockerfile:  "Dockerfile",
	})
	require.NoError(t, err, "image build")
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err, "read image build output")
	if _, err := mobyClient.ImageInspect(t.Context(), tag); err != nil {
		t.Fatalf("image %s not present after build: %v\n%s", tag, err, string(out))
	}
	return tag
}

func buildContext(dir string) (io.Reader, error) {
	files := []string{"Dockerfile", "spegel.service", "containerd-config.toml"}
	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)
	for _, name := range files {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		hdr := &tar.Header{
			Name:    name,
			Mode:    0o644,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(tw, f); err != nil {
			f.Close()
			return nil, err
		}
		f.Close()
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf, nil
}

func startNode(t *testing.T, mobyClient *client.Client, name, imageTag, spegelBinaryPath string) *node {
	t.Helper()

	hostConfig := &container.HostConfig{
		Privileged:  true,
		CgroupnsMode: container.CgroupnsModeHost,
		Tmpfs: map[string]string{
			"/run":      "",
			"/run/lock": "",
		},
		Mounts: []mount.Mount{
			{Type: mount.TypeBind, Source: "/sys/fs/cgroup", Target: "/sys/fs/cgroup", ReadOnly: false},
			{Type: mount.TypeBind, Source: spegelBinaryPath, Target: "/usr/local/bin/spegel", ReadOnly: true},
		},
	}

	cfg := &container.Config{
		Image:      imageTag,
		Tty:        false,
		StopSignal: "SIGRTMIN+3",
	}

	createResp, err := mobyClient.ContainerCreate(t.Context(), client.ContainerCreateOptions{
		Config:     cfg,
		HostConfig: hostConfig,
		Name:       name,
	})
	require.NoError(t, err, "container create %s", name)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if t.Failed() {
			collectFailureLogs(t, mobyClient, name, createResp.ID)
		}
		_, _ = mobyClient.ContainerRemove(ctx, createResp.ID, client.ContainerRemoveOptions{Force: true})
	})

	_, err = mobyClient.ContainerStart(t.Context(), createResp.ID, client.ContainerStartOptions{})
	require.NoError(t, err, "container start %s", name)

	waitForSystemReady(t, mobyClient, createResp.ID)

	inspect, err := mobyClient.ContainerInspect(t.Context(), createResp.ID, client.ContainerInspectOptions{})
	require.NoError(t, err, "container inspect %s", name)
	var ip string
	for _, ep := range inspect.Container.NetworkSettings.Networks {
		if ep != nil && ep.IPAddress.IsValid() {
			ip = ep.IPAddress.String()
			break
		}
	}
	require.NotEmpty(t, ip, "container %s has no IP", name)

	return &node{
		name:           name,
		containerID:    createResp.ID,
		ip:             ip,
		metricsURL:     fmt.Sprintf("http://%s:9090/metrics", ip),
		containerdSock: "/run/containerd/containerd.sock",
	}
}

func waitForSystemReady(t *testing.T, mobyClient *client.Client, containerID string) {
	t.Helper()
	deadline := time.Now().Add(systemReadyTimeout)
	var lastOut string
	for time.Now().Before(deadline) {
		out, _, err := dockerExec(t.Context(), mobyClient, containerID, []string{"systemctl", "is-system-running"})
		state := strings.TrimSpace(out)
		lastOut = state
		if err == nil && (state == "running" || state == "degraded") {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("systemd did not become ready within %s (last state %q)", systemReadyTimeout, lastOut)
}

func waitForUnitActive(t *testing.T, mobyClient *client.Client, containerID, unit string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastOut string
	for time.Now().Before(deadline) {
		out, _, err := dockerExec(t.Context(), mobyClient, containerID, []string{"systemctl", "is-active", unit})
		state := strings.TrimSpace(out)
		lastOut = state
		if err == nil && state == "active" {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("unit %s did not become active within %s (last state %q)", unit, timeout, lastOut)
}

func writePeerEnv(t *testing.T, mobyClient *client.Client, n *node) {
	t.Helper()
	require.NotNil(t, n.peer, "peer not wired for %s", n.name)
	content := fmt.Sprintf("SPEGEL_HTTP_BOOTSTRAP_PEER=http://%s:5002\n", n.peer.ip)
	cmd := []string{"sh", "-c", fmt.Sprintf("mkdir -p /etc/spegel && cat >/etc/spegel/peer.env <<'EOF'\n%sEOF", content)}
	out, code, err := dockerExec(t.Context(), mobyClient, n.containerID, cmd)
	require.NoError(t, err, "write peer.env on %s: %s", n.name, out)
	require.Equal(t, 0, code, "write peer.env on %s exited %d: %s", n.name, code, out)

	out, code, err = dockerExec(t.Context(), mobyClient, n.containerID, []string{"systemctl", "restart", "spegel.service"})
	require.NoError(t, err, "restart spegel on %s: %s", n.name, out)
	require.Equal(t, 0, code, "restart spegel on %s exited %d: %s", n.name, code, out)

	waitForUnitActive(t, mobyClient, n.containerID, "spegel.service", spegelReadyTimeout)
}

func configureContainerd(t *testing.T, mobyClient *client.Client, n *node) {
	t.Helper()
	cmd := []string{
		"/usr/local/bin/spegel", "configuration",
		"--mirror-targets", "http://127.0.0.1:5000",
		"--mirrored-registries", "https://ghcr.io", "https://registry.k8s.io", "https://docker.io",
	}
	out, code, err := dockerExec(t.Context(), mobyClient, n.containerID, cmd)
	require.NoError(t, err, "spegel configuration on %s: %s", n.name, out)
	require.Equal(t, 0, code, "spegel configuration on %s exited %d: %s", n.name, code, out)

	out, code, err = dockerExec(t.Context(), mobyClient, n.containerID, []string{"systemctl", "reload-or-restart", "containerd.service"})
	require.NoError(t, err, "reload containerd on %s: %s", n.name, out)
	require.Equal(t, 0, code, "reload containerd on %s exited %d: %s", n.name, code, out)
}

func assertPortsListening(t *testing.T, mobyClient *client.Client, n *node) {
	t.Helper()
	tcpPorts := []string{"5000", "5001", "5002", "9090"}
	udpPorts := []string{"5001"}

	deadline := time.Now().Add(portsTimeout)
	for {
		tcpOut, _, _ := dockerExec(t.Context(), mobyClient, n.containerID, []string{"ss", "-Hltn"})
		udpOut, _, _ := dockerExec(t.Context(), mobyClient, n.containerID, []string{"ss", "-Hlun"})
		missing := []string{}
		for _, p := range tcpPorts {
			if !portListening(tcpOut, p) {
				missing = append(missing, p+"/tcp")
			}
		}
		for _, p := range udpPorts {
			if !portListening(udpOut, p) {
				missing = append(missing, p+"/udp")
			}
		}
		if len(missing) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("ports not listening on %s: %v\ntcp:\n%s\nudp:\n%s", n.name, missing, tcpOut, udpOut)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func portListening(ssOutput, port string) bool {
	for _, line := range strings.Split(ssOutput, "\n") {
		if strings.Contains(line, ":"+port+" ") || strings.HasSuffix(strings.TrimSpace(line), ":"+port) {
			return true
		}
	}
	return false
}

func scrapeMetrics(t *testing.T, n *node) metricSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, n.metricsURL, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err, "scrape metrics on %s", n.name)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "metrics on %s status", n.name)

	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(resp.Body)
	require.NoError(t, err, "parse metrics on %s", n.name)

	snap := metricSnapshot{
		mirrorHits:   map[string]uint64{},
		mirrorMisses: map[string]uint64{},
	}
	mf, ok := families["spegel_mirror_requests_total"]
	if !ok {
		return snap
	}
	for _, m := range mf.Metric {
		var registry, cache string
		for _, lp := range m.Label {
			switch lp.GetName() {
			case "registry":
				registry = lp.GetValue()
			case "cache":
				cache = lp.GetValue()
			}
		}
		if m.Counter == nil {
			continue
		}
		v := uint64(m.Counter.GetValue())
		switch cache {
		case "hit":
			snap.mirrorHits[registry] = v
		case "miss":
			snap.mirrorMisses[registry] = v
		}
	}
	return snap
}

func ctrPull(t *testing.T, mobyClient *client.Client, n *node, ref string) {
	t.Helper()
	cmd := []string{"crictl", "pull", ref}
	out, code, err := dockerExec(t.Context(), mobyClient, n.containerID, cmd)
	require.NoError(t, err, "crictl pull on %s: %s", n.name, out)
	require.Equal(t, 0, code, "crictl pull on %s exited %d: %s", n.name, code, out)
}

func dockerExec(ctx context.Context, mobyClient *client.Client, containerID string, cmd []string) (string, int, error) {
	execResp, err := mobyClient.ExecCreate(ctx, containerID, client.ExecCreateOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return "", -1, fmt.Errorf("exec create: %w", err)
	}
	att, err := mobyClient.ExecAttach(ctx, execResp.ID, client.ExecAttachOptions{})
	if err != nil {
		return "", -1, fmt.Errorf("exec attach: %w", err)
	}
	defer att.Close()
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, att.Reader); err != nil && !errors.Is(err, io.EOF) {
		return stdout.String() + stderr.String(), -1, fmt.Errorf("exec stdcopy: %w", err)
	}
	out := stdout.String() + stderr.String()
	inspect, err := mobyClient.ExecInspect(ctx, execResp.ID, client.ExecInspectOptions{})
	if err != nil {
		return out, -1, fmt.Errorf("exec inspect: %w", err)
	}
	if inspect.Running {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			inspect, err = mobyClient.ExecInspect(ctx, execResp.ID, client.ExecInspectOptions{})
			if err != nil {
				return out, -1, err
			}
			if !inspect.Running {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	if inspect.ExitCode != 0 {
		return out, inspect.ExitCode, fmt.Errorf("exit %d", inspect.ExitCode)
	}
	return out, inspect.ExitCode, nil
}

func collectFailureLogs(t *testing.T, mobyClient *client.Client, name, containerID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rc, err := mobyClient.ContainerLogs(ctx, containerID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: "all"})
	if err == nil {
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		t.Logf("=== docker logs %s ===\n%s", name, string(b))
	}
	for _, unit := range []string{"spegel.service", "containerd.service"} {
		out, _, _ := dockerExec(ctx, mobyClient, containerID, []string{"journalctl", "-u", unit, "--no-pager"})
		t.Logf("=== journalctl -u %s on %s ===\n%s", unit, name, out)
	}
	out, _, _ := dockerExec(ctx, mobyClient, containerID, []string{"sh", "-c", "find /etc/containerd/certs.d -type f -exec sh -c 'echo === {} ===; cat {}' \\;"})
	t.Logf("=== hosts.toml on %s ===\n%s", name, out)
	out, _, _ = dockerExec(ctx, mobyClient, containerID, []string{"sh", "-c", "curl -sf http://127.0.0.1:9090/metrics | grep -E 'mirror|spegel' || true"})
	t.Logf("=== spegel metrics on %s ===\n%s", name, out)
}

func TestHostBinaryPeerToPeer(t *testing.T) {
	mobyClient := preflight(t)
	t.Cleanup(func() { _ = mobyClient.Close() })

	spegelBinary := locateSpegelBinary(t)
	imageTag := ensureTestImage(t, mobyClient)

	// Drop any pre-existing test containers.
	for _, name := range []string{"spegel-host-node-a", "spegel-host-node-b"} {
		_, _ = mobyClient.ContainerRemove(t.Context(), name, client.ContainerRemoveOptions{Force: true})
	}

	nodeA := startNode(t, mobyClient, "spegel-host-node-a", imageTag, spegelBinary)
	nodeB := startNode(t, mobyClient, "spegel-host-node-b", imageTag, spegelBinary)
	nodeA.peer = nodeB
	nodeB.peer = nodeA

	writePeerEnv(t, mobyClient, nodeA)
	writePeerEnv(t, mobyClient, nodeB)

	configureContainerd(t, mobyClient, nodeA)
	configureContainerd(t, mobyClient, nodeB)

	assertPortsListening(t, mobyClient, nodeA)
	assertPortsListening(t, mobyClient, nodeB)

	beforeAPull := scrapeMetrics(t, nodeA)
	ctrPull(t, mobyClient, nodeA, pulledImageRef)
	afterAPull := scrapeMetrics(t, nodeA)
	assert.Equal(t, beforeAPull.mirrorHits[pulledImageRegistry], afterAPull.mirrorHits[pulledImageRegistry],
		"node A's hit counter should not change when node A pulls (warming cache only)")

	// Node B's mirror_requests_total{cache="hit"} increasing proves B's containerd
	// went through B's spegel and B's spegel found the content on a peer — node A
	// is the only peer, so the pull was served by A.
	beforeBPull := scrapeMetrics(t, nodeB)
	ctrPull(t, mobyClient, nodeB, pulledImageRef)
	require.Eventually(t, func() bool {
		after := scrapeMetrics(t, nodeB)
		return after.mirrorHits[pulledImageRegistry] >= beforeBPull.mirrorHits[pulledImageRegistry]+1
	}, 30*time.Second, 500*time.Millisecond,
		"node B's mirror hit counter for %s should increase by >=1 after node B pulls", pulledImageRegistry)
}

// silence unused-import warnings if a sub-feature is dropped.
var _ = errors.New
