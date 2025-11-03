package web

import (
	"errors"
	"fmt"
	"html"

	"github.com/spegel-org/spegel/pkg/httpx"
)

var _ httpx.ResponseError = &HTMLResponseError{}

type HTMLResponseError struct {
	error
}

func NewHTMLResponseError(err error) *HTMLResponseError {
	return &HTMLResponseError{err}
}

func (e *HTMLResponseError) ResponseBody() ([]byte, string, error) {
	if e.error == nil {
		return nil, "", errors.New("no error set")
	}
	return fmt.Appendf(nil, `<p class="error">%s</p>`, html.EscapeString(e.Error())), httpx.ContentTypeText, nil
}
