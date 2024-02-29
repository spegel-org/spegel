package throttle

import (
	"fmt"
	"io"
	"time"

	"golang.org/x/time/rate"
)

const burstLimit = 1024 * 1024 * 1024 // 1GB

type Throttler struct {
	limiter *rate.Limiter
}

func NewThrottler(br Byterate) *Throttler {
	limiter := rate.NewLimiter(rate.Limit(br), burstLimit)
	limiter.AllowN(time.Now(), burstLimit)
	return &Throttler{
		limiter: limiter,
	}
}

func (t *Throttler) Writer(w io.Writer) io.Writer {
	return &writer{
		limiter: t.limiter,
		writer:  w,
	}
}

type writer struct {
	limiter *rate.Limiter
	writer  io.Writer
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
