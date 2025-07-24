package httpx

type ResponseError interface {
	error
	ResponseBody() ([]byte, error)
}
