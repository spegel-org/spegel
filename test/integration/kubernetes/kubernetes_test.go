package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fluxcd/cli-utils/pkg/kstatus/status"
	"github.com/fluxcd/pkg/runtime/patch"

	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/chart/loader"
	"helm.sh/helm/v4/pkg/downloader"
	"helm.sh/helm/v4/pkg/getter"
	"helm.sh/helm/v4/pkg/kube"
	"helm.sh/helm/v4/pkg/registry"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/kubectl/pkg/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/kind/pkg/apis/config/v1alpha4"
	"sigs.k8s.io/kind/pkg/cluster"
	kindnodes "sigs.k8s.io/kind/pkg/cluster/nodes"
	"sigs.k8s.io/kind/pkg/cluster/nodeutils"
)

const (
	spegelNamespace      = "spegel"
	conformanceNamespace = "conformance"
	pullTestNamespace    = "pull-test"
	nodeTaintKey         = "spegel.dev/enabled"
	// debugWebPort is the container port for metrics/debug web (GET /debug/web/metadata).
	debugWebPort = 9090
)

func TestKubernetes(t *testing.T) {
	testStrategy := os.Getenv("INTEGRATION_TEST_STRATEGY")
	require.NotEmpty(t, testStrategy)
	t.Log("Running tests with with strategy", testStrategy)

	imgRef := os.Getenv("IMG_REF")
	require.NotEmpty(t, imgRef)
	t.Log("Using Spegel image", imgRef)
	mobyClient, err := client.New(client.FromEnv)
	require.NoError(t, err)
	t.Cleanup(func() {
		mobyClient.Close()
	})
	saveRes, err := mobyClient.ImageSave(t.Context(), []string{imgRef})
	require.NoError(t, err)
	imgPath := filepath.Join(t.TempDir(), "image")
	f, err := os.OpenFile(imgPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	require.NoError(t, err)
	_, err = io.Copy(f, saveRes)
	require.NoError(t, err)
	err = f.Close()
	require.NoError(t, err)

	kubernetesVersions := []string{
		"v1.35.0",
		"v1.34.3",
		"v1.33.7",
	}
	proxyModes := []v1alpha4.ProxyMode{
		v1alpha4.NFTablesProxyMode,
		v1alpha4.IPTablesProxyMode,
	}
	ipFamilies := []v1alpha4.ClusterIPFamily{
		v1alpha4.DualStackFamily,
		v1alpha4.IPv4Family,
		v1alpha4.IPv6Family,
	}
	switch testStrategy {
	case "all":
		break
	case "fast":
		kubernetesVersions = []string{kubernetesVersions[0]}
		proxyModes = []v1alpha4.ProxyMode{proxyModes[0]}
		ipFamilies = []v1alpha4.ClusterIPFamily{ipFamilies[0]}
	default:
		t.Fatal("unknown test strategy", testStrategy)
	}

	type kubernetesTest struct {
		kubernetesVersion string
		proxyMode         v1alpha4.ProxyMode
		ipFamily          v1alpha4.ClusterIPFamily
	}
	tests := []kubernetesTest{}
	for _, kubernetesVersion := range kubernetesVersions {
		for _, proxyMode := range proxyModes {
			for _, ipFamily := range ipFamilies {
				tests = append(tests, kubernetesTest{
					kubernetesVersion: kubernetesVersion,
					proxyMode:         proxyMode,
					ipFamily:          ipFamily,
				})
			}
		}
	}
	for _, tt := range tests {
		name := strings.Join([]string{tt.kubernetesVersion, string(tt.ipFamily), string(tt.proxyMode)}, "-")
		t.Run(name, func(t *testing.T) {
			t.Log("Creating Kind cluster")
			kcPath := filepath.Join(t.TempDir(), "kind.kubeconfig")
			provider := cluster.NewProvider()
			containerdPatch := `[plugins."io.containerd.grpc.v1.cri".registry]
    config_path = "/etc/containerd/certs.d"
  # Discarding unpacked layers causes them to be removed, which defeats the purpose of a local cache.
  # Aditioanlly nodes will report having layers which no long exist.
  # This is by default false in containerd.
  [plugins."io.containerd.grpc.v1.cri".containerd]
    discard_unpacked_layers = false
  # This is just to make sure that images are not shared between namespaces.
  [plugins."io.containerd.metadata.v1.bolt"]
    content_sharing_policy = "isolated"`
			clusterCfg := &v1alpha4.Cluster{
				Networking: v1alpha4.Networking{
					KubeProxyMode: tt.proxyMode,
					IPFamily:      tt.ipFamily,
				},
				FeatureGates: map[string]bool{
					"ImageVolume": true,
				},
				ContainerdConfigPatches: []string{containerdPatch},
				Nodes: []v1alpha4.Node{
					{
						Role: v1alpha4.ControlPlaneRole,
						Labels: map[string]string{
							nodeTaintKey: "true",
						},
					},
					{
						Role: v1alpha4.WorkerRole,
						Labels: map[string]string{
							nodeTaintKey: "true",
						},
					},
					{
						Role: v1alpha4.WorkerRole,
						Labels: map[string]string{
							nodeTaintKey: "true",
						},
					},
				},
			}
			createOpts := []cluster.CreateOption{
				cluster.CreateWithNodeImage(fmt.Sprintf("docker.io/kindest/node:%s", tt.kubernetesVersion)),
				cluster.CreateWithV1Alpha4Config(clusterCfg),
				cluster.CreateWithKubeconfigPath(kcPath),
			}
			kindName := fmt.Sprintf("spegel-e2e-%s", name)
			err := provider.Create(kindName, createOpts...)
			require.NoError(t, err)
			t.Cleanup(func() {
				if t.Failed() {
					return
				}
				err = provider.Delete(kindName, "")
				require.NoError(t, err)
			})

			kindNodes, err := provider.ListNodes(kindName)
			require.NoError(t, err)
			controlPlaneNodeName := kindName + "-control-plane"
			slices.SortStableFunc(kindNodes, func(a, b kindnodes.Node) int {
				if a.String() == controlPlaneNodeName {
					return -1
				}
				if b.String() == controlPlaneNodeName {
					return 1
				}
				return 0
			})

			k8sCfg, err := clientcmd.BuildConfigFromFlags("", kcPath)
			require.NoError(t, err)
			k8sClient, err := kubernetes.NewForConfig(k8sCfg)
			require.NoError(t, err)

			t.Log("Loading Spegel image into nodes")
			f, err := os.Open(imgPath)
			require.NoError(t, err)
			imageDigest := ""
			for _, node := range kindNodes {
				_, err = f.Seek(0, io.SeekStart)
				require.NoError(t, err)
				err = nodeutils.LoadImageArchive(node, f)
				require.NoError(t, err)
				if imageDigest == "" {
					var buf bytes.Buffer
					err = node.CommandContext(t.Context(), "ctr", "-n=k8s.io", "image", "ls", "name=="+imgRef).SetStdout(&buf).Run()
					require.NoError(t, err)
					line := strings.Split(buf.String(), "\n")[1]
					imageDigest = strings.Split(line, " ")[2]
					require.NotEmpty(t, imageDigest)
				}
				err = node.CommandContext(t.Context(), "ctr", "-n=k8s.io", "image", "tag", imgRef, fmt.Sprintf("ghcr.io/spegel-org/spegel@%s", imageDigest)).Run()
				require.NoError(t, err)
			}

			regClient, err := registry.NewClient()
			require.NoError(t, err)
			actionCfg := &action.Configuration{
				RegistryClient: regClient,
			}
			actionCfg.SetLogger(slog.DiscardHandler)
			clientGetter := &genericclioptions.ConfigFlags{KubeConfig: &kcPath}
			err = actionCfg.Init(clientGetter, spegelNamespace, "secret")
			require.NoError(t, err)

			t.Log("Upgrading Spegel from latest release to dev build")
			installSpegel(t, actionCfg, k8sClient, kindNodes, "")
			installSpegel(t, actionCfg, k8sClient, kindNodes, imageDigest)
			uninstallSpegel(t, actionCfg, kindNodes)

			t.Log("Pulling test images")
			images := []string{
				"ghcr.io/spegel-org/conformance:9d1b925",
				"docker.io/library/busybox:1.37.0",
				"ghcr.io/spegel-org/benchmark:v1-10MB-4",
				"ghcr.io/spegel-org/benchmark:v2-10MB-4@sha256:735223c59bb4df293176337f84f42b58ac53cb5a4740752b7aa56c19c0f6ec5b",
			}
			for _, image := range images[:3] {
				t.Logf("Pulling image %s", image)
				err = kindNodes[0].CommandContext(t.Context(), "crictl", "pull", image).Run()
				require.NoError(t, err)
			}

			t.Log("Write existing certs.d configuration")
			hostsToml := `server = https://docker.io

[host.https://registry-1.docker.io]
  capabilities = [push]`
			err = nodeutils.WriteFile(kindNodes[0], "/etc/containerd/certs.d/docker.io/hosts.toml", hostsToml)
			require.NoError(t, err)

			installSpegel(t, actionCfg, k8sClient, kindNodes, imageDigest)

			t.Log("Checking peer ID persistence")
			assertPeerIDPersistence(t, k8sClient)

			t.Logf("Pulling image %s", images[3])
			err = kindNodes[0].CommandContext(t.Context(), "crictl", "pull", images[3]).Run()
			require.NoError(t, err)

			t.Log("Block upstream registry access")
			for _, node := range kindNodes {
				for _, domain := range []string{"ghcr.io", "docker.io", "registry-1.docker.io"} {
					err = node.CommandContext(t.Context(), "sh", "-c", fmt.Sprintf(`echo 0.0.0.0 %s >>/etc/hosts`, domain)).Run()
					require.NoError(t, err)
				}
			}

			t.Log("Checking backup content")
			backupHostBuffer := bytes.NewBuffer(nil)
			err = kindNodes[0].CommandContext(t.Context(), "cat", "/etc/containerd/certs.d/_backup/docker.io/hosts.toml").SetStdout(backupHostBuffer).Run()
			require.NoError(t, err)
			require.Equal(t, hostsToml, backupHostBuffer.String())
			err = kindNodes[0].CommandContext(t.Context(), "rm", "-rf", "/etc/containerd/certs.d/_backup").Run()
			require.NoError(t, err)
			err = kindNodes[0].CommandContext(t.Context(), "mkdir", "/etc/containerd/certs.d/_backup").Run()
			require.NoError(t, err)

			t.Log("Running conformance tests")
			runConformanceTests(t, k8sClient, kindNodes)

			t.Log("Remove Spegel from a node")
			watcher, err := k8sClient.CoreV1().Pods(spegelNamespace).Watch(t.Context(), metav1.ListOptions{FieldSelector: "spec.nodeName=" + kindNodes[2].String()})
			require.NoError(t, err)
			node, err := k8sClient.CoreV1().Nodes().Get(t.Context(), kindNodes[2].String(), metav1.GetOptions{})
			require.NoError(t, err)
			node.ObjectMeta.Labels[nodeTaintKey] = "false"
			_, err = k8sClient.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{})
			require.NoError(t, err)
			for event := range watcher.ResultChan() {
				pod, ok := event.Object.(*corev1.Pod)
				if !ok {
					continue
				}
				if !(pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed) {
					continue
				}
				// Ensure Spegel exist cleanly.
				require.Equal(t, int32(0), pod.Status.ContainerStatuses[0].State.Terminated.ExitCode)
				break
			}
			watcher.Stop()

			// Give time for system to balance. Without this tests tend to be flaky.
			time.Sleep(10 * time.Second)

			t.Log("Checking node port accessibility")
			tests := []struct {
				node     kindnodes.Node
				port     string
				expected string
			}{
				{
					node:     kindNodes[0],
					port:     "30020",
					expected: "200",
				},
				{
					node:     kindNodes[0],
					port:     "30021",
					expected: "200",
				},
				{
					node:     kindNodes[2],
					port:     "30020",
					expected: "000",
				},
				{
					node:     kindNodes[2],
					port:     "30021",
					expected: "200",
				},
			}
			for _, tt := range tests {
				node, err := k8sClient.CoreV1().Nodes().Get(t.Context(), tt.node.String(), metav1.GetOptions{})
				require.NoError(t, err)
				nodeIP := getNodeIP(t, node)
				buf := &bytes.Buffer{}
				tt.node.CommandContext(t.Context(), "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", fmt.Sprintf("http://%s:%s/readyz", nodeIP, tt.port)).SetStdout(buf).Run()
				require.Equal(t, tt.expected, buf.String(), fmt.Sprintf("%s %s", tt.node, tt.port))
			}

			t.Log("Deploy pull test pods")
			runPullTests(t, k8sClient, k8sCfg, images[1:], kindNodes)
			noSpegelRestart(t, k8sClient)

			t.Log("Restarting Containerd")
			podList, err := k8sClient.CoreV1().Pods(spegelNamespace).List(t.Context(), metav1.ListOptions{FieldSelector: "spec.nodeName=" + kindNodes[0].String()})
			require.NoError(t, err)
			require.Len(t, podList.Items, 1)
			err = kindNodes[0].CommandContext(t.Context(), "systemctl", "restart", "containerd").Run()
			require.NoError(t, err)
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				pod, err := k8sClient.CoreV1().Pods(spegelNamespace).Get(t.Context(), podList.Items[0].Name, metav1.GetOptions{})
				require.NoError(c, err)
				require.Len(c, pod.Status.ContainerStatuses, 1)
				require.Equal(c, int32(0), pod.Status.ContainerStatuses[0].RestartCount)
			}, 5*time.Second, 1*time.Second)

			t.Log("Scale down Spegel to single instance")
			node, err = k8sClient.CoreV1().Nodes().Get(t.Context(), kindNodes[1].String(), metav1.GetOptions{})
			require.NoError(t, err)
			node.ObjectMeta.Labels[nodeTaintKey] = "false"
			_, err = k8sClient.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{})
			require.NoError(t, err)
			podList, err = k8sClient.CoreV1().Pods(spegelNamespace).List(t.Context(), metav1.ListOptions{})
			require.NoError(t, err)
			err = k8sClient.CoreV1().Pods(spegelNamespace).DeleteCollection(t.Context(), metav1.DeleteOptions{}, metav1.ListOptions{})
			require.NoError(t, err)
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				for _, pod := range podList.Items {
					_, err := k8sClient.CoreV1().Pods(spegelNamespace).Get(t.Context(), pod.Name, metav1.GetOptions{})
					require.True(c, kerrors.IsNotFound(err))
				}
			}, 5*time.Second, 1*time.Second)

			for range 5 {
				podList, err = k8sClient.CoreV1().Pods(spegelNamespace).List(t.Context(), metav1.ListOptions{})
				require.NoError(t, err)
				require.Len(t, podList.Items, 1)
				u, err := patch.ToUnstructured(&podList.Items[0])
				require.NoError(t, err)
				res, err := status.Compute(u)
				require.NoError(t, err)
				require.NotEqual(t, status.CurrentStatus, res.Status)

				time.Sleep(1 * time.Second)
			}
			noSpegelRestart(t, k8sClient)

			uninstallSpegel(t, actionCfg, kindNodes)
		})
	}
}

func installSpegel(t *testing.T, actionCfg *action.Configuration, k8sClient kubernetes.Interface, kindNodes []kindnodes.Node, imageDigest string) {
	t.Helper()

	chartPath, version := func() (string, string) {
		if imageDigest == "" {
			tags, err := actionCfg.RegistryClient.Tags("ghcr.io/spegel-org/helm-charts/spegel")
			require.NoError(t, err)
			buf := bytes.NewBuffer(nil)
			dl := downloader.ChartDownloader{
				Out:            buf,
				Verify:         downloader.VerifyIfPossible,
				ContentCache:   t.TempDir(),
				Getters:        getter.Getters(getter.WithRegistryClient(actionCfg.RegistryClient)),
				RegistryClient: actionCfg.RegistryClient,
			}
			chartPath, _, err := dl.DownloadTo("oci://ghcr.io/spegel-org/helm-charts/spegel", tags[0], t.TempDir())
			require.NoError(t, err, buf.String())
			return chartPath, tags[0]
		}
		return "../../../charts/spegel/", "dev"
	}()
	charter, err := loader.Load(chartPath)
	require.NoError(t, err)

	t.Log("Deploying Spegel", version)
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		dumpPods(t, k8sClient, spegelNamespace, true)
	})
	vals := map[string]any{
		"spegel": map[string]any{
			"logLevel": "DEBUG",
		},
		"nodeSelector": map[string]any{
			nodeTaintKey: "true",
		},
	}
	if imageDigest != "" {
		vals["image"] = map[string]any{
			"pullPolicy": "Never",
			"digest":     imageDigest,
		}
	}
	_, err = action.NewGet(actionCfg).Run(spegelNamespace)
	if err != nil {
		install := action.NewInstall(actionCfg)
		install.ReleaseName = spegelNamespace
		install.Namespace = spegelNamespace
		install.CreateNamespace = true
		install.WaitStrategy = kube.StatusWatcherStrategy
		install.Timeout = 60 * time.Second
		_, err = install.RunWithContext(t.Context(), charter, vals)
		require.NoError(t, err)
	} else {
		upgrade := action.NewUpgrade(actionCfg)
		upgrade.Namespace = spegelNamespace
		upgrade.WaitStrategy = kube.StatusWatcherStrategy
		upgrade.Timeout = 60 * time.Second
		_, err := upgrade.RunWithContext(t.Context(), spegelNamespace, charter, vals)
		require.NoError(t, err)
	}

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		ds, err := k8sClient.AppsV1().DaemonSets(spegelNamespace).Get(t.Context(), spegelNamespace, metav1.GetOptions{})
		require.NoError(c, err)
		u, err := patch.ToUnstructured(ds)
		require.NoError(c, err)
		res, err := status.Compute(u)
		require.NoError(c, err)
		require.Equal(c, status.CurrentStatus, res.Status)
	}, 10*time.Second, 1*time.Second)
	podList, err := k8sClient.CoreV1().Pods(spegelNamespace).List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Equal(t, len(kindNodes), len(podList.Items))
}

func uninstallSpegel(t *testing.T, actionCfg *action.Configuration, kindNodes []kindnodes.Node) {
	t.Helper()

	t.Log("Uninstalling Spegel")
	uninstall := action.NewUninstall(actionCfg)
	uninstall.WaitStrategy = kube.StatusWatcherStrategy
	uninstall.Timeout = 60 * time.Second
	_, err := uninstall.Run(spegelNamespace)
	require.NoError(t, err)

	t.Log("Verify Spegel cleaned up host configuration")
	for _, node := range kindNodes {
		buf := &bytes.Buffer{}
		err = node.CommandContext(t.Context(), "ls", "/etc/containerd/certs.d").SetStdout(buf).Run()
		require.NoError(t, err)
		require.Empty(t, buf.String())
	}
}

func runPullTests(t *testing.T, k8sClient kubernetes.Interface, k8sCfg *restclient.Config, images []string, kindNodes []kindnodes.Node) {
	succeeded := t.Run("Pull Tests", func(t *testing.T) {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: pullTestNamespace,
			},
		}
		ns, err := k8sClient.CoreV1().Namespaces().Create(t.Context(), ns, metav1.CreateOptions{})
		require.NoError(t, err)

		ociPod := corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "oci",
				Namespace: pullTestNamespace,
			},
			Spec: corev1.PodSpec{
				NodeName: kindNodes[1].String(),
				Containers: []corev1.Container{
					{
						Name:            "pull-test",
						Image:           images[0],
						ImagePullPolicy: corev1.PullAlways,
						Command: []string{
							"sh",
							"-c",
							"sleep infinity",
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "oci-volume",
								MountPath: "/oci-volume",
								ReadOnly:  true,
							},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: "oci-volume",
						VolumeSource: corev1.VolumeSource{
							Image: &corev1.ImageVolumeSource{
								Reference:  images[1],
								PullPolicy: corev1.PullAlways,
							},
						},
					},
				},
			},
		}
		digestPod := corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "digest",
				Namespace: pullTestNamespace,
			},
			Spec: corev1.PodSpec{
				NodeName: kindNodes[2].String(),
				Containers: []corev1.Container{
					{
						Name:            "pull-test",
						Image:           images[2],
						ImagePullPolicy: corev1.PullAlways,
					},
				},
			},
		}
		readyPods := []corev1.Pod{
			ociPod,
			digestPod,
		}
		failedPod := corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "failed",
				Namespace: pullTestNamespace,
			},
			Spec: corev1.PodSpec{
				NodeName: kindNodes[1].String(),
				Containers: []corev1.Container{
					{
						Name:            "pull-test",
						Image:           "ghcr.io/spegel-org/benchmark:v1-10MB-1",
						ImagePullPolicy: corev1.PullAlways,
					},
				},
			},
		}
		for _, pod := range append(readyPods, failedPod) {
			_, err := k8sClient.CoreV1().Pods(pullTestNamespace).Create(t.Context(), &pod, metav1.CreateOptions{})
			require.NoError(t, err)
		}

		t.Cleanup(func() {
			if !t.Failed() {
				return
			}
			dumpPods(t, k8sClient, pullTestNamespace, false)
		})

		require.EventuallyWithT(t, func(c *assert.CollectT) {
			for _, readyPod := range readyPods {
				pod, err := k8sClient.CoreV1().Pods(pullTestNamespace).Get(t.Context(), readyPod.Name, metav1.GetOptions{})
				require.NoError(c, err)
				u, err := patch.ToUnstructured(pod)
				require.NoError(c, err)
				res, err := status.Compute(u)
				require.NoError(c, err)
				require.Equal(c, status.CurrentStatus, res.Status)
			}
		}, 30*time.Second, 1*time.Second)
		for range 5 {
			pod, err := k8sClient.CoreV1().Pods(pullTestNamespace).Get(t.Context(), failedPod.Name, metav1.GetOptions{})
			require.NoError(t, err)
			u, err := patch.ToUnstructured(pod)
			require.NoError(t, err)
			res, err := status.Compute(u)
			require.NoError(t, err)
			require.NotEqual(t, status.CurrentStatus, res.Status)

			time.Sleep(1 * time.Second)
		}

		podList, err := k8sClient.CoreV1().Pods(pullTestNamespace).List(t.Context(), metav1.ListOptions{})
		require.NoError(t, err)
		for _, pod := range podList.Items {
			require.NotEqual(t, kindNodes[0].String(), pod.Spec.NodeName)
		}

		// Check OCI volume content.
		command := "ls -1A /oci-volume"
		req := k8sClient.CoreV1().RESTClient().Post().
			Namespace(pullTestNamespace).
			Resource("pods").
			Name(ociPod.Name).
			SubResource("exec").
			VersionedParams(&corev1.PodExecOptions{
				Command: []string{"/bin/sh", "-c", command},
				Stdin:   false,
				Stdout:  true,
				Stderr:  false,
				TTY:     false,
			}, runtime.NewParameterCodec(scheme.Scheme))
		exec, err := remotecommand.NewSPDYExecutor(k8sCfg, "POST", req.URL())
		require.NoError(t, err)

		var stdout bytes.Buffer
		err = exec.StreamWithContext(t.Context(), remotecommand.StreamOptions{Stdout: &stdout})
		require.NoError(t, err)
		files := strings.Split(strings.TrimSpace(stdout.String()), "\n")
		expected := []string{
			"pause",
			"random_file_2259168264799515459.txt",
			"random_file_495671701297603781.txt",
			"random_file_7526869637736667835.txt",
			"random_file_8163815451001128425.txt",
		}
		require.ElementsMatch(t, expected, files)
	})
	require.True(t, succeeded, "pull test failed")
}

func runConformanceTests(t *testing.T, k8sClient kubernetes.Interface, kindNodes []kindnodes.Node) {
	succeeded := t.Run("Conformance Tests", func(t *testing.T) {
		// We want to make sure the requests go to remaining worker Spegel instance.
		podList, err := k8sClient.CoreV1().Pods(spegelNamespace).List(t.Context(), metav1.ListOptions{FieldSelector: "spec.nodeName=" + kindNodes[1].String()})
		require.NoError(t, err)
		require.Len(t, podList.Items, 1)

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: conformanceNamespace,
			},
		}
		ns, err = k8sClient.CoreV1().Namespaces().Create(t.Context(), ns, metav1.CreateOptions{})
		require.NoError(t, err)

		podIP := getPodIP(t, &podList.Items[0])
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "conformance",
				Namespace: ns.Name,
			},
			Spec: batchv1.JobSpec{
				BackoffLimit: ptr.To(int32(0)),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						RestartPolicy: corev1.RestartPolicyNever,
						Containers: []corev1.Container{
							{
								Name:  "conformance",
								Image: "ghcr.io/spegel-org/conformance:9d1b925",
								Env: []corev1.EnvVar{
									{
										Name:  "OCI_TEST_PULL",
										Value: "1",
									},
									{
										Name:  "OCI_ROOT_URL",
										Value: fmt.Sprintf("http://%s:5000", podIP),
									},
									{
										Name:  "OCI_MIRROR_URL",
										Value: "ghcr.io",
									},
									{
										Name:  "OCI_NAMESPACE",
										Value: "spegel-org/benchmark",
									},
									{
										Name:  "OCI_TAG_NAME",
										Value: "v1-10MB-4",
									},
									{
										Name:  "OCI_MANIFEST_DIGEST",
										Value: "sha256:7eeb6e8677d65452dbb5bd824a23d40b3753d26a69279db7dccb9dd426b192b8",
									},
									{
										Name:  "OCI_BLOB_DIGEST",
										Value: "sha256:7582c2cc65ef30105b84c1c6812f71c8012663c6352b01fe2f483238313ab0ed",
									},
								},
							},
						},
					},
				},
			},
		}
		job, err = k8sClient.BatchV1().Jobs(ns.Name).Create(t.Context(), job, metav1.CreateOptions{})
		require.NoError(t, err)

		t.Cleanup(func() {
			if !t.Failed() {
				return
			}
			dumpPods(t, k8sClient, conformanceNamespace, true)
		})

		require.EventuallyWithT(t, func(c *assert.CollectT) {
			job, err := k8sClient.BatchV1().Jobs(conformanceNamespace).Get(t.Context(), job.Name, metav1.GetOptions{})
			require.NoError(c, err)
			require.Equal(c, int32(0), job.Status.Failed)
			require.Equal(c, int32(1), job.Status.Succeeded)
		}, 15*time.Second, 1*time.Second)
	})
	require.True(t, succeeded, "conformance test failed")
}

func noSpegelRestart(t *testing.T, k8sClient kubernetes.Interface) {
	t.Helper()

	podList, err := k8sClient.CoreV1().Pods(spegelNamespace).List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, podList.Items)
	for _, pod := range podList.Items {
		require.Equal(t, int32(0), pod.Status.ContainerStatuses[0].RestartCount)
	}
}

func assertPeerIDPersistence(t *testing.T, k8sClient kubernetes.Interface) {
	t.Helper()

	pod := getSpegelPod(t, k8sClient)
	peerID := getSpegelPeerID(t, k8sClient, pod.Name)

	err := k8sClient.CoreV1().Pods(spegelNamespace).Delete(t.Context(), pod.Name, metav1.DeleteOptions{})
	require.NoError(t, err)

	newPod := waitForNewSpegelPod(t, k8sClient, pod.Spec.NodeName, pod.UID)
	require.NotEmpty(t, newPod.Name)
	newPeerID := getSpegelPeerID(t, k8sClient, newPod.Name)
	require.Equal(t, peerID, newPeerID)
}

func getSpegelPod(t *testing.T, k8sClient kubernetes.Interface) corev1.Pod {
	t.Helper()

	podList, err := k8sClient.CoreV1().Pods(spegelNamespace).List(t.Context(), metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/component=spegel",
	})
	require.NoError(t, err)
	require.NotEmpty(t, podList.Items)
	return podList.Items[0]
}

func waitForNewSpegelPod(t *testing.T, k8sClient kubernetes.Interface, nodeName string, oldUID types.UID) corev1.Pod {
	t.Helper()

	require.NotEmpty(t, nodeName)
	var newPod corev1.Pod
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		found := false
		podList, err := k8sClient.CoreV1().Pods(spegelNamespace).List(t.Context(), metav1.ListOptions{
			FieldSelector: "spec.nodeName=" + nodeName,
			LabelSelector: "app.kubernetes.io/component=spegel",
		})
		require.NoError(c, err)
		for _, pod := range podList.Items {
			if pod.UID == oldUID {
				continue
			}
			u, err := patch.ToUnstructured(&pod)
			require.NoError(c, err)
			res, err := status.Compute(u)
			require.NoError(c, err)
			if res.Status != status.CurrentStatus {
				continue
			}
			newPod = pod
			found = true
			break
		}
		require.True(c, found, "new spegel pod not ready yet")
	}, 60*time.Second, 2*time.Second)

	return newPod
}

func getSpegelPeerID(t *testing.T, k8sClient kubernetes.Interface, podName string) string {
	t.Helper()

	require.NotEmpty(t, podName)
	var peerID string
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		b, err := k8sClient.CoreV1().RESTClient().Get().
			Namespace(spegelNamespace).
			Resource("pods").
			Name(fmt.Sprintf("%s:%d", podName, debugWebPort)).
			SubResource("proxy").
			Suffix("debug", "web", "metadata").
			DoRaw(t.Context())
		require.NoError(c, err)

		var meta struct {
			LibP2P struct {
				ID string `json:"id"`
			} `json:"libp2p"`
		}
		err = json.Unmarshal(b, &meta)
		require.NoError(c, err)
		peerID = meta.LibP2P.ID
		require.NotEmpty(c, peerID)
	}, 10*time.Second, 1*time.Second)

	return peerID
}

func dumpPods(t *testing.T, k8sClient kubernetes.Interface, namespace string, includeLogs bool) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	podList, err := k8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	output := []string{fmt.Sprintf("Pods in namespace %q:", namespace)}
	for _, pod := range podList.Items {
		restartCount := 0
		readyCount := 0
		for _, cs := range pod.Status.ContainerStatuses {
			restartCount += int(cs.RestartCount)
			if cs.Ready {
				readyCount += 1
			}
		}
		node, err := k8sClient.CoreV1().Nodes().Get(ctx, pod.Spec.NodeName, metav1.GetOptions{})
		require.NoError(t, err)
		hostIP := ""
		for _, nodeAddress := range node.Status.Addresses {
			if nodeAddress.Type != corev1.NodeInternalIP {
				continue
			}
			hostIP = nodeAddress.Address
			break
		}
		output = append(output, fmt.Sprintf("%s %s (%s %d/%d %d) -> %s %s", pod.Name, pod.Status.PodIP, pod.Status.Phase, readyCount, len(pod.Status.ContainerStatuses), restartCount, node.Name, hostIP))
		if includeLogs {
			logs, err := k8sClient.CoreV1().Pods(namespace).GetLogs(pod.Name, &corev1.PodLogOptions{}).Stream(ctx)
			require.NoError(t, err)
			b, err := io.ReadAll(logs)
			require.NoError(t, err)
			output = append(output, string(b))
		}
	}
	t.Log("\n" + strings.Join(output, "\n") + "\n")
}

func getNodeIP(t *testing.T, node *corev1.Node) string {
	t.Helper()

	for _, a := range node.Status.Addresses {
		if a.Type != corev1.NodeInternalIP {
			continue
		}
		return getIP6SafeString(t, a.Address)
	}
	require.FailNow(t, "node ip not found")
	return ""
}

func getPodIP(t *testing.T, pod *corev1.Pod) string {
	t.Helper()

	require.NotEmpty(t, pod.Status.PodIPs)
	return getIP6SafeString(t, pod.Status.PodIPs[0].IP)
}

func getIP6SafeString(t *testing.T, s string) string {
	t.Helper()

	addr, err := netip.ParseAddr(s)
	require.NoError(t, err)
	if addr.Is6() {
		return fmt.Sprintf("[%s]", addr.String())
	}
	return addr.String()
}
