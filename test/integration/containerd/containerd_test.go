package containerd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/moby/go-archive"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"

	"github.com/spegel-org/spegel/pkg/oci"
)

func TestContainerdPull(t *testing.T) {
	testStrategy := os.Getenv("INTEGRATION_TEST_STRATEGY")
	require.NotEmpty(t, testStrategy)
	t.Log("Running tests with with strategy", testStrategy)

	containerdVersions := []string{
		"2.2.0",
		"2.1.5",
		"2.0.7",
		"1.7.29",
	}
	switch testStrategy {
	case "all":
		break
	case "fast":
		containerdVersions = []string{
			containerdVersions[0],
			containerdVersions[3],
		}
	case "latest":
		containerdVersions = containerdVersions[:1]
	default:
		t.Fatal("unknown test strategy", testStrategy)
	}

	cli, err := client.New(client.FromEnv)
	require.NoError(t, err)
	for _, containerdVersion := range containerdVersions {
		t.Run(containerdVersion, func(t *testing.T) {
			t.Log("Building Containerd image")
			containerImage := fmt.Sprintf("ghcr.io/spegel-org/containerd:%s", containerdVersion)
			buildCtx, err := archive.TarWithOptions("./testdata/containerd-image", &archive.TarOptions{})
			require.NoError(t, err)
			targetarch := runtime.GOARCH
			buildOpts := client.ImageBuildOptions{
				PullParent: true,
				Tags:       []string{containerImage},
				Dockerfile: "Dockerfile",
				BuildArgs: map[string]*string{
					"TARGETARCH":         &targetarch,
					"CONTAINERD_VERSION": &containerdVersion,
				},
			}
			buildResp, err := cli.ImageBuild(t.Context(), buildCtx, buildOpts)
			require.NoError(t, err)
			_, err = cli.ImageLoad(t.Context(), buildResp.Body)
			require.NoError(t, err)
			err = buildResp.Body.Close()
			assert.NoError(t, err)

			t.Log("Running Containerd container")
			env := []string{
				fmt.Sprintf("USER_ID=%d", os.Getuid()),
				fmt.Sprintf("GROUP_ID=%d", os.Getgid()),
			}
			runPath := t.TempDir()
			createOpt := client.ContainerCreateOptions{
				Config: &container.Config{
					Image: containerImage,
					Tty:   false,
					Env:   env,
				},
				HostConfig: &container.HostConfig{
					Privileged: true,
					Mounts: []mount.Mount{
						{
							Type:   mount.TypeBind,
							Source: runPath,
							Target: "/run/containerd-sock",
						},
					},
				},
			}
			createResp, err := cli.ContainerCreate(t.Context(), createOpt)
			require.NoError(t, err)
			_, err = cli.ContainerStart(t.Context(), createResp.ID, client.ContainerStartOptions{})
			require.NoError(t, err)
			t.Cleanup(func() {
				cli.ContainerStop(context.Background(), createResp.ID, client.ContainerStopOptions{})
			})
			require.EventuallyWithT(t, func(collect *assert.CollectT) {
				require.FileExists(collect, filepath.Join(runPath, "ready"))
			}, 10*time.Second, 100*time.Millisecond)

			t.Log("Setting up Containerd store")
			socketPath := filepath.Join(runPath, "containerd.sock")

			connClient, err := grpc.NewClient("unix://"+socketPath, grpc.WithTransportCredentials(insecure.NewCredentials()))
			require.NoError(t, err)
			imageClient := runtimeapi.NewImageServiceClient(connClient)

			containerdStore, err := oci.NewContainerd(t.Context(), socketPath, "k8s.io")
			require.NoError(t, err)
			err = containerdStore.Verify(t.Context(), "/etc/containerd/certs.d")
			require.NoError(t, err)
			name := containerdStore.Name()
			require.Equal(t, "containerd", name)
			eventCh, err := containerdStore.Subscribe(t.Context())
			require.NoError(t, err)

			imgs := []string{
				"ghcr.io/spegel-org/benchmark:v2-10MB-4",
				"ghcr.io/spegel-org/benchmark:v2-10MB-4@sha256:735223c59bb4df293176337f84f42b58ac53cb5a4740752b7aa56c19c0f6ec5b",
				"ghcr.io/spegel-org/benchmark@sha256:735223c59bb4df293176337f84f42b58ac53cb5a4740752b7aa56c19c0f6ec5b",
			}
			for _, s := range imgs {
				benchmarkImg, err := oci.ParseImage(s, oci.AllowTagOnly())
				require.NoError(t, err)
				expectedDescs := []ocispec.Descriptor{
					{Digest: "sha256:735223c59bb4df293176337f84f42b58ac53cb5a4740752b7aa56c19c0f6ec5b", Size: 1371, MediaType: "application/vnd.docker.distribution.manifest.v2+json"},
					{Digest: "sha256:7582c2cc65ef30105b84c1c6812f71c8012663c6352b01fe2f483238313ab0ed", Size: 307023, MediaType: "application/octet-stream"},
					{Digest: "sha256:85bdfbf66d5c95e296fd1332d94e6a0ac86508af48fbd28b825db7c15b39cdad", Size: 1318, MediaType: "application/vnd.oci.image.config.v1+json"},
					{Digest: "sha256:99ea62d595b5a3e1d01639af2781f97730eca4086f5308be58f68b18c244adc9", Size: 2622396, MediaType: "application/octet-stream"},
					{Digest: "sha256:a3dbaff286eb1da0a03dd99d51cbeacb6f38f1dfd1ce04c267278d835fa64865", Size: 2622398, MediaType: "application/octet-stream"},
					{Digest: "sha256:d76a66ca5a6e5fdd3b4f5df356b7762572327f0d9c1dbf4d71d1116fbc623589", Size: 2622396, MediaType: "application/octet-stream"},
					{Digest: "sha256:df178cf0f2112519a5ff06bec070a33b2e2a968936466ccfec15b13f1a51ae86", Size: 2622395, MediaType: "application/octet-stream"},
				}
				expectedCreateEvents := []oci.OCIEvent{}
				expectedDeleteEvents := []oci.OCIEvent{}
				if benchmarkImg.Tag != "" && benchmarkImg.Digest == "" {
					expectedCreateEvents = append(expectedCreateEvents, oci.OCIEvent{Type: oci.CreateEvent, Reference: benchmarkImg.Reference})
					expectedDeleteEvents = append(expectedDeleteEvents, oci.OCIEvent{Type: oci.DeleteEvent, Reference: benchmarkImg.Reference})
				}
				for _, desc := range expectedDescs {
					ref := oci.Reference{
						Registry:   benchmarkImg.Registry,
						Repository: benchmarkImg.Repository,
						Digest:     desc.Digest,
					}
					expectedCreateEvents = append(expectedCreateEvents, oci.OCIEvent{Type: oci.CreateEvent, Reference: ref})
					expectedDeleteEvents = append(expectedDeleteEvents, oci.OCIEvent{Type: oci.DeleteEvent, Reference: ref})
				}

				t.Log("Pulling image with CRI", benchmarkImg.String())
				fmt.Println(benchmarkImg.String())
				_, err = imageClient.PullImage(t.Context(), &runtimeapi.PullImageRequest{Image: &runtimeapi.ImageSpec{Image: benchmarkImg.String()}})
				require.NoError(t, err)
				ensureEvents(t, eventCh, expectedCreateEvents)

				t.Log("Checking Containerd store")
				imgs, err := containerdStore.ListImages(t.Context())
				require.NoError(t, err)
				require.Len(t, imgs, 1)
				tagName, ok := imgs[0].TagName()
				if ok {
					require.Equal(t, benchmarkImg.String(), tagName)
					dgst, err := containerdStore.Resolve(t.Context(), tagName)
					require.NoError(t, err)
					require.Equal(t, imgs[0].Digest, dgst)
				}

				descs := []ocispec.Descriptor{}
				contents, err := containerdStore.ListContent(t.Context())
				require.NoError(t, err)
				for _, refs := range contents {
					for _, ref := range refs {
						rc, err := containerdStore.Open(t.Context(), ref.Digest)
						require.NoError(t, err)
						err = rc.Close()
						require.NoError(t, err)

						desc, err := containerdStore.Descriptor(t.Context(), ref.Digest)
						require.NoError(t, err)
						descs = append(descs, desc)
					}
				}
				require.ElementsMatch(t, expectedDescs, descs)

				t.Log("Deleting image with CRI", benchmarkImg.String())
				_, err = imageClient.RemoveImage(t.Context(), &runtimeapi.RemoveImageRequest{Image: &runtimeapi.ImageSpec{Image: benchmarkImg.String()}})
				require.NoError(t, err)
				ensureEvents(t, eventCh, expectedDeleteEvents)
			}

			t.Log("Closing Containerd store")
			err = connClient.Close()
			require.NoError(t, err)
			err = containerdStore.Close()
			require.NoError(t, err)
		})
	}
	err = cli.Close()
	require.NoError(t, err)
}

func ensureEvents(t *testing.T, ch <-chan oci.OCIEvent, expected []oci.OCIEvent) {
	t.Helper()

	received := []oci.OCIEvent{}
	for range len(expected) {
		event := <-ch
		received = append(received, event)
	}
	require.ElementsMatch(t, expected, received)
}
