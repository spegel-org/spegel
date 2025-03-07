package buffer

import "sync"

type BufferPool struct {
	pool sync.Pool
}

func NewBufferPool() *BufferPool {
	return &BufferPool{
		pool: sync.Pool{
			New: func() any {
				return make([]byte, 32*1024)
			},
		},
	}
}

func (p *BufferPool) Get() []byte {
	//nolint: errcheck // Ignore
	return p.pool.Get().([]byte)
}

func (p *BufferPool) Put(b []byte) {
	//nolint: staticcheck // false positive
	p.pool.Put(b)
}
