package benchmark

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
)

func TestBenchmark(t *testing.T) {
	kubeconfigPath := os.Getenv("BENCHMARK_KUBECONFIG")
	require.NotEmpty(t, kubeconfigPath)
	image := os.Getenv("BENCHMARK_IMAGE")
	require.NotEmpty(t, image)
	concurrencyStr := os.Getenv("BENCHMARK_CONCURRENCY")
	require.NotEmpty(t, concurrencyStr)
	concurrency, err := strconv.Atoi(concurrencyStr)
	require.NoError(t, err)
	err = run(kubeconfigPath, image, concurrency)
	require.NoError(t, err)
}

type job struct {
	iteration int
	name      string
	namespace string
}

type result struct {
	iteration int
	duration  float64
}

func run(kubeconfigPath string, image string, concurrency int) error {
	ctx := context.TODO()

	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}

	ns := "spegel-benchmark"
	err = createBenchmarkPods(ctx, cs, ns)
	if err != nil {
		return fmt.Errorf("could not create benchmark pods: %w", err)
	}

	podList, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=spegel-benchmark"})
	if err != nil {
		return err
	}
	for _, pod := range podList.Items {
		steps := []string{
			fmt.Sprintf("/usr/local/bin/crictl rmi %s || true", image),
		}
		_, err := execute(ctx, cs, cfg, pod.Name, pod.Namespace, steps)
		if err != nil {
			return err
		}
	}
	g, ctx := errgroup.WithContext(ctx)
	jobs := make(chan job, len(podList.Items))
	results := make(chan result, len(podList.Items))
	for w := 0; w < concurrency; w++ {
		g.Go(func() error {
			for job := range jobs {
				steps := []string{
					"export TIMEFORMAT=%R",
					fmt.Sprintf("time /usr/local/bin/crictl pull %s > /dev/null", image),
				}
				out, err := execute(ctx, cs, cfg, job.name, job.namespace, steps)
				if err != nil {
					return err
				}
				out = strings.ReplaceAll(out, "\n", "")
				out = strings.ReplaceAll(out, "\r", "")
				duration, err := strconv.ParseFloat(out, 64)
				if err != nil {
					return err
				}
				results <- result{iteration: job.iteration, duration: duration}
			}
			return nil
		})
	}
	for i, pod := range podList.Items {
		jobs <- job{iteration: i, name: pod.Name, namespace: pod.Namespace}
	}
	close(jobs)
	err = g.Wait()
	if err != nil {
		return err
	}

	file, err := os.Create(fmt.Sprintf("/tmp/spegel-benchmark-%s-%d.csv", strings.ReplaceAll(image, "/", "_"), concurrency))
	if err != nil {
		return err
	}
	w := csv.NewWriter(file)
	err = w.Write([]string{"Iteration", "Duration"})
	if err != nil {
		return err
	}
	for i := 0; i < len(podList.Items); i++ {
		res := <-results
		err = w.Write([]string{fmt.Sprintf("%d", res.iteration), fmt.Sprintf("%v", res.duration)})
		if err != nil {
			return err
		}
	}
	w.Flush()
	err = w.Error()
	if err != nil {
		return err
	}

	return nil
}

func createBenchmarkPods(ctx context.Context, cs kubernetes.Interface, namespace string) error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}
	_, err := cs.CoreV1().Namespaces().Get(ctx, ns.ObjectMeta.Name, metav1.GetOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	if errors.IsNotFound(err) {
		ns, err = cs.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
		if err != nil {
			return err
		}
	}
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "spegel-benchmark",
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "spegel-benchmark"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": "spegel-benchmark",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:            "benchmark",
							Image:           "docker.io/library/alpine:3.18.4@sha256:48d9183eb12a05c99bcc0bf44a003607b8e941e1d4f41f9ad12bdcc4b5672f86",
							ImagePullPolicy: "IfNotPresent",
							Stdin:           true,
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "host-root",
									MountPath: "/host",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "host-root",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/",
								},
							},
						},
					},
				},
			},
		},
	}
	_, err = cs.AppsV1().DaemonSets(ns.ObjectMeta.Name).Get(ctx, ds.ObjectMeta.Name, metav1.GetOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	if errors.IsNotFound(err) {
		ds, err = cs.AppsV1().DaemonSets(ns.ObjectMeta.Name).Create(ctx, ds, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		err = wait.PollUntilContextTimeout(ctx, 1*time.Second, 1*time.Minute, true, func(ctx context.Context) (done bool, err error) {
			ds, err := cs.AppsV1().DaemonSets(ns.ObjectMeta.Name).Get(ctx, ds.ObjectMeta.Name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			if ds.Status.CurrentNumberScheduled == ds.Status.DesiredNumberScheduled {
				return true, nil
			}
			return false, nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func execute(ctx context.Context, cs kubernetes.Interface, cfg *restclient.Config, name, namespace string, steps []string) (string, error) {
	command := fmt.Sprintf("chroot /host /bin/bash -c '%s'", strings.Join(steps, ";"))
	buf := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	request := cs.CoreV1().RESTClient().
		Post().
		Namespace(namespace).
		Resource("pods").
		Name(name).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command: []string{"/bin/sh", "-c", command},
			Stdin:   false,
			Stdout:  true,
			Stderr:  true,
			TTY:     true,
		}, scheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(cfg, "POST", request.URL())
	if err != nil {
		return "", err
	}
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: buf,
		Stderr: errBuf,
	})
	if err != nil {
		return "", err
	}
	if errBuf.String() != "" {
		return "", fmt.Errorf(errBuf.String())
	}
	return buf.String(), nil
}
