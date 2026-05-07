package httpx

import (
	"html/template"
	"io"
	"net/http"
	"strconv"
	"testing"

	"github.com/go-openapi/testify/v2/require"
)

func TestRenderTemplate(t *testing.T) {
	t.Parallel()

	tmpl, err := template.New("").Parse("{{ .Test }}")
	require.NoError(t, err)

	rw, rec := NewRecorder()
	RenderTemplate(rw, tmpl, nil)
	resp := rec.Result()
	require.EqualT(t, http.StatusInternalServerError, resp.StatusCode)

	data := struct {
		Test string
	}{
		Test: "Hello World",
	}
	rw, rec = NewRecorder()
	RenderTemplate(rw, tmpl, data)
	resp = rec.Result()
	require.EqualT(t, http.StatusOK, resp.StatusCode)
	require.EqualT(t, ContentTypeHTML, rw.Header().Get(HeaderContentType))
	require.EqualT(t, strconv.FormatInt(int64(len(data.Test)), 10), rw.Header().Get(HeaderContentLength))
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.EqualT(t, data.Test, string(b))
}
