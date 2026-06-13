package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/modfile"

	"github.com/fluxcd/cli-utils/pkg/kstatus/status"
	"github.com/go-openapi/testify/v2/assert"
	"github.com/go-openapi/testify/v2/require"
	"github.com/moby/moby/client"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/kubectl/pkg/scheme"
	"sigs.k8s.io/kind/pkg/apis/config/v1alpha4"
	"sigs.k8s.io/kind/pkg/cluster"
	kindnodes "sigs.k8s.io/kind/pkg/cluster/nodes"
	"sigs.k8s.io/kind/pkg/cluster/nodeutils"

	"github.com/kvick-org/pkg/errgroup"

	"github.com/spegel-org/spegel/pkg/oci"
	spegelregistry "github.com/spegel-org/spegel/pkg/registry"
	"github.com/spegel-org/spegel/pkg/web"
)

var (
	kubernetesVersions = []string{
		"v1.36.2",
		"v1.35.6",
		"v1.34.9",
	}
)

const (
	spegelNamespace      = "spegel"
	conformanceNamespace = "conformance"
	pullTestNamespace    = "pull-test"
	nodeTaintKey         = "spegel.dev/enabled"
)

func TestKubernetes(t *testing.T) {
	testStrategy := os.Getenv("INTEGRATION_TEST_STRATEGY")
	require.NotEmpty(t, testStrategy)
	t.Log("Running tests with with strategy", testStrategy)

	imgRef := os.Getenv("IMG_REF")
	require.NotEmpty(t, imgRef)

	mobyClient, err := client.New(client.FromEnv)
	require.NoError(t, err)
	t.Cleanup(func() {
		mobyClient.Close()
	})

	b, err := os.ReadFile("go.mod")
	require.NoError(t, err)
	modFile, err := modfile.Parse("go.mod", b, nil)
	require.NoError(t, err)
	idx := slices.IndexFunc(modFile.Require, func(r *modfile.Require) bool {
		return r.Mod.Path == "sigs.k8s.io/kind"
	})
	require.GreaterT(t, idx, -1)
	kindVersion := modFile.Require[idx].Mod.Version

	imgPath := exportImage(t, mobyClient, imgRef)

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

	kubernetesImgs := []oci.Image{}
	pullGroup := errgroup.WithContext(t.Context())
	for _, kubernetesVersion := range kubernetesVersions {
		img, err := oci.NewImage("ghcr.io", "spegel-org/test-images/kind-node", fmt.Sprintf("%s-%s", kindVersion, kubernetesVersion), "")
		require.NoError(t, err)
		kubernetesImgs = append(kubernetesImgs, img)

		t.Log("Pulling Kubernetes image", img.String())
		pullGroup.Go(func(ctx context.Context) error {
			resp, err := mobyClient.ImagePull(ctx, img.String(), client.ImagePullOptions{})
			if err != nil {
				return err
			}
			err = resp.Wait(ctx)
			if err != nil {
				return err
			}
			return nil
		})
	}
	err = pullGroup.Wait()
	require.NoError(t, err)

	type kubernetesTest struct {
		kubernetesImg oci.Image
		proxyMode     v1alpha4.ProxyMode
		ipFamily      v1alpha4.ClusterIPFamily
	}
	tests := []kubernetesTest{}
	for _, kubernetesImg := range kubernetesImgs {
		for _, proxyMode := range proxyModes {
			for _, ipFamily := range ipFamilies {
				tests = append(tests, kubernetesTest{
					kubernetesImg: kubernetesImg,
					proxyMode:     proxyMode,
					ipFamily:      ipFamily,
				})
			}
		}
	}
	for _, tt := range tests {
		name := strings.Join([]string{tt.kubernetesImg.Tag, string(tt.ipFamily), string(tt.proxyMode)}, "-")
		t.Run(name, func(t *testing.T) {
			containerdPatch := `version = 2
  [plugins."io.containerd.grpc.v1.cri".registry]
    config_path = "/etc/containerd/certs.d"
  [plugins."io.containerd.grpc.v1.cri".containerd]
    discard_unpacked_layers = false
  [plugins."io.containerd.metadata.v1.bolt"]
    content_sharing_policy = "isolated"`

			clusterCfg := &v1alpha4.Cluster{
				Networking: v1alpha4.Networking{
					KubeProxyMode: tt.proxyMode,
					IPFamily:      tt.ipFamily,
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
			kindName := fmt.Sprintf("spegel-e2e-%s", strings.ReplaceAll(name, ".", "-"))
			k8sClient, k8sDynClient, k8sCfg, kcPath, kindNodes := createKindCluster(t, kindName, tt.kubernetesImg, clusterCfg)

			testImages := []string{
				"ghcr.io/spegel-org/test-images/conformance:ed885fa",
				"docker.io/library/busybox:1.37.0",
				"ghcr.io/spegel-org/benchmark:v1-10MB-4",
				"ghcr.io/spegel-org/benchmark:v2-10MB-4@sha256:735223c59bb4df293176337f84f42b58ac53cb5a4740752b7aa56c19c0f6ec5b",
			}

			imageDigest := loadSpegelImage(t, kindNodes, imgPath, imgRef)

			actionCfg := newHelmActionConfig(t, kcPath)

			t.Cleanup(func() {
				if !t.Failed() {
					return
				}
				dumpPods(t, k8sClient, spegelNamespace, true)
			})

			t.Log("Upgrading Spegel from latest release to dev build")
			installSpegel(t, actionCfg, k8sClient, k8sDynClient, kindNodes, "")
			time.Sleep(3 * time.Second)
			installSpegel(t, actionCfg, k8sClient, k8sDynClient, kindNodes, imageDigest)
			uninstallSpegel(t, actionCfg, kindNodes)

			pullImages(t, kindNodes[0], testImages[:3])

			t.Log("Write existing certs.d configuration")
			hostsToml := `server = https://docker.io

[host.https://registry-1.docker.io]
  capabilities = [push]`
			err = nodeutils.WriteFile(kindNodes[0], "/etc/containerd/certs.d/docker.io/hosts.toml", hostsToml)
			require.NoError(t, err)

			installSpegel(t, actionCfg, k8sClient, k8sDynClient, kindNodes, imageDigest)

			pullImages(t, kindNodes[0], testImages[3:])

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
			require.EqualT(t, hostsToml, backupHostBuffer.String())
			err = kindNodes[0].CommandContext(t.Context(), "rm", "-rf", "/etc/containerd/certs.d/_backup").Run()
			require.NoError(t, err)
			err = kindNodes[0].CommandContext(t.Context(), "mkdir", "/etc/containerd/certs.d/_backup").Run()
			require.NoError(t, err)

			t.Log("Running conformance tests")
			runConformanceTests(t, k8sClient, kindNodes)

			t.Log("Checking peer ID persistence")
			initPodName, initPeerID := getSpegelPeerID(t, k8sClient, kindNodes[2])

			err = k8sClient.CoreV1().Pods(spegelNamespace).Delete(t.Context(), initPodName, metav1.DeleteOptions{})
			require.NoError(t, err)
			require.EventuallyWith(t, func(c *assert.CollectT) {
				_, err := k8sClient.CoreV1().Pods(spegelNamespace).Get(t.Context(), initPodName, metav1.GetOptions{})
				require.TrueT(c, kerrors.IsNotFound(err))
			}, 15*time.Second, 1*time.Second)
			gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}
			waitForStatus(t, k8sDynClient, gvr, spegelNamespace, spegelNamespace, status.CurrentStatus)

			_, newPeerID := getSpegelPeerID(t, k8sClient, kindNodes[2])
			require.EqualT(t, initPeerID, newPeerID)

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
				require.EqualT(t, int32(0), pod.Status.ContainerStatuses[0].State.Terminated.ExitCode)
				break
			}
			watcher.Stop()

			t.Log("Deploy pull test pods")
			runPullTests(t, k8sClient, k8sDynClient, k8sCfg, testImages[1:], kindNodes)
			noSpegelRestart(t, k8sClient)

			t.Log("Restarting Containerd")
			podList, err := k8sClient.CoreV1().Pods(spegelNamespace).List(t.Context(), metav1.ListOptions{FieldSelector: "spec.nodeName=" + kindNodes[0].String()})
			require.NoError(t, err)
			require.Len(t, podList.Items, 1)
			err = kindNodes[0].CommandContext(t.Context(), "systemctl", "restart", "containerd").Run()
			require.NoError(t, err)
			require.EventuallyWith(t, func(c *assert.CollectT) {
				pod, err := k8sClient.CoreV1().Pods(spegelNamespace).Get(t.Context(), podList.Items[0].Name, metav1.GetOptions{})
				require.NoError(c, err)
				require.Len(c, pod.Status.ContainerStatuses, 1)
				require.EqualT(c, int32(1), pod.Status.ContainerStatuses[0].RestartCount)
			}, 15*time.Second, 1*time.Second)

			t.Log("Scale down Spegel to single instance")
			node, err = k8sClient.CoreV1().Nodes().Get(t.Context(), kindNodes[1].String(), metav1.GetOptions{})
			require.NoError(t, err)
			node.ObjectMeta.Labels[nodeTaintKey] = "false"
			_, err = k8sClient.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{})
			require.NoError(t, err)
			podList, err = k8sClient.CoreV1().Pods(spegelNamespace).List(t.Context(), metav1.ListOptions{})
			require.NoError(t, err)
			require.Len(t, podList.Items, 2)
			err = k8sClient.CoreV1().Pods(spegelNamespace).DeleteCollection(t.Context(), metav1.DeleteOptions{}, metav1.ListOptions{})
			require.NoError(t, err)
			require.EventuallyWith(t, func(c *assert.CollectT) {
				for _, pod := range podList.Items {
					_, err := k8sClient.CoreV1().Pods(spegelNamespace).Get(t.Context(), pod.Name, metav1.GetOptions{})
					require.TrueT(c, kerrors.IsNotFound(err))
				}
			}, 5*time.Second, 1*time.Second)

			t.Log("Single instance is not ready but does not restart")
			for range 5 {
				podList, err = k8sClient.CoreV1().Pods(spegelNamespace).List(t.Context(), metav1.ListOptions{})
				require.NoError(t, err)
				require.Len(t, podList.Items, 1)
				gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
				waitForStatus(t, k8sDynClient, gvr, podList.Items[0].Namespace, podList.Items[0].Name, status.InProgressStatus)
				time.Sleep(1 * time.Second)
			}
			noSpegelRestart(t, k8sClient)

			uninstallSpegel(t, actionCfg, kindNodes)
		})
	}
}

func pullImages(t *testing.T, kindNode kindnodes.Node, images []string) {
	t.Helper()

	for _, image := range images {
		t.Logf("Pulling image %s", image)
		err := kindNode.CommandContext(t.Context(), "crictl", "pull", image).Run()
		require.NoError(t, err)
	}
}

func installSpegel(t *testing.T, actionCfg *action.Configuration, k8sClient kubernetes.Interface, k8sDynClient dynamic.Interface, kindNodes []kindnodes.Node, imageDigest string) {
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
	vals := map[string]any{
		"spegel": map[string]any{
			"logLevel":             "DEBUG",
			"mirrorResolveTimeout": "100ms",
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

	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}
	waitForStatus(t, k8sDynClient, gvr, spegelNamespace, spegelNamespace, status.CurrentStatus)

	podList, err := k8sClient.CoreV1().Pods(spegelNamespace).List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	require.EqualT(t, len(kindNodes), len(podList.Items))
}

func waitForStatus(t *testing.T, k8sDynClient dynamic.Interface, gvr schema.GroupVersionResource, namespace, name string, s status.Status) {
	t.Helper()

	require.EventuallyWith(t, func(c *assert.CollectT) {
		u, err := k8sDynClient.Resource(gvr).Namespace(namespace).Get(t.Context(), name, metav1.GetOptions{})
		require.NoError(c, err)
		require.NotNil(t, status.GetLegacyConditionsFn(u))
		res, err := status.Compute(u)
		require.NoError(c, err)
		require.EqualT(c, s, res.Status)
	}, 60*time.Second, 500*time.Millisecond)
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

func runPullTests(t *testing.T, k8sClient kubernetes.Interface, k8sDynClient dynamic.Interface, k8sCfg *restclient.Config, images []string, kindNodes []kindnodes.Node) {
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

		gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
		for _, readyPod := range readyPods {
			waitForStatus(t, k8sDynClient, gvr, pullTestNamespace, readyPod.Name, status.CurrentStatus)
		}
		require.EventuallyWith(t, func(c *assert.CollectT) {
			pod, err := k8sClient.CoreV1().Pods(pullTestNamespace).Get(t.Context(), failedPod.Name, metav1.GetOptions{})
			require.NoError(t, err)
			require.Len(c, pod.Status.ContainerStatuses, 1)
			waitingState := pod.Status.ContainerStatuses[0].State.Waiting
			require.NotNil(c, waitingState)
			require.EqualT(c, "ErrImagePull", waitingState.Reason)
		}, 10*time.Second, 500*time.Millisecond)

		podList, err := k8sClient.CoreV1().Pods(pullTestNamespace).List(t.Context(), metav1.ListOptions{})
		require.NoError(t, err)
		for _, pod := range podList.Items {
			require.NotEqualT(t, kindNodes[0].String(), pod.Spec.NodeName)
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
		require.ElementsMatchT(t, expected, files)
	})
	require.TrueT(t, succeeded, "pull test failed")
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
				BackoffLimit: new(int32(0)),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						RestartPolicy: corev1.RestartPolicyNever,
						Containers: []corev1.Container{
							{
								Name:  "conformance",
								Image: "ghcr.io/spegel-org/test-images/conformance:ed885fa",
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

		require.EventuallyWith(t, func(c *assert.CollectT) {
			job, err := k8sClient.BatchV1().Jobs(conformanceNamespace).Get(t.Context(), job.Name, metav1.GetOptions{})
			require.NoError(c, err)
			require.EqualT(c, int32(0), job.Status.Failed)
			require.EqualT(c, int32(1), job.Status.Succeeded)
		}, 15*time.Second, 1*time.Second)
	})
	require.TrueT(t, succeeded, "conformance test failed")
}

func noSpegelRestart(t *testing.T, k8sClient kubernetes.Interface) {
	t.Helper()

	podList, err := k8sClient.CoreV1().Pods(spegelNamespace).List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, podList.Items)
	for _, pod := range podList.Items {
		require.EqualT(t, int32(0), pod.Status.ContainerStatuses[0].RestartCount)
	}
}

// getSpegelPod returns the Spegel pod running on the given node.
func getSpegelPod(t *testing.T, k8sClient kubernetes.Interface, kindNode kindnodes.Node) corev1.Pod {
	t.Helper()

	podList, err := k8sClient.CoreV1().Pods(spegelNamespace).List(t.Context(), metav1.ListOptions{FieldSelector: "spec.nodeName=" + kindNode.String()})
	require.NoError(t, err)
	require.Len(t, podList.Items, 1)
	return podList.Items[0]
}

func getSpegelPeerID(t *testing.T, k8sClient kubernetes.Interface, kindNode kindnodes.Node) (string, string) {
	t.Helper()

	pod := getSpegelPod(t, k8sClient, kindNode)
	portIdx := slices.IndexFunc(pod.Spec.Containers[0].Ports, func(port corev1.ContainerPort) bool {
		return port.Name == "metrics"
	})
	require.PositiveT(t, portIdx)
	debugWebPort := pod.Spec.Containers[0].Ports[portIdx].ContainerPort
	podName := pod.Name

	b, err := k8sClient.CoreV1().RESTClient().Get().
		Namespace(spegelNamespace).
		Resource("pods").
		Name(fmt.Sprintf("%s:%d", podName, debugWebPort)).
		SubResource("proxy").
		Suffix("debug", "web", "metadata").
		DoRaw(t.Context())
	require.NoError(t, err)

	metadata := web.Metadata{}
	err = json.Unmarshal(b, &metadata)
	require.NoError(t, err)
	peerID := metadata.LibP2P.ID
	require.NotEmpty(t, peerID)
	return podName, peerID
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

const (
	// stargzVersion is the release of stargz-snapshotter installed on the lazy pulling node.
	stargzVersion = "0.18.2"
	// stargzNamespace is the test namespace for pods running lazily pulled images.
	stargzNamespace = "stargz-test"
	// estargzImageRef is the reference of the eStargz image seeded into the cluster.
	// The reference only exists within the cluster, guaranteeing that all of its
	// content is served by Spegel.
	estargzImageRef = "ghcr.io/spegel-org/test-estargz:v1"
	// estargzSourceImageRef is the image converted to eStargz to create the test image.
	estargzSourceImageRef = "docker.io/library/busybox:1.37.0"
)

// TestKubernetesStargz verifies that the stargz snapshotter can lazy pull images through
// Spegel. The eStargz image is seeded on one node through a conventional pull, meaning
// its content is fully present in the content store and advertised by Spegel. Another
// node, configured with the stargz snapshotter and with upstream registry access blocked,
// lazy pulls the image with all requests served by the seed node.
//
// Nodes which lazy pull do not write layer blobs to the content store. With the
// estargz backend enabled, the lazily pulled blobs are served from the chunk cache of
// the snapshotter once the background fetch has completed, turning the lazy pulling
// node into a seed. The test verifies the full cycle by removing the image from the
// original seed and pulling it back from the lazy pulling node.
func TestKubernetesStargz(t *testing.T) {
	testStrategy := os.Getenv("INTEGRATION_TEST_STRATEGY")
	require.NotEmpty(t, testStrategy)

	imgRef := os.Getenv("IMG_REF")
	require.NotEmpty(t, imgRef)

	mobyClient, err := client.New(client.FromEnv)
	require.NoError(t, err)
	t.Cleanup(func() {
		mobyClient.Close()
	})

	imgPath := exportImage(t, mobyClient, imgRef)

	kubernetesImg, err := oci.NewImage("ghcr.io", "spegel-org/test-images/kind-node", kubernetesVersions[0], "")
	require.NoError(t, err)
	t.Log("Pulling Kubernetes image", kubernetesImg.String())
	resp, err := mobyClient.ImagePull(t.Context(), kubernetesImg.String(), client.ImagePullOptions{})
	require.NoError(t, err)
	err = resp.Wait(t.Context())
	require.NoError(t, err)

	containerdPatch := `version = 2
  [plugins."io.containerd.grpc.v1.cri".registry]
    config_path = "/etc/containerd/certs.d"
  [plugins."io.containerd.grpc.v1.cri".containerd]
    discard_unpacked_layers = false`
	clusterCfg := &v1alpha4.Cluster{
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
	k8sClient, k8sDynClient, _, kcPath, kindNodes := createKindCluster(t, "spegel-e2e-stargz", kubernetesImg, clusterCfg)
	require.Len(t, kindNodes, 3)
	seedNode := kindNodes[1]
	lazyNode := kindNodes[2]

	imageDigest := loadSpegelImage(t, kindNodes, imgPath, imgRef)

	t.Log("Installing stargz snapshotter on the lazy pulling node")
	stargzArchive := downloadStargzSnapshotter(t)
	err = seedNode.CommandContext(t.Context(), "tar", "-xz", "-C", "/usr/local/bin", "ctr-remote").SetStdin(bytes.NewReader(stargzArchive)).Run()
	require.NoError(t, err)
	installStargzSnapshotter(t, k8sClient, lazyNode, stargzArchive)
	waitForNodesReady(t, k8sClient, kindNodes)

	// The image is seeded before Spegel is deployed so that it is part of the initial
	// advertisement, as content created by the conversion has no registry source and is
	// not advertised when created.
	t.Log("Seeding eStargz image through a conventional pull")
	pullImages(t, seedNode, []string{estargzSourceImageRef})
	err = seedNode.CommandContext(t.Context(), "ctr-remote", "-n=k8s.io", "image", "convert", "--estargz", "--oci", estargzSourceImageRef, estargzImageRef).Run()
	require.NoError(t, err)
	manifestDgst := getImageDigest(t, seedNode, estargzImageRef)
	layerDgsts := getManifestLayers(t, seedNode, manifestDgst)
	require.NotEmpty(t, layerDgsts)

	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		dumpPods(t, k8sClient, spegelNamespace, true)
	})

	t.Log("Deploying Spegel")
	actionCfg := newHelmActionConfig(t, kcPath)
	installSpegel(t, actionCfg, k8sClient, k8sDynClient, kindNodes, imageDigest)

	t.Log("Blocking upstream registry access on the lazy pulling node")
	blockRegistries(t, lazyNode)

	t.Log("Lazy pulling eStargz image through Spegel")
	pullImages(t, lazyNode, []string{estargzImageRef})

	t.Log("Checking that the image was lazy pulled")
	lazyContent := listContent(t, lazyNode)
	require.Contains(t, lazyContent, manifestDgst)
	for _, layerDgst := range layerDgsts {
		require.NotContains(t, lazyContent, layerDgst)
	}

	// The lazy pulling node has the manifest and config in its content store but not the
	// layers, so it advertises and serves only the content it can actually serve while
	// the seed serves the whole image.
	t.Log("Checking that the lazy pulling node only serves content it has")
	repoPath := strings.TrimPrefix(estargzImageRef, "ghcr.io/")
	repoPath = strings.Split(repoPath, ":")[0]
	require.EqualT(t, http.StatusOK, getContentStatus(t, k8sClient, seedNode, repoPath, oci.DistributionKindManifest, manifestDgst))
	require.EqualT(t, http.StatusOK, getContentStatus(t, k8sClient, lazyNode, repoPath, oci.DistributionKindManifest, manifestDgst))
	for _, layerDgst := range layerDgsts {
		require.EqualT(t, http.StatusOK, getContentStatus(t, k8sClient, seedNode, repoPath, oci.DistributionKindBlob, layerDgst))
		require.EqualT(t, http.StatusNotFound, getContentStatus(t, k8sClient, lazyNode, repoPath, oci.DistributionKindBlob, layerDgst))
	}
}

// blockRegistries blocks access to the upstream registries on the node, guaranteeing
// that everything pulled afterwards is served from within the cluster.
func blockRegistries(t *testing.T, node kindnodes.Node) {
	t.Helper()

	for _, domain := range []string{"ghcr.io", "docker.io", "registry-1.docker.io", "production.cloudflare.docker.com"} {
		err := node.CommandContext(t.Context(), "sh", "-c", fmt.Sprintf(`echo 0.0.0.0 %s >>/etc/hosts`, domain)).Run()
		require.NoError(t, err)
	}
}

func waitForNodesReady(t *testing.T, k8sClient kubernetes.Interface, kindNodes []kindnodes.Node) {
	t.Helper()

	require.EventuallyWith(t, func(c *assert.CollectT) {
		for _, kindNode := range kindNodes {
			node, err := k8sClient.CoreV1().Nodes().Get(t.Context(), kindNode.String(), metav1.GetOptions{})
			require.NoError(c, err)
			idx := slices.IndexFunc(node.Status.Conditions, func(cond corev1.NodeCondition) bool {
				return cond.Type == corev1.NodeReady
			})
			require.GreaterT(c, idx, -1)
			require.EqualT(c, corev1.ConditionTrue, node.Status.Conditions[idx].Status)
		}
	}, 60*time.Second, 1*time.Second)
}

// exportImage saves the image from the local Docker daemon to a file.
func exportImage(t *testing.T, mobyClient *client.Client, imgRef string) string {
	t.Helper()

	t.Log("Exporting Spegel image", imgRef)
	saveRes, err := mobyClient.ImageSave(t.Context(), []string{imgRef})
	require.NoError(t, err)
	imgPath := filepath.Join(t.TempDir(), "image")
	f, err := os.OpenFile(imgPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	require.NoError(t, err)
	_, err = io.Copy(f, saveRes)
	require.NoError(t, err)
	err = f.Close()
	require.NoError(t, err)
	return imgPath
}

// createKindCluster creates a Kind cluster and waits for every node to be ready. The
// returned nodes are sorted with the control plane first.
func createKindCluster(t *testing.T, kindName string, nodeImg oci.Image, clusterCfg *v1alpha4.Cluster) (kubernetes.Interface, dynamic.Interface, *restclient.Config, string, []kindnodes.Node) {
	t.Helper()

	t.Log("Creating Kind cluster")
	kcPath := filepath.Join(t.TempDir(), "kind.kubeconfig")
	provider := cluster.NewProvider()
	createOpts := []cluster.CreateOption{
		cluster.CreateWithNodeImage(nodeImg.String()),
		cluster.CreateWithV1Alpha4Config(clusterCfg),
		cluster.CreateWithKubeconfigPath(kcPath),
	}
	err := provider.Create(kindName, createOpts...)
	require.NoError(t, err)
	t.Cleanup(func() {
		if t.Failed() {
			return
		}
		err = provider.Delete(kindName, "")
		require.NoError(t, err)
	})

	k8sCfg, err := clientcmd.BuildConfigFromFlags("", kcPath)
	require.NoError(t, err)
	k8sClient, err := kubernetes.NewForConfig(k8sCfg)
	require.NoError(t, err)
	k8sDynClient, err := dynamic.NewForConfig(k8sCfg)
	require.NoError(t, err)

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
	waitForNodesReady(t, k8sClient, kindNodes)
	return k8sClient, k8sDynClient, k8sCfg, kcPath, kindNodes
}

// loadSpegelImage loads the image archive into every node and returns the image digest.
func loadSpegelImage(t *testing.T, kindNodes []kindnodes.Node, imgPath, imgRef string) string {
	t.Helper()

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
			imageDigest = getImageDigest(t, node, imgRef)
		}
		err = node.CommandContext(t.Context(), "ctr", "-n=k8s.io", "image", "tag", imgRef, fmt.Sprintf("ghcr.io/spegel-org/spegel@%s", imageDigest)).Run()
		require.NoError(t, err)
	}
	return imageDigest
}

// downloadStargzSnapshotter downloads the stargz snapshotter release archive for the
// architecture of the test host, which matches the architecture of the Kind nodes.
func downloadStargzSnapshotter(t *testing.T) []byte {
	t.Helper()

	arch := goruntime.GOARCH
	url := fmt.Sprintf("https://github.com/containerd/stargz-snapshotter/releases/download/v%s/stargz-snapshotter-v%s-linux-%s.tar.gz", stargzVersion, stargzVersion, arch)
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.EqualT(t, http.StatusOK, resp.StatusCode)
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return b
}

// installStargzSnapshotter installs and starts the stargz snapshotter and configures
// containerd to use it as the default snapshotter for CRI.
func installStargzSnapshotter(t *testing.T, k8sClient kubernetes.Interface, node kindnodes.Node, archive []byte) {
	t.Helper()

	err := node.CommandContext(t.Context(), "tar", "-xz", "-C", "/usr/local/bin", "containerd-stargz-grpc", "ctr-remote").SetStdin(bytes.NewReader(archive)).Run()
	require.NoError(t, err)

	// The standalone stargz snapshotter does not read the containerd registry configuration.
	// Mirrors have to be configured in the resolver configuration of the snapshotter.
	k8sNode, err := k8sClient.CoreV1().Nodes().Get(t.Context(), node.String(), metav1.GetOptions{})
	require.NoError(t, err)
	nodeIP := getNodeIP(t, k8sNode)
	snapshotterConfig := fmt.Sprintf(`[[resolver.host."ghcr.io".mirrors]]
host = "%s:30020"
insecure = true
`, nodeIP)
	err = nodeutils.WriteFile(node, "/etc/containerd-stargz-grpc/config.toml", snapshotterConfig)
	require.NoError(t, err)

	systemdUnit := `[Unit]
Description=stargz snapshotter
After=network.target
Before=containerd.service

[Service]
Type=notify
Environment=HOME=/root
ExecStart=/usr/local/bin/containerd-stargz-grpc --log-level=debug --config=/etc/containerd-stargz-grpc/config.toml
Restart=always
RestartSec=1

[Install]
WantedBy=multi-user.target
`
	err = nodeutils.WriteFile(node, "/etc/systemd/system/stargz-snapshotter.service", systemdUnit)
	require.NoError(t, err)
	err = node.CommandContext(t.Context(), "systemctl", "daemon-reload").Run()
	require.NoError(t, err)
	err = node.CommandContext(t.Context(), "systemctl", "enable", "--now", "stargz-snapshotter").Run()
	require.NoError(t, err)

	err = node.CommandContext(t.Context(), "sed", "-i", `s/snapshotter = "overlayfs"/snapshotter = "stargz"\n  disable_snapshot_annotations = false/`, "/etc/containerd/config.toml").Run()
	require.NoError(t, err)
	proxyPlugin := `
[proxy_plugins."stargz"]
  type = "snapshot"
  address = "/run/containerd-stargz-grpc/containerd-stargz-grpc.sock"
`
	err = node.CommandContext(t.Context(), "sh", "-c", fmt.Sprintf("cat >>/etc/containerd/config.toml <<'EOF'%sEOF", proxyPlugin)).Run()
	require.NoError(t, err)
	err = node.CommandContext(t.Context(), "systemctl", "restart", "containerd").Run()
	require.NoError(t, err)
	// Spegel exits when Containerd is restarted, wait for the pod to be recreated
	// before waiting for the daemonset to become ready.
	time.Sleep(5 * time.Second)
}

func newHelmActionConfig(t *testing.T, kcPath string) *action.Configuration {
	t.Helper()

	regClient, err := registry.NewClient()
	require.NoError(t, err)
	actionCfg := &action.Configuration{
		RegistryClient: regClient,
	}
	actionCfg.SetLogger(slog.DiscardHandler)
	clientGetter := &genericclioptions.ConfigFlags{KubeConfig: &kcPath}
	err = actionCfg.Init(clientGetter, spegelNamespace, "secret")
	require.NoError(t, err)
	return actionCfg
}

// getImageDigest returns the digest of an image in the containerd store of the node.
func getImageDigest(t *testing.T, node kindnodes.Node, ref string) string {
	t.Helper()

	var buf bytes.Buffer
	err := node.CommandContext(t.Context(), "ctr", "-n=k8s.io", "image", "ls", "name=="+ref).SetStdout(&buf).Run()
	require.NoError(t, err)
	lines := strings.Split(buf.String(), "\n")
	require.GreaterOrEqualT(t, len(lines), 2)
	fields := strings.Fields(lines[1])
	require.GreaterOrEqualT(t, len(fields), 3)
	dgst, err := digest.Parse(fields[2])
	require.NoError(t, err)
	return dgst.String()
}

// getManifestLayers returns the layer digests referenced by the manifest.
// Indexes are resolved to the manifest of the first listed platform.
func getManifestLayers(t *testing.T, node kindnodes.Node, manifestDgst string) []string {
	t.Helper()

	var buf bytes.Buffer
	err := node.CommandContext(t.Context(), "ctr", "-n=k8s.io", "content", "get", manifestDgst).SetStdout(&buf).Run()
	require.NoError(t, err)
	index := ocispec.Index{}
	err = json.Unmarshal(buf.Bytes(), &index)
	require.NoError(t, err)
	if len(index.Manifests) > 0 {
		return getManifestLayers(t, node, index.Manifests[0].Digest.String())
	}
	manifest := ocispec.Manifest{}
	err = json.Unmarshal(buf.Bytes(), &manifest)
	require.NoError(t, err)
	layerDgsts := []string{}
	for _, layer := range manifest.Layers {
		layerDgsts = append(layerDgsts, layer.Digest.String())
	}
	return layerDgsts
}

// listContent returns all content digests in the containerd content store of the node.
func listContent(t *testing.T, node kindnodes.Node) string {
	t.Helper()

	var buf bytes.Buffer
	err := node.CommandContext(t.Context(), "ctr", "-n=k8s.io", "content", "ls", "-q").SetStdout(&buf).Run()
	require.NoError(t, err)
	return buf.String()
}

// getContentStatus requests content from the Spegel registry on the given node and
// returns the response status code. The mirrored header is set so that the request is
// only served with local content instead of being forwarded to other nodes.
func getContentStatus(t *testing.T, k8sClient kubernetes.Interface, node kindnodes.Node, repoPath string, kind oci.DistributionKind, dgst string) int {
	t.Helper()

	pod := getSpegelPod(t, k8sClient, node)
	result := k8sClient.CoreV1().RESTClient().Get().
		Namespace(spegelNamespace).
		Resource("pods").
		Name(fmt.Sprintf("%s:%d", pod.Name, 5000)).
		SubResource("proxy").
		Suffix("v2", repoPath, string(kind), dgst).
		SetHeader(spegelregistry.HeaderSpegelMirrored, "true").
		Do(t.Context())
	statusCode := 0
	result.StatusCode(&statusCode)
	return statusCode
}
