package registry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/spegel-org/spegel/pkg/httpx"
	"github.com/spegel-org/spegel/pkg/oci"
)

// Add a temporary lease to newly created content and images.
func (r *Registry) withLease(ctx context.Context) (context.Context, error) {
	if r.leaseDuration == 0 {
		return ctx, nil
	}
	cd, ok := r.ociStore.(*oci.Containerd)
	if !ok {
		return nil, errors.New("lease requires containerd store")
	}
	cdc, err := cd.Client()
	if err != nil {
		return nil, err
	}
	lease := cdc.LeasesService()

	l, err := lease.Create(ctx, leases.WithRandomID(), leases.WithExpiration(r.leaseDuration))
	if err != nil {
		return nil, fmt.Errorf("failed to create lease: %w", err)
	}
	return leases.WithLease(ctx, l.ID), nil
}

func uploadRef(id string) string {
	return ("spegel-upload:") + id
}

func withSource(dist oci.DistributionPath) content.Opt {
	return content.WithLabels(map[string]string{labels.LabelDistributionSource + "." + dist.Registry: dist.Name})
}

func uploadStatus(rw httpx.ResponseWriter, dist oci.DistributionPath, offset int64) {
	rw.Header().Set("Location", "/v2/"+dist.Name+"/blobs/uploads/"+dist.Session)
	rw.Header().Set("Docker-Upload-UUID", dist.Session)
	rw.Header().Set("Range", "0-"+strconv.FormatInt(max(0, offset-1), 10))
	rw.Header().Set(httpx.HeaderContentLength, "0")
}

func created(rw httpx.ResponseWriter, dist oci.DistributionPath) {
	rw.Header().Set(oci.HeaderDockerDigest, dist.Digest.String())
	rw.Header().Set("Location", dist.URL().Path)
	rw.Header().Set(httpx.HeaderContentLength, "0")
	rw.WriteHeader(http.StatusCreated)
}

func (r *Registry) pushHandler(rw httpx.ResponseWriter, req *http.Request) {
	rw.SetHandler("push")

	if !r.push {
		rw.WriteError(http.StatusMethodNotAllowed, oci.NewDistributionError(oci.ErrCodeUnsupported, "push endpoints disabled", nil))
		return
	}

	// Check basic authentication
	if r.username != "" || r.password != "" {
		username, password, _ := req.BasicAuth()
		if r.username != username || r.password != password {
			rw.WriteError(http.StatusUnauthorized, oci.NewDistributionError(oci.ErrCodeUnauthorized, "invalid credentials", nil))
			return
		}
	}

	// Parse out path components from request.
	dist, err := oci.ParseDistributionPath(req.URL)
	if err != nil {
		rw.WriteError(http.StatusNotFound, fmt.Errorf("could not parse path according to OCI distribution spec: %w", err))
		return
	}

	cdc, cs, err := r.getContainerdClient()
	if err != nil {
		rw.WriteError(http.StatusMethodNotAllowed, oci.NewDistributionError(oci.ErrCodeUnsupported, err.Error(), nil))
		return
	}

	if dist.Kind == oci.DistributionKindUpload {
		if req.Method == http.MethodPost && dist.Session == "" {
			if string(dist.Digest) == "" {
				r.handleBlobUploadStart(rw, req, dist, cs)
			} else {
				r.handleBlobUploadMonolithic(rw, req, dist, cs)
			}
			return
		}
		if dist.Session != "" {
			switch req.Method {
			case http.MethodPatch:
				r.handleBlobUploadChunk(rw, req, dist, cs)
				return
			case http.MethodPut:
				r.handleBlobUploadCommit(rw, req, dist, cs)
				return
			case http.MethodGet:
				r.handleBlobUploadGet(rw, req, dist, cs)
				return
			}
		}

	}

	if dist.Kind == oci.DistributionKindManifest && req.Method == http.MethodPut {
		r.handleManifestPut(rw, req, dist, cdc)
		return
	}

	rw.WriteError(http.StatusNotFound, oci.NewDistributionError(oci.ErrCodeUnsupported, "unsupported push endpoint", nil))
}

func (r *Registry) getContainerdClient() (*client.Client, content.Store, error) {
	cd, ok := r.ociStore.(*oci.Containerd)
	if !ok {
		return nil, nil, errors.New("push requires containerd store")
	}
	cdc, err := cd.Client()
	if err != nil {
		return nil, nil, err
	}
	return cdc, cdc.ContentStore(), nil
}

func (r *Registry) handleBlobUploadMonolithic(rw httpx.ResponseWriter, req *http.Request, dist oci.DistributionPath, cs content.Store) {
	if err := dist.Digest.Validate(); err != nil {
		rw.WriteError(http.StatusBadRequest, oci.NewDistributionError(oci.ErrCodeDigestInvalid, "invalid digest", err.Error()))
		return
	}
	ctx, err := r.withLease(req.Context())
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}

	w, err := cs.Writer(ctx, content.WithRef(uploadRef(uuid.NewString())))
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
	defer w.Close()

	n, err := io.Copy(w, req.Body)
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
	if err = w.Commit(ctx, n, dist.Digest, withSource(dist)); err != nil && !errdefs.IsAlreadyExists(err) {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}

	created(rw, dist)
}

func (r *Registry) handleBlobUploadStart(rw httpx.ResponseWriter, req *http.Request, dist oci.DistributionPath, cs content.Store) {
	dist.Session = uuid.NewString()
	w, err := cs.Writer(req.Context(), content.WithRef(uploadRef(dist.Session)))
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
	_ = w.Close()
	uploadStatus(rw, dist, 0)
	rw.WriteHeader(http.StatusAccepted)
}

func (r *Registry) handleBlobUploadChunk(rw httpx.ResponseWriter, req *http.Request, dist oci.DistributionPath, cs content.Store) {
	ctx, err := r.withLease(req.Context())
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}

	w, err := cs.Writer(ctx, content.WithRef(uploadRef(dist.Session)))
	if err != nil {
		rw.WriteError(http.StatusNotFound, oci.NewDistributionError(oci.ErrCodeBlobUploadUnknown, "unknown upload session", nil))
		return
	}
	if _, err = io.Copy(w, req.Body); err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}

	status, err := w.Status()
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}

	uploadStatus(rw, dist, status.Offset)
	rw.WriteHeader(http.StatusAccepted)
}

func (r *Registry) handleBlobUploadCommit(rw httpx.ResponseWriter, req *http.Request, dist oci.DistributionPath, cs content.Store) {
	if err := dist.Digest.Validate(); err != nil {
		rw.WriteError(http.StatusBadRequest, oci.NewDistributionError(oci.ErrCodeDigestInvalid, "invalid digest", err.Error()))
		return
	}
	ctx, err := r.withLease(req.Context())
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
	desc := ocispec.Descriptor{Digest: dist.Digest}
	w, err := cs.Writer(ctx, content.WithRef(uploadRef(dist.Session)), content.WithDescriptor(desc))
	if err != nil {
		rw.WriteError(http.StatusNotFound, oci.NewDistributionError(oci.ErrCodeBlobUploadUnknown, "unknown upload session", nil))
		return
	}
	defer w.Close()

	// final chunk
	if _, err = io.Copy(w, req.Body); err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
	status, err := w.Status()
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}

	if err = w.Commit(ctx, status.Offset, dist.Digest, withSource(dist)); err != nil && !errdefs.IsAlreadyExists(err) {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}

	dist.Kind = oci.DistributionKindBlob
	created(rw, dist)
}

func (r *Registry) handleBlobUploadGet(rw httpx.ResponseWriter, req *http.Request, dist oci.DistributionPath, cs content.Store) {
	status, err := cs.Status(req.Context(), uploadRef(dist.Session))
	if err != nil && errdefs.IsNotFound(err) {
		rw.WriteError(http.StatusNotFound, oci.NewDistributionError(oci.ErrCodeBlobUploadUnknown, "unknown upload session", nil))
		return
	} else if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}

	uploadStatus(rw, dist, status.Offset)
	rw.WriteHeader(http.StatusNoContent)
}

func (r *Registry) handleManifestPut(rw httpx.ResponseWriter, req *http.Request, dist oci.DistributionPath, client *client.Client) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
	mediaType := req.Header.Get(httpx.HeaderContentType)
	if mediaType == "" {
		mediaType, err = oci.DetermineMediaType(body)
		if err != nil {
			rw.WriteError(http.StatusBadRequest, oci.NewDistributionError(oci.ErrCodeManifestInvalid, "cannot determine manifest media type", nil))
			return
		}
	}
	size := int64(len(body))
	desc := ocispec.Descriptor{MediaType: mediaType, Digest: digest.FromBytes(body), Size: size}

	ctx, err := r.withLease(req.Context())
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}

	cs := client.ContentStore()
	w, err := cs.Writer(ctx, content.WithRef(dist.Reference()))
	if err != nil && !errdefs.IsAlreadyExists(err) {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}
	if err == nil {
		defer w.Close()
		if _, err := io.Copy(w, bytes.NewReader(body)); err != nil {
			rw.WriteError(http.StatusInternalServerError, err)
			return
		}
		if err = w.Commit(ctx, size, desc.Digest, withSource(dist)); err != nil && !errdefs.IsAlreadyExists(err) {
			rw.WriteError(http.StatusInternalServerError, err)
			return
		}
	}

	ref := dist.Reference()
	if dist.Digest != "" {
		ref = fmt.Sprintf("%s/%s@%s", dist.Registry, dist.Name, desc.Digest)
	}
	if _, err = client.ImageService().Create(ctx, (images.Image{Name: ref, Target: desc})); err != nil && !errdefs.IsAlreadyExists(err) {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}

	dist.Digest = desc.Digest
	created(rw, dist)

	if err = images.Dispatch(ctx, images.SetChildrenLabels(cs, images.ChildrenHandler(cs)), nil, desc); err != nil {
		r.log.Error(err, "failed to set image labels")
		return
	}
	go func() {
		// Broadcast image content for immediate discovery.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		img, err := oci.NewImage(dist.Registry, dist.Name, dist.Tag, desc.Digest)
		if err != nil {
			r.log.Error(err, "failed to announce image")
			return
		}
		keys := []string{desc.Digest.String(), ref}
		digests, err := oci.WalkImage(ctx, r.ociStore, img)
		if err == nil {
			for _, dgst := range digests {
				keys = append(keys, dgst.String())
			}
		} else {
			r.log.Error(err, "failed to walk image")
		}

		err = r.router.Advertise(ctx, keys, true)
		if err != nil {
			r.log.Error(err, "failed to advertise image")
		} else {
			r.log.Info("advertised image")
		}
	}()
	if r.pushUpstream {
		pushHeaders := req.Header.Clone()
		go func() {
			log := r.log.WithName("backgroundPush").WithValues("ref", ref, "desc", desc)
			log.Info("Starting upstream image push")
			ctx := context.Background()

			pusher, err := docker.NewResolver(docker.ResolverOptions{Headers: pushHeaders}).Pusher(ctx, ref)
			if err != nil {
				log.Error(err, "failed to get pusher")
				return
			}

			if err = remotes.PushContent(ctx, pusher, desc, cs, nil, nil, nil); err != nil {
				log.Error(err, "failed to push image upstream")
				return
			}
			log.Info("Finished upstream image push")
		}()
	}
}
