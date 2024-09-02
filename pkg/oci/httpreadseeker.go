package oci

import (
	"io"

	"github.com/containerd/containerd/content"
	"github.com/containerd/errdefs"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
)

const maxRetry = 3

type httpReadSeeker struct {
	rc                 content.ReaderAt
	log                logr.Logger
	size               int64
	offset             int64
	errsWithNoProgress int
	closed             bool
}

var _ io.ReadSeekCloser = (*httpReadSeeker)(nil)

func newHTTPReadSeeker(log logr.Logger, reader content.ReaderAt) (io.ReadSeekCloser, error) {
	if reader == nil {
		return nil, errors.Errorf("httpReadSeeker: reader cannot be nil")
	}
	return &httpReadSeeker{
		log:  log,
		rc:   reader,
		size: reader.Size(),
	}, nil
}

func (hrs *httpReadSeeker) Read(p []byte) (n int, err error) {
	if hrs.closed {
		return 0, io.EOF
	}

	n, err = hrs.rc.ReadAt(p, hrs.offset)
	hrs.offset += int64(n)
	if n > 0 || err == nil {
		hrs.errsWithNoProgress = 0
	}
	if err == io.ErrUnexpectedEOF {
		// connection closed unexpectedly. try reconnecting.
		if n == 0 {
			hrs.errsWithNoProgress++
			if hrs.errsWithNoProgress > maxRetry {
				return // too many retries for this offset with no progress
			}
		}
		if hrs.rc != nil {
			if clsErr := hrs.rc.Close(); clsErr != nil {
				hrs.log.Error(clsErr, "httpReadSeeker: failed to close ReadCloser")
			}
			hrs.rc = nil
		}

	} else if err == io.EOF {
		// The CRI's imagePullProgressTimeout relies on responseBody.Close to
		// update the process monitor's status. If the err is io.EOF, close
		// the connection since there is no more available data.
		if hrs.rc != nil {
			if clsErr := hrs.rc.Close(); clsErr != nil {
				hrs.log.Error(clsErr, "httpReadSeeker: failed to close ReadCloser after io.EOF")
			}
			hrs.rc = nil
		}
	}
	return
}

func (hrs *httpReadSeeker) Close() error {
	if hrs.closed {
		return nil
	}
	hrs.closed = true
	if hrs.rc != nil {
		return hrs.rc.Close()
	}

	return nil
}

func (hrs *httpReadSeeker) Seek(offset int64, whence int) (int64, error) {
	if hrs.closed {
		return 0, errors.Errorf("Fetcher.Seek: closed: %v", errdefs.ErrUnavailable)
	}

	abs := hrs.offset
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs += offset
	case io.SeekEnd:
		if hrs.size == -1 {
			return 0, errors.Errorf("Fetcher.Seek: unknown size, cannot seek from end: %v", errdefs.ErrUnavailable)
		}
		abs = hrs.size + offset
	default:
		return 0, errors.Errorf("Fetcher.Seek: invalid whence: %v", errdefs.ErrInvalidArgument)
	}

	if abs < 0 {
		return 0, errors.Errorf("Fetcher.Seek: negative offset: %v", errdefs.ErrInvalidArgument)
	}

	if abs != hrs.offset {
		if hrs.rc != nil {
			if err := hrs.rc.Close(); err != nil {
				hrs.log.Error(err, "Fetcher.Seek: failed to close ReadCloser")
			}

			hrs.rc = nil
		}

		hrs.offset = abs
	}

	return hrs.offset, nil
}
