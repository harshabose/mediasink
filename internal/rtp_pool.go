package internal

import (
	"github.com/pion/rtp"
	"sync"
)

type RTPPool struct {
	pool sync.Pool
}

func CreateRTPPool() *RTPPool {
	return &RTPPool{
		pool: sync.Pool{
			New: func() any {
				return &rtp.Packet{}
			},
		},
	}
}

func (pool *RTPPool) Get() *rtp.Packet {
	packet, ok := pool.pool.Get().(*rtp.Packet)

	if packet == nil || !ok {
		return &rtp.Packet{}
	}
	return packet
}

func (pool *RTPPool) Put(packet *rtp.Packet) {
	if packet == nil {
		return
	}
	pool.pool.Put(packet)
}

func (pool *RTPPool) Release() {
	for {
		packet, ok := pool.pool.Get().(*rtp.Packet)
		if !ok {
			continue
		}
		if packet == nil {
			break
		}

		packet = nil
	}
}
