package throttle

import (
	"fmt"
	"io"
	"time"

	"golang.org/x/time/rate"
)

type writer struct {
	limiter *rate.Limiter
	writer  io.Writer
}

func NewWriter(w io.Writer, limiter *rate.Limiter) io.Writer {
	return &writer{
		limiter: limiter,
		writer:  w,
	}
}

func (w *writer) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	if err != nil {
		return 0, err
	}
	r := w.limiter.ReserveN(time.Now(), n)
	if !r.OK() {
		return n, fmt.Errorf("write size %d exceeds limiters burst %d", n, w.limiter.Burst())
	}
	time.Sleep(r.Delay())
	return n, nil
}
