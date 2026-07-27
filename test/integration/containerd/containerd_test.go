package containerd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	ctrdclient "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/go-openapi/testify/v2/assert"
	"github.com/go-openapi/testify/v2/require"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	mobyclient "github.com/moby/moby/client"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"go.uber.org/goleak"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"

	"github.com/kvick-org/pkg/errgroup"

	"github.com/spegel-org/spegel/internal/testutil"
	"github.com/spegel-org/spegel/pkg/httpx"
	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/oci/containerd"
	"github.com/spegel-org/spegel/pkg/store"
)

var (
	containerdVersions = []string{
		"2.3.4",
		"2.2.7",
		"2.4.0-beta.0",
	}
	containerdNamespace = "k8s.io"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestContainerdPull(t *testing.T) {
	testStrategy := os.Getenv("INTEGRATION_TEST_STRATEGY")
	require.NotEmpty(t, testStrategy)
	t.Log("Running tests with with strategy", testStrategy)

	switch testStrategy {
	case "all":
		break
	case "fast":
		containerdVersions = containerdVersions[:1]
	default:
		t.Fatal("unknown test strategy", testStrategy)
	}

	mobyClient, err := mobyclient.New(mobyclient.FromEnv)
	require.NoError(t, err)
	t.Cleanup(func() {
		mobyClient.Close()
	})

	containerdImgs := []oci.Image{}
	pullGroup := errgroup.WithContext(t.Context())
	for _, containerdVersion := range containerdVersions {
		img, err := oci.NewImage("ghcr.io", "spegel-org/test-images/containerd", containerdVersion, "")
		require.NoError(t, err)
		containerdImgs = append(containerdImgs, img)

		t.Log("Pulling Containerd image", img.String())
		pullGroup.Go(func(ctx context.Context) error {
			resp, err := mobyClient.ImagePull(ctx, img.String(), mobyclient.ImagePullOptions{})
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

	for _, img := range containerdImgs {
		t.Run(img.Tag, func(t *testing.T) {
			t.Log("Running Containerd container")
			env := []string{
				fmt.Sprintf("USER_ID=%d", os.Getuid()),
				fmt.Sprintf("GROUP_ID=%d", os.Getgid()),
			}
			runPath := t.TempDir()
			createOpt := mobyclient.ContainerCreateOptions{
				Config: &container.Config{
					Image: img.String(),
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
			createResp, err := mobyClient.ContainerCreate(t.Context(), createOpt)
			require.NoError(t, err)
			_, err = mobyClient.ContainerStart(t.Context(), createResp.ID, mobyclient.ContainerStartOptions{})
			require.NoError(t, err)
			t.Cleanup(func() {
				mobyClient.ContainerStop(context.Background(), createResp.ID, mobyclient.ContainerStopOptions{})
			})
			require.EventuallyWith(t, func(collect *assert.CollectT) {
				require.FileExists(collect, filepath.Join(runPath, "ready"))
			}, 10*time.Second, 100*time.Millisecond)

			t.Log("Setting up Containerd store")
			socketPath := filepath.Join(runPath, "containerd.sock")

			ctrdClient, err := ctrdclient.New(socketPath, ctrdclient.WithDefaultNamespace(containerdNamespace))
			require.NoError(t, err)

			connClient, err := grpc.NewClient("unix://"+socketPath, grpc.WithTransportCredentials(insecure.NewCredentials()))
			require.NoError(t, err)
			imageClient := runtimeapi.NewImageServiceClient(connClient)

			ctrd, err := containerd.NewContainerd(t.Context(), socketPath, containerdNamespace)
			require.NoError(t, err)
			name := ctrd.Name()
			require.EqualT(t, "containerd", name)

			_, err = imageClient.PullImage(t.Context(), &runtimeapi.PullImageRequest{Image: &runtimeapi.ImageSpec{Image: "ghcr.io/spegel-org/spegel:v0.7.4"}})
			require.NoError(t, err)
			expectedInitial := []store.Event{
				{Type: store.CreateEvent, Reference: "ghcr.io/spegel-org/spegel:v0.7.4", Digest: "sha256:26c60b05e08ac738e8442bc389c5780bff0e1d8153956e45d810a2f1008cf56f"},
				{Type: store.CreateEvent, Digest: "sha256:cfa0b07068007bc283828f25ee6a128c81052857b9c1efc93c4dc596ed895b6a"},
				{Type: store.CreateEvent, Digest: "sha256:e7a777e36197ea8d4ce50cb206cfb238986e3462fa5b1f3c28cbbfb5c5128431"},
				{Type: store.CreateEvent, Digest: "sha256:1eed391ea893e6015bf4ce4ed366909975d2acdbe907670919236a8d18ea6b07"},
				{Type: store.CreateEvent, Digest: "sha256:c172f21841dff4c8cf45cde46589c1c2616cefe7e819965e92e6d3475c428aa0"},
				{Type: store.CreateEvent, Digest: "sha256:99515e7b4d35e0652d3b0fde571b6ec269222ecacc506f026e1758d6261e9109"},
				{Type: store.CreateEvent, Digest: "sha256:99ba982a9142213c751a1709dcf088e63d8601f03b3f211bae037be698fef270"},
				{Type: store.CreateEvent, Digest: "sha256:d6b1b89eccacc15c2420b2776d72c1dae334a00805ed9af54bf2f71e4d536f28"},
				{Type: store.CreateEvent, Digest: "sha256:2780920e5dbfbe103d03a583ed75345306e572ec5a48cb10361f046767d9f29a"},
				{Type: store.CreateEvent, Digest: "sha256:7c12895b777bcaa8ccae0605b4de635b68fc32d60fa08f421dc3818bf55ee212"},
				{Type: store.CreateEvent, Digest: "sha256:3214acf345c0cc6bbdb56b698a41ccdefc624a09d6beb0d38b5de0b2303ecaf4"},
				{Type: store.CreateEvent, Digest: "sha256:52630fc75a18675c530ed9eba5f55eca09b03e91bd5bc15307918bbc1a7e7296"},
				{Type: store.CreateEvent, Digest: "sha256:dd64bf2dd177757451a98fcdc999a339c35dee5d9872d8f4dc69c8f3c4dd0112"},
				{Type: store.CreateEvent, Digest: "sha256:b839dfae01f66e15c6a8b63520557ed315bdfe036342fa7a0c537259f10d7a9a"},
				{Type: store.CreateEvent, Digest: "sha256:ebddc55facdc6b1f7e0f30816a5fc7cc62f38abdf76c0a8b0a0ce52085754795"},
				{Type: store.CreateEvent, Digest: "sha256:bdfd7f7e5bf6fc27e70b59101db21c3d8284d283884419dd5fe7020583bb79ca"},
				{Type: store.CreateEvent, Digest: "sha256:8eb081c0ebda8c184042e9ad6ecf7ea761c9857f7d6f38cdb2d2cd95b0f2db4f"},
			}

			subCtx, subCancel := context.WithCancel(t.Context())
			initial, eventCh, err := ctrd.Watch(subCtx)
			require.NoError(t, err)
			require.ElementsMatchT(t, expectedInitial, initial)

			imgs := []string{
				"ghcr.io/spegel-org/benchmark:v2-10MB-4",
				"ghcr.io/spegel-org/benchmark:v2-10MB-4@sha256:735223c59bb4df293176337f84f42b58ac53cb5a4740752b7aa56c19c0f6ec5b",
				"ghcr.io/spegel-org/benchmark@sha256:735223c59bb4df293176337f84f42b58ac53cb5a4740752b7aa56c19c0f6ec5b",
			}
			for _, s := range imgs {
				benchmarkImg, err := oci.ParseImage(s, oci.AllowTagOnly())
				require.NoError(t, err)
				expectedDescs := []store.Descriptor{
					{Digest: "sha256:735223c59bb4df293176337f84f42b58ac53cb5a4740752b7aa56c19c0f6ec5b", Size: 1371, MediaType: images.MediaTypeDockerSchema2Manifest},
					{Digest: "sha256:7582c2cc65ef30105b84c1c6812f71c8012663c6352b01fe2f483238313ab0ed", Size: 307023, MediaType: httpx.ContentTypeBinary},
					{Digest: "sha256:85bdfbf66d5c95e296fd1332d94e6a0ac86508af48fbd28b825db7c15b39cdad", Size: 1318, MediaType: ocispec.MediaTypeImageConfig},
					{Digest: "sha256:99ea62d595b5a3e1d01639af2781f97730eca4086f5308be58f68b18c244adc9", Size: 2622396, MediaType: httpx.ContentTypeBinary},
					{Digest: "sha256:a3dbaff286eb1da0a03dd99d51cbeacb6f38f1dfd1ce04c267278d835fa64865", Size: 2622398, MediaType: httpx.ContentTypeBinary},
					{Digest: "sha256:d76a66ca5a6e5fdd3b4f5df356b7762572327f0d9c1dbf4d71d1116fbc623589", Size: 2622396, MediaType: httpx.ContentTypeBinary},
					{Digest: "sha256:df178cf0f2112519a5ff06bec070a33b2e2a968936466ccfec15b13f1a51ae86", Size: 2622395, MediaType: httpx.ContentTypeBinary},
				}
				expectedCreateEvents := []store.Event{}
				expectedDeleteEvents := []store.Event{}
				if tagName, ok := benchmarkImg.TagName(); ok && benchmarkImg.Digest == "" {
					expectedCreateEvents = append(expectedCreateEvents, store.Event{Type: store.CreateEvent, Reference: tagName})
					expectedDeleteEvents = append(expectedDeleteEvents, store.Event{Type: store.DeleteEvent, Reference: tagName})
				}
				for _, desc := range expectedDescs {
					expectedCreateEvents = append(expectedCreateEvents, store.Event{Type: store.CreateEvent, Digest: desc.Digest})
					expectedDeleteEvents = append(expectedDeleteEvents, store.Event{Type: store.DeleteEvent, Digest: desc.Digest})
				}

				t.Log("Pulling image with CRI", benchmarkImg.String())
				_, err = imageClient.PullImage(t.Context(), &runtimeapi.PullImageRequest{Image: &runtimeapi.ImageSpec{Image: benchmarkImg.String()}})
				require.NoError(t, err)
				testutil.EnsureEvents(t, eventCh, expectedCreateEvents)

				t.Log("Checking Containerd store")
				imgs, err := ctrd.ListImages(t.Context())
				require.NoError(t, err)
				require.Len(t, imgs, 2)
				tagName, ok := imgs[0].TagName()
				if ok {
					require.EqualT(t, benchmarkImg.String(), tagName)
					dgst, err := ctrd.Resolve(t.Context(), tagName)
					require.NoError(t, err)
					require.EqualT(t, imgs[0].Digest, dgst)
				}
				if ok {
					cfg.ExistingRef = tagName
					cfg.ExistingRefDigest = imgs[0].Digest
				}
				storetest.Conformance(t, ctrd, cfg)

				time.Sleep(1 * time.Second)

				t.Log("Deleting image with CRI", benchmarkImg.String())
				_, err = imageClient.RemoveImage(t.Context(), &runtimeapi.RemoveImageRequest{Image: &runtimeapi.ImageSpec{Image: benchmarkImg.String()}})
				require.NoError(t, err)
				testutil.EnsureEvents(t, eventCh, expectedDeleteEvents)
			}

			t.Log("Closing subscription")
			subCancel()
			testutil.WaitForClose(t, eventCh)

			_, err = imageClient.RemoveImage(t.Context(), &runtimeapi.RemoveImageRequest{Image: &runtimeapi.ImageSpec{Image: "ghcr.io/spegel-org/spegel:v0.7.4"}})
			require.NoError(t, err)

			t.Log("Checking that content missing from the content store is not advertised")
			_, err = imageClient.PullImage(t.Context(), &runtimeapi.PullImageRequest{Image: &runtimeapi.ImageSpec{Image: imgs[0]}})
			require.NoError(t, err)
			missingDgst := digest.Digest("sha256:99ea62d595b5a3e1d01639af2781f97730eca4086f5308be58f68b18c244adc9")
			err = ctrdClient.ContentStore().Delete(t.Context(), missingDgst)
			require.NoError(t, err)

			missingCtx, missingCancel := context.WithCancel(t.Context())
			missingInitial, missingCh, err := ctrd.Watch(missingCtx)
			require.NoError(t, err)
			require.NotEmpty(t, missingInitial)
			require.Len(t, missingInitial, 6)
			require.NotContains(t, missingInitial, store.Event{Type: store.CreateEvent, Digest: missingDgst})

			missingCancel()
			testutil.WaitForClose(t, missingCh)

			t.Log("Closing Containerd store")
			_, eventCh, err = ctrd.Watch(t.Context())
			require.NoError(t, err)

			err = connClient.Close()
			require.NoError(t, err)
			err = ctrd.Close()
			require.NoError(t, err)

			testutil.WaitForClose(t, eventCh)

			err = ctrdClient.Close()
			require.NoError(t, err)
		})
	}
	err = mobyClient.Close()
	require.NoError(t, err)
}
