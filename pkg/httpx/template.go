package httpx

import (
	"bytes"
	"html/template"
	"net/http"
	"strconv"
)

func RenderTemplate(rw ResponseWriter, tmpl *template.Template, data any) {
	var buf bytes.Buffer
	err := tmpl.Option("missingkey=error").Execute(&buf, data)
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
		return
	}

	rw.Header().Set(HeaderContentLength, strconv.FormatInt(int64(buf.Len()), 10))
	rw.Header().Set(HeaderContentType, ContentTypeHTML)
	rw.WriteHeader(http.StatusOK)
	_, err = rw.Write(buf.Bytes())
	if err != nil {
		rw.WriteError(http.StatusInternalServerError, err)
	}
}
