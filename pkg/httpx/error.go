package httpx

type ResponseError interface {
	error
	ResponseBody() ([]byte, string, error)
}

var _ ResponseError = &BasicResponseError{}

type BasicResponseError struct {
	Body string
}

func NewBasicResponseError(body string) *BasicResponseError {
	return &BasicResponseError{
		Body: body,
	}
}

func (e *BasicResponseError) Error() string {
	return e.Body
}

func (e *BasicResponseError) ResponseBody() ([]byte, string, error) {
	return []byte(e.Body), ContentTypeText, nil
}
