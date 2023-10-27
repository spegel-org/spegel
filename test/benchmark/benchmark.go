package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path"
	"regexp"
	"strings"
	"syscall"
	"time"

	"image/color"

	"github.com/alexflint/go-arg"
	"golang.org/x/exp/slices"
	"gonum.org/v1/plot"
	"gonum.org/v1/plot/plotter"
	"gonum.org/v1/plot/vg"
	"gonum.org/v1/plot/vg/draw"
	"gonum.org/v1/plot/vg/vgimg"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/cli-utils/pkg/kstatus/status"
)

type BenchmarkCmd struct {
	ResultDir      string   `arg:"--result-dir,required"`
	Name           string   `arg:"--name,required"`
	KubeconfigPath string   `arg:"--kubeconfig,required"`
	Namespace      string   `arg:"--namespace,required"`
	Images         []string `arg:"--images,required"`
}

type AnalyzeCmd struct {
	Path string `args:"--path"`
}

type Arguments struct {
	Benchmark *BenchmarkCmd `arg:"subcommand:benchmark"`
	Analyze   *AnalyzeCmd   `arg:"subcommand:analyze"`
}

func main() {
	args := &Arguments{}
	arg.MustParse(args)
	err := run(*args)
	if err != nil {
		fmt.Println("unexpected error:", err)
		os.Exit(1)
	}
}

func run(args Arguments) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer cancel()
	switch {
	case args.Benchmark != nil:
		return benchmark(ctx, *args.Benchmark)
	case args.Analyze != nil:
		return analyze(ctx, *args.Analyze)
	default:
		return fmt.Errorf("unknown command")
	}
}

type Result struct {
	Name       string
	Benchmarks []Benchmark
}

type Benchmark struct {
	Image        string
	Measurements []Measurement
}

type Measurement struct {
	Start    time.Time
	Stop     time.Time
	Duration time.Duration
}

func benchmark(ctx context.Context, args BenchmarkCmd) error {
	cfg, err := clientcmd.BuildConfigFromFlags("", args.KubeconfigPath)
	if err != nil {
		return err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}

	ts := time.Now().Unix()
	runName := fmt.Sprintf("spegel-benchmark-%d", ts)
	_, err = cs.CoreV1().Namespaces().Get(ctx, args.Namespace, metav1.GetOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	if errors.IsNotFound(err) {
		ns := corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: args.Namespace,
			},
		}
		_, err := cs.CoreV1().Namespaces().Create(ctx, &ns, metav1.CreateOptions{})
		if err != nil {
			return err
		}
	}

	err = clearImages(ctx, cs, dc, args.Namespace, args.Images)
	if err != nil {
		return err
	}
	defer func() {
		cs.AppsV1().DaemonSets(args.Namespace).Delete(ctx, runName, metav1.DeleteOptions{})
	}()
	result := Result{
		Name: args.Name,
	}
	for _, image := range args.Images {
		bench, err := measureImagePull(ctx, cs, dc, args.Namespace, runName, image)
		if err != nil {
			return err
		}
		result.Benchmarks = append(result.Benchmarks, bench)
	}
	err = clearImages(ctx, cs, dc, args.Namespace, args.Images)
	if err != nil {
		return err
	}

	fileName := fmt.Sprintf("%s.json", args.Name)
	file, err := os.Create(path.Join(args.ResultDir, fileName))
	if err != nil {
		return err
	}
	defer file.Close()
	b, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	_, err = file.Write(b)
	if err != nil {
		return err
	}

	return nil
}

func clearImages(ctx context.Context, cs kubernetes.Interface, dc dynamic.Interface, namespace string, images []string) error {
	remove := fmt.Sprintf("crictl rmi %s || true", strings.Join(images, " "))
	commands := []string{"/bin/sh", "-c", fmt.Sprintf("chroot /host /bin/bash -c '%s'; sleep infinity;", remove)}
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "spegel-clear-image",
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "spegel-clear-image"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": "spegel-clear-image",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:            "clear",
							Image:           "docker.io/library/alpine:3.18.4@sha256:48d9183eb12a05c99bcc0bf44a003607b8e941e1d4f41f9ad12bdcc4b5672f86",
							ImagePullPolicy: "IfNotPresent",
							Command:         commands,
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
	_, err := cs.AppsV1().DaemonSets(namespace).Create(ctx, ds, metav1.CreateOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	defer func() {
		cs.AppsV1().DaemonSets(namespace).Delete(ctx, ds.ObjectMeta.Name, metav1.DeleteOptions{})
	}()
	err = wait.PollUntilContextTimeout(ctx, 1*time.Second, 10*time.Minute, true, func(ctx context.Context) (done bool, err error) {
		gvr := schema.GroupVersionResource{
			Group:    "apps",
			Version:  "v1",
			Resource: "daemonsets",
		}
		u, err := dc.Resource(gvr).Namespace(namespace).Get(ctx, ds.ObjectMeta.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		res, err := status.Compute(u)
		if err != nil {
			return false, err
		}
		if res.Status != status.CurrentStatus {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return err
	}
	return nil
}

func measureImagePull(ctx context.Context, cs kubernetes.Interface, dc dynamic.Interface, namespace, name, image string) (Benchmark, error) {
	ds, err := cs.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return Benchmark{}, err
	}
	if errors.IsNotFound(err) {
		ds := &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
			Spec: appsv1.DaemonSetSpec{
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": name},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": name,
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:            "benchmark",
								Image:           image,
								ImagePullPolicy: "IfNotPresent",
								// Keep container running
								Stdin: true,
							},
						},
					},
				},
			},
		}
		ds, err := cs.AppsV1().DaemonSets(namespace).Create(ctx, ds, metav1.CreateOptions{})
		if err != nil {
			return Benchmark{}, err
		}
	} else {
		ds.Spec.Template.Spec.Containers[0].Image = image
		_, err := cs.AppsV1().DaemonSets(namespace).Update(ctx, ds, metav1.UpdateOptions{})
		if err != nil {
			return Benchmark{}, err
		}
	}

	err = wait.PollUntilContextTimeout(ctx, 1*time.Second, 30*time.Minute, true, func(ctx context.Context) (done bool, err error) {
		gvr := schema.GroupVersionResource{
			Group:    "apps",
			Version:  "v1",
			Resource: "daemonsets",
		}
		u, err := dc.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		res, err := status.Compute(u)
		if err != nil {
			return false, err
		}
		if res.Status != status.CurrentStatus {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return Benchmark{}, err
	}

	podList, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: fmt.Sprintf("app=%s", name)})
	if err != nil {
		return Benchmark{}, err
	}
	if len(podList.Items) == 0 {
		return Benchmark{}, fmt.Errorf("received empty benchmark pod list")
	}
	bench := Benchmark{
		Image: image,
	}
	for _, pod := range podList.Items {
		eventList, _ := cs.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{FieldSelector: fmt.Sprintf("involvedObject.name=%s", pod.Name), TypeMeta: metav1.TypeMeta{Kind: "Pod"}})
		if err != nil {
			return Benchmark{}, err
		}
		pullingEvent, err := getEvent(eventList.Items, "Pulling")
		if err != nil {
			return Benchmark{}, err
		}
		pulledEvent, err := getEvent(eventList.Items, "Pulled")
		if err != nil {
			return Benchmark{}, err
		}
		d, err := parsePullMessage(pulledEvent.Message)
		if err != nil {
			return Benchmark{}, err
		}
		bench.Measurements = append(bench.Measurements, Measurement{Start: pullingEvent.FirstTimestamp.Time, Stop: pullingEvent.FirstTimestamp.Time.Add(d), Duration: d})
	}
	return bench, nil
}

func getEvent(events []corev1.Event, reason string) (corev1.Event, error) {
	for _, event := range events {
		if event.Reason != reason {
			continue
		}
		return event, nil
	}
	return corev1.Event{}, fmt.Errorf("could not find event with reason %s", reason)
}

func parsePullMessage(msg string) (time.Duration, error) {
	r, err := regexp.Compile(`\((.*) including waiting\)`)
	if err != nil {
		return 0, err
	}
	match := r.FindStringSubmatch(msg)
	if len(match) < 2 {
		return 0, fmt.Errorf("could not find image pull duration")
	}
	s := match[1]
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	return d, nil
}

func analyze(ctx context.Context, args AnalyzeCmd) error {
	b, err := os.ReadFile(args.Path)
	if err != nil {
		return err
	}
	result := Result{}
	err = json.Unmarshal(b, &result)
	if err != nil {
		return err
	}
	ext := path.Ext(args.Path)
	outPath := strings.TrimSuffix(args.Path, ext)
	outPath = fmt.Sprintf("%s.png", outPath)
	err = createPlot(result, outPath)
	if err != nil {
		return err
	}
	return nil
}

func createPlot(result Result, path string) error {
	plots := []*plot.Plot{}
	for _, bench := range result.Benchmarks {
		p := plot.New()
		p.Title.Text = bench.Image
		p.Title.Padding = vg.Points(10)
		p.Y.Label.Text = "Pod Number"
		p.X.Label.Text = "Time [ms]"
		slices.SortFunc(bench.Measurements, func(a, b Measurement) int {
			if a.Start == b.Start {
				return a.Stop.Compare(b.Stop)
			}
			return a.Start.Compare(b.Start)
		})
		zeroTime := bench.Measurements[0].Start
		max := int64(0)
		min := int64(0)
		for i, result := range bench.Measurements {
			if i == 0 || result.Duration.Milliseconds() < min {
				min = result.Duration.Milliseconds()
			}
			if i == 0 || result.Duration.Milliseconds() > max {
				max = result.Duration.Milliseconds()
			}
			start := result.Start.Sub(zeroTime)
			stop := start + result.Duration
			b, err := plotter.NewBoxPlot(4, float64(len(bench.Measurements)-i-1), plotter.Values{float64(start.Milliseconds()), float64(stop.Milliseconds())})
			if err != nil {
				return err
			}
			b.Horizontal = true
			b.FillColor = color.Black
			p.Add(b)
		}
		plots = append(plots, p)
	}

	img := vgimg.New(vg.Points(700), vg.Points(300))
	dc := draw.New(img)
	t := draw.Tiles{
		Rows:      1,
		Cols:      len(plots),
		PadX:      vg.Millimeter,
		PadY:      vg.Millimeter,
		PadTop:    vg.Points(10),
		PadBottom: vg.Points(10),
		PadLeft:   vg.Points(10),
		PadRight:  vg.Points(10),
	}
	canv := plot.Align([][]*plot.Plot{plots}, t, dc)
	for i, plot := range plots {
		plot.Draw(canv[0][i])
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	png := vgimg.PngCanvas{Canvas: img}
	if _, err := png.WriteTo(file); err != nil {
		return err
	}
	return nil
}
