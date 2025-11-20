package integration

import (
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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"

	"github.com/spegel-org/spegel/pkg/oci"
)

func TestContainerdPull(t *testing.T) {
	t.Parallel()

	cli, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	require.NoError(t, err)
	defer func() {
		err := cli.Close()
		assert.NoError(t, err)
	}()

	containerdVersions := []string{
		"1.7.29",
		"2.0.7",
		"2.1.5",
		"2.2.0",
	}
	for _, containerdVersion := range containerdVersions {
		t.Run(containerdVersion, func(t *testing.T) {
			t.Parallel()

			t.Log("building Containerd image")
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
			defer func() {
				err = buildResp.Body.Close()
				assert.NoError(t, err)
			}()
			_, err = cli.ImageLoad(t.Context(), buildResp.Body)
			require.NoError(t, err)

			t.Log("running Containerd container")
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
			defer func() {
				_, err := cli.ContainerStop(t.Context(), createResp.ID, client.ContainerStopOptions{})
				assert.NoError(t, err)
			}()
			require.EventuallyWithT(t, func(collect *assert.CollectT) {
				require.FileExists(collect, filepath.Join(runPath, "ready"))
			}, 10*time.Second, 100*time.Millisecond)

			t.Log("setting up Containerd store")
			socketPath := filepath.Join(runPath, "containerd.sock")

			connClient, err := grpc.NewClient("unix://"+socketPath, grpc.WithTransportCredentials(insecure.NewCredentials()))
			require.NoError(t, err)
			defer connClient.Close()
			imageClient := runtimeapi.NewImageServiceClient(connClient)

			containerdStore, err := oci.NewContainerd(t.Context(), socketPath, "k8s.io")
			require.NoError(t, err)
			err = containerdStore.Verify(t.Context(), "/etc/containerd/certs.d")
			require.NoError(t, err)
			name := containerdStore.Name()
			require.Equal(t, "containerd", name)
			eventCh, err := containerdStore.Subscribe(t.Context())
			require.NoError(t, err)

			benchmarkImage := "ghcr.io/spegel-org/benchmark:v2-10MB-4"
			expected := []string{
				"ghcr.io/spegel-org/benchmark@sha256:735223c59bb4df293176337f84f42b58ac53cb5a4740752b7aa56c19c0f6ec5b",
				"ghcr.io/spegel-org/benchmark@sha256:85bdfbf66d5c95e296fd1332d94e6a0ac86508af48fbd28b825db7c15b39cdad",
				"ghcr.io/spegel-org/benchmark@sha256:df178cf0f2112519a5ff06bec070a33b2e2a968936466ccfec15b13f1a51ae86",
				"ghcr.io/spegel-org/benchmark@sha256:99ea62d595b5a3e1d01639af2781f97730eca4086f5308be58f68b18c244adc9",
				"ghcr.io/spegel-org/benchmark@sha256:7582c2cc65ef30105b84c1c6812f71c8012663c6352b01fe2f483238313ab0ed",
				"ghcr.io/spegel-org/benchmark@sha256:d76a66ca5a6e5fdd3b4f5df356b7762572327f0d9c1dbf4d71d1116fbc623589",
				"ghcr.io/spegel-org/benchmark@sha256:a3dbaff286eb1da0a03dd99d51cbeacb6f38f1dfd1ce04c267278d835fa64865",
				benchmarkImage,
			}

			t.Log("testing OCI pull events")
			_, err = imageClient.PullImage(t.Context(), &runtimeapi.PullImageRequest{Image: &runtimeapi.ImageSpec{Image: benchmarkImage}})
			require.NoError(t, err)
			receivedCreate := []string{}
			for range len(expected) {
				event := <-eventCh
				require.Equal(t, oci.CreateEvent, event.Type)
				receivedCreate = append(receivedCreate, event.Reference.String())
			}
			require.ElementsMatch(t, expected, receivedCreate)

			t.Log("testing OCI delete events")
			_, err = imageClient.RemoveImage(t.Context(), &runtimeapi.RemoveImageRequest{Image: &runtimeapi.ImageSpec{Image: benchmarkImage}})
			require.NoError(t, err)
			receivedDelete := []string{}
			for range len(expected) {
				event := <-eventCh
				require.Equal(t, oci.DeleteEvent, event.Type)
				receivedDelete = append(receivedDelete, event.Reference.String())
			}
			require.ElementsMatch(t, expected, receivedDelete)

			t.Log("testing Container store close")
			err = containerdStore.Close()
			require.NoError(t, err)
		})
	}
}
