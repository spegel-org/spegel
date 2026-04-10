package web

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/spegel-org/spegel/pkg/httpx"
	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/registry"
	"github.com/spegel-org/spegel/pkg/routing"
)

func TestWeb(t *testing.T) {
	t.Parallel()

	router, err := routing.NewP2PRouter(t.Context(), ":0", nil, "5000")
	require.NoError(t, err)

	ociStore := oci.NewMemory()

	reg, err := registry.NewRegistry(ociStore, router)
	require.NoError(t, err)

	w, err := NewWeb(router, ociStore, reg, nil)
	require.NoError(t, err)
	require.NotNil(t, w.tmpls)

	rw, rec := httpx.NewRecorder()
	w.indexHandler(rw, nil)
	resp := rec.Result()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	rw, rec = httpx.NewRecorder()
	w.metaDataHandler(rw, nil)
	resp = rec.Result()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, httpx.ContentTypeJSON, resp.Header.Get(httpx.HeaderContentType))

	rw, rec = httpx.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	w.statsHandler(rw, req)
	resp = rec.Result()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	stats := statsData{
		LocalAddresses:    []netip.Addr{{}},
		Images:            []oci.Image{{}},
		Peers:             []routing.Peer{{}},
		MirrorLastSuccess: 1 * time.Minute,
	}
	rw, rec = httpx.NewRecorder()
	httpx.RenderTemplate(rw, w.tmpls.Lookup("stats.html"), stats)
	resp = rec.Result()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	rw, rec = httpx.NewRecorder()
	req = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	query := req.URL.Query()
	query.Add("image", "docker.io/library/ubuntu:latest")
	req.URL.RawQuery = query.Encode()
	w.measureHandler(rw, req)
	resp = rec.Result()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	measure := measureResult{
		LookupResults: []routing.LookupResult{{}},
		PullResults:   []pullResult{{}},
	}
	rw, rec = httpx.NewRecorder()
	httpx.RenderTemplate(rw, w.tmpls.Lookup("measure.html"), measure)
	resp = rec.Result()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}
