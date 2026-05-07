package web

import (
	"errors"
	"testing"

	"github.com/go-openapi/testify/v2/require"
)

func TestHTMLResponseError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err             error
		name            string
		wantContentType string
		wantBody        []byte
		wantErr         bool
	}{
		{
			name:            "with error",
			err:             errors.New("<error>"),
			wantBody:        []byte(`<p class="error">&lt;error&gt;</p>`),
			wantContentType: "text/plain",
			wantErr:         false,
		},
		{
			name:            "nil error",
			err:             nil,
			wantBody:        nil,
			wantContentType: "",
			wantErr:         true,
		},
		{
			name:            "empty error",
			err:             errors.New(""),
			wantBody:        []byte(`<p class="error"></p>`),
			wantContentType: "text/plain",
			wantErr:         false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := NewHTMLResponseError(tt.err)
			body, contentType, err := e.ResponseBody()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.SliceEqualT(t, tt.wantBody, body)
			require.EqualT(t, tt.wantContentType, contentType)
		})
	}
}
