package httpx

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
)

type StatusError struct {
	Message       string
	ExpectedCodes []int
	StatusCode    int
}

func (e *StatusError) Error() string {
	expectedCodeStrs := []string{}
	for _, expected := range e.ExpectedCodes {
		expectedCodeStrs = append(expectedCodeStrs, fmt.Sprintf("%d %s", expected, http.StatusText(expected)))
	}
	msg := fmt.Sprintf("expected one of the following statuses [%s], but received %d %s", strings.Join(expectedCodeStrs, ", "), e.StatusCode, http.StatusText(e.StatusCode))
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

func CheckResponseStatus(resp *http.Response, expectedCodes ...int) error {
	if len(expectedCodes) == 0 {
		return errors.New("expected codes cannot be empty")
	}
	if slices.Contains(expectedCodes, resp.StatusCode) {
		return nil
	}
	message, messageErr := getErrorMessage(resp)
	statusErr := &StatusError{
		Message:       message,
		ExpectedCodes: expectedCodes,
		StatusCode:    resp.StatusCode,
	}
	return errors.Join(statusErr, messageErr)
}

func getErrorMessage(resp *http.Response) (string, error) {
	defer resp.Body.Close()
	if resp.Request.Method == http.MethodHead {
		return "", nil
	}
	contentTypes := []string{
		"text/plain",
		"text/html",
		"application/json",
		"application/xml",
	}
	if !slices.Contains(contentTypes, resp.Header.Get(HeaderContentType)) {
		return "", nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxReadBytes))
	if err != nil {
		return "", err
	}
	return string(b), err
}
