package containerd

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/client"
	ctrdclient "github.com/containerd/containerd/v2/client"
	"github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	"github.com/go-openapi/testify/v2/assert"
	"github.com/go-openapi/testify/v2/require"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	mobyclient "github.com/moby/moby/client"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"go.uber.org/goleak"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"

	"github.com/kvick-org/pkg/errgroup"

	"github.com/spegel-org/spegel/internal/testutil"
	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/oci/containerd"
	"github.com/spegel-org/spegel/pkg/store"
	"github.com/spegel-org/spegel/pkg/store/storetest"
)

var (
	ctrdVersions = []string{
		"2.3.4",
		"2.2.7",
		"2.4.0-beta.0",
	}
	ctrdNamespace = "k8s.io"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
	)
}

func TestContainerdPull(t *testing.T) {
	testStrategy := os.Getenv("INTEGRATION_TEST_STRATEGY")
	require.NotEmpty(t, testStrategy)
	t.Log("Running tests with with strategy", testStrategy)

	switch testStrategy {
	case "all":
		break
	case "fast":
		ctrdVersions = ctrdVersions[:1]
	default:
		t.Fatal("unknown test strategy", testStrategy)
	}

	mobyClient, err := mobyclient.New(mobyclient.FromEnv)
	require.NoError(t, err)
	t.Cleanup(func() {
		mobyClient.Close()
	})

	ctrdImgs := []oci.Image{}
	pullGroup := errgroup.WithContext(t.Context())
	for _, ctrdVersion := range ctrdVersions {
		img, err := oci.NewImage("ghcr.io", "spegel-org/test-images/containerd", ctrdVersion, "")
		require.NoError(t, err)
		ctrdImgs = append(ctrdImgs, img)

		t.Log("Pulling Containerd image", img.String())
		pullGroup.Go(func(ctx context.Context) error {
			_, err := mobyClient.ImageInspect(ctx, img.String())
			if err != nil {
				if !errors.Is(err, errdefs.ErrNotFound) {
					return err
				}
				resp, err := mobyClient.ImagePull(ctx, img.String(), mobyclient.ImagePullOptions{})
				if err != nil {
					return err
				}
				err = resp.Wait(ctx)
				if err != nil {
					return err
				}
			}
			return nil
		})
	}
	err = pullGroup.Wait()
	require.NoError(t, err)

	for _, ctrdImg := range ctrdImgs {
		t.Run(ctrdImg.Tag, func(t *testing.T) {
			t.Parallel()

			t.Log("Running Containerd container")
			env := []string{
				fmt.Sprintf("USER_ID=%d", os.Getuid()),
				fmt.Sprintf("GROUP_ID=%d", os.Getgid()),
			}
			runPath := t.TempDir()
			createOpt := mobyclient.ContainerCreateOptions{
				Config: &container.Config{
					Image: ctrdImg.String(),
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

			socketPath := filepath.Join(runPath, "containerd.sock")

			ctrdClient, err := ctrdclient.New(socketPath, ctrdclient.WithDefaultNamespace(ctrdNamespace))
			require.NoError(t, err)
			t.Cleanup(func() {
				ctrdClient.Close()
			})

			connClient, err := grpc.NewClient("unix://"+socketPath, grpc.WithTransportCredentials(insecure.NewCredentials()))
			require.NoError(t, err)
			t.Cleanup(func() {
				connClient.Close()
			})
			imageClient := runtimeapi.NewImageServiceClient(connClient)

			ociClient, err := oci.NewClient(oci.WithDisableKeepAlives(true))
			require.NoError(t, err)

			tests := []struct {
				name     string
				getFn    func(ctx context.Context, img oci.Image) error
				deleteFn func(ctx context.Context, img oci.Image) error
			}{
				{
					name: "cri",
					getFn: func(ctx context.Context, img oci.Image) error {
						_, err = imageClient.PullImage(t.Context(), &runtimeapi.PullImageRequest{Image: &runtimeapi.ImageSpec{Image: img.String()}})
						if err != nil {
							return err
						}
						return nil
					},
					deleteFn: func(ctx context.Context, img oci.Image) error {
						_, err = imageClient.RemoveImage(t.Context(), &runtimeapi.RemoveImageRequest{Image: &runtimeapi.ImageSpec{Image: img.String()}})
						if err != nil {
							return err
						}
						return nil
					},
				},
				{
					name: "import",
					getFn: func(ctx context.Context, img oci.Image) error {
						w := NewExportWriter(t.TempDir())
						err := ociClient.Pull(ctx, img, w)
						if err != nil {
							return err
						}
						rc, err := w.Export()
						if err != nil {
							return err
						}
						opts := []ctrdclient.ImportOpt{
							ctrdclient.WithIndexName(string(digest.FromString(img.String()))),
							ctrdclient.WithDigestRef(func(d digest.Digest) string {
								if tagName, ok := img.TagName(); ok && img.Digest == "" {
									return tagName
								}
								return fmt.Sprintf("%s/%s@%s", img.Registry, img.Repository, img.Digest)
							}),
							ctrdclient.WithImageLabels(map[string]string{"io.cri-containerd.image": "managed"}),
						}
						importImgs, err := ctrdClient.Import(ctx, rc, opts...)
						if err != nil {
							return err
						}
						for _, importImg := range importImgs {
							image := client.NewImageWithPlatform(ctrdClient, importImg, platforms.All)
							err := image.Unpack(ctx, "")
							if err != nil {
								return err
							}
						}

						return nil
					},
					deleteFn: func(ctx context.Context, img oci.Image) error {
						_, err = imageClient.RemoveImage(t.Context(), &runtimeapi.RemoveImageRequest{Image: &runtimeapi.ImageSpec{Image: img.String()}})
						if err != nil {
							return err
						}
						return nil
					},
				},
			}
			for _, tt := range tests[1:] {
				t.Run(tt.name, func(t *testing.T) {
					t.Log("Setting up Containerd store")
					ctrd, err := containerd.NewContainerd(t.Context(), socketPath, ctrdNamespace)
					require.NoError(t, err)
					ctrdName := ctrd.Name()
					require.EqualT(t, "containerd", ctrdName)

					t.Log("Pulling initial image")
					initialImg, err := oci.ParseImage("ghcr.io/spegel-org/spegel:v0.7.4", oci.AllowTagOnly())
					require.NoError(t, err)
					err = tt.getFn(t.Context(), initialImg)
					require.NoError(t, err)
					expectedInitial := []store.Event{
						{Type: store.CreateEvent, Reference: "ghcr.io/spegel-org/spegel:v0.7.4"},
						{Type: store.CreateEvent, Digest: "sha256:26c60b05e08ac738e8442bc389c5780bff0e1d8153956e45d810a2f1008cf56f"},
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
					// The top level index is also included when importing.
					if tt.name == "import" {
						expectedInitial = append(expectedInitial, store.Event{Type: store.CreateEvent, Digest: "sha256:86c4fc3395f61469f7413dfea4153b310d8383c7115e751d96668fe2d23d34b6"})
					}

					t.Log("Checking conformance")
					providerCfg := storetest.ProviderConfig{
						Name:               "containerd",
						NotFoundRef:        "dummy",
						ExistingRef:        expectedInitial[0].Reference,
						ExistingRefDigest:  expectedInitial[1].Digest,
						NotFoundDigest:     digest.FromBytes(nil),
						ExistingDescriptor: store.Descriptor{Digest: expectedInitial[1].Digest, Size: 2385, MediaType: ocispec.MediaTypeImageIndex},
					}
					storetest.ProviderConformance(t, ctrd, providerCfg)

					subCtx, subCancel := context.WithCancel(t.Context())
					initial, eventCh, err := ctrd.Watch(subCtx)
					require.NoError(t, err)
					require.ElementsMatchT(t, expectedInitial, initial)

					benchmarkImgs := []oci.Image{}
					for i := range 3 {
						benchmarkImg, err := oci.ParseImage("ghcr.io/spegel-org/benchmark:v2-10MB-4@sha256:735223c59bb4df293176337f84f42b58ac53cb5a4740752b7aa56c19c0f6ec5b")
						require.NoError(t, err)
						switch i {
						case 0:
							benchmarkImg.Digest = ""
						case 2:
							benchmarkImg.Tag = ""
						}
						benchmarkImgs = append(benchmarkImgs, benchmarkImg)
					}
					for _, benchmarkImg := range benchmarkImgs {
						expectedDescs := []ocispec.Descriptor{
							{Digest: "sha256:735223c59bb4df293176337f84f42b58ac53cb5a4740752b7aa56c19c0f6ec5b", Size: 1371, MediaType: "application/vnd.docker.distribution.manifest.v2+json"},
							{Digest: "sha256:7582c2cc65ef30105b84c1c6812f71c8012663c6352b01fe2f483238313ab0ed", Size: 307023, MediaType: "application/octet-stream"},
							{Digest: "sha256:85bdfbf66d5c95e296fd1332d94e6a0ac86508af48fbd28b825db7c15b39cdad", Size: 1318, MediaType: "application/vnd.oci.image.config.v1+json"},
							{Digest: "sha256:99ea62d595b5a3e1d01639af2781f97730eca4086f5308be58f68b18c244adc9", Size: 2622396, MediaType: "application/octet-stream"},
							{Digest: "sha256:a3dbaff286eb1da0a03dd99d51cbeacb6f38f1dfd1ce04c267278d835fa64865", Size: 2622398, MediaType: "application/octet-stream"},
							{Digest: "sha256:d76a66ca5a6e5fdd3b4f5df356b7762572327f0d9c1dbf4d71d1116fbc623589", Size: 2622396, MediaType: "application/octet-stream"},
							{Digest: "sha256:df178cf0f2112519a5ff06bec070a33b2e2a968936466ccfec15b13f1a51ae86", Size: 2622395, MediaType: "application/octet-stream"},
						}
						// The top level index is also included when importing.
						if tt.name == "import" {
							expectedDescs = append(expectedDescs, ocispec.Descriptor{Digest: "sha256:3add891293bdf0f5fead0f504223645476adccc0746d43b2c82d3781d1d2358f", Size: 0, MediaType: "application/octet-stream"})
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

						t.Log("Pulling image", benchmarkImg.String())
						err = tt.getFn(t.Context(), benchmarkImg)
						require.NoError(t, err)
						testutil.EnsureEvents(t, eventCh, expectedCreateEvents)

						t.Log("Listing images")
						imgs, err := ctrd.ListImages(t.Context())
						require.NoError(t, err)
						require.Len(t, imgs, 2)

						t.Log("Deleting image", benchmarkImg.String())
						err = tt.deleteFn(t.Context(), benchmarkImg)
						require.NoError(t, err)
						testutil.EnsureEvents(t, eventCh, expectedDeleteEvents)
					}

					t.Log("Closing subscription")
					subCancel()
					testutil.WaitForClose(t, eventCh)

					err = tt.deleteFn(t.Context(), initialImg)
					require.NoError(t, err)

					t.Log("Closing Containerd store")
					_, eventCh, err = ctrd.Watch(t.Context())
					require.NoError(t, err)

					err = ctrd.Close()
					require.NoError(t, err)

					testutil.WaitForClose(t, eventCh)
				})
			}
		})
	}
	err = mobyClient.Close()
	require.NoError(t, err)
}

type ExportWriter struct {
	dirPath string
	idx     ocispec.Index
}

func NewExportWriter(dirPath string) *ExportWriter {
	return &ExportWriter{
		dirPath: dirPath,
	}
}

func (w *ExportWriter) Root(ctx context.Context, img oci.Image, desc ocispec.Descriptor) error {
	err := os.WriteFile(filepath.Join(w.dirPath, "oci-layout"), []byte(`{"imageLayoutVersion": "1.0.0"}`), 0o644)
	if err != nil {
		return err
	}
	idx := ocispec.Index{
		Versioned: specs.Versioned{
			SchemaVersion: 2,
		},
		MediaType: ocispec.MediaTypeImageIndex,
		Manifests: []ocispec.Descriptor{
			{
				MediaType: desc.MediaType,
				Digest:    desc.Digest,
				Size:      desc.Size,
			},
		},
	}
	b, err := json.Marshal(&idx)
	if err != nil {
		return err
	}
	err = os.WriteFile(filepath.Join(w.dirPath, "index.json"), b, 0o644)
	if err != nil {
		return err
	}
	return nil
}

func (w *ExportWriter) Write(ctx context.Context, desc ocispec.Descriptor, r io.Reader) error {
	blobPath := filepath.Join(w.dirPath, "blobs", desc.Digest.Algorithm().String())
	err := os.MkdirAll(blobPath, 0o755)
	if err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(blobPath, desc.Digest.Encoded()))
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, r)
	if err != nil {
		return err
	}
	return nil
}

func (w *ExportWriter) Export() (io.Reader, error) {
	pr, pw := io.Pipe()

	go func() {
		tw := tar.NewWriter(pw)

		err := filepath.Walk(w.dirPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			rel, err := filepath.Rel(w.dirPath, path)
			if err != nil {
				return err
			}
			if rel == "." {
				return nil
			}

			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			hdr.Name = filepath.ToSlash(rel)

			if info.IsDir() {
				hdr.Name += "/"
			}

			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}

			if info.IsDir() {
				return nil
			}

			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()

			_, err = io.Copy(tw, f)
			return err
		})

		if err == nil {
			err = tw.Close()
		}
		pw.CloseWithError(err)
	}()

	return pr, nil
}
