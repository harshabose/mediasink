package mediasink

import (
	"context"
	"fmt"
	"time"

	"github.com/harshabose/tools/buffer/pkg"
	"github.com/pion/rtp"

	"mediasink/internal"
)

type Stream struct {
	host   Host
	buffer buffer.BufferWithGenerator[rtp.Packet]
	ctx    context.Context
}

func CreateStream(ctx context.Context, bufferSize int, options ...StreamOption) (*Stream, error) {
	stream := &Stream{ctx: ctx, buffer: buffer.CreateChannelBuffer[rtp.Packet](ctx, bufferSize, internal.CreateRTPPool())}

	for _, option := range options {
		if err := option(stream); err != nil {
			return nil, err
		}
	}

	return stream, nil
}

func (stream *Stream) WriteRTPPacket(packet *rtp.Packet) error {
	ctx, cancel := context.WithTimeout(stream.ctx, time.Second)
	defer cancel()

	return stream.buffer.Push(ctx, packet)
}

func (stream *Stream) Start() {
	go stream.loop()
}

func (stream *Stream) loop() {
	for {
		select {
		case <-stream.ctx.Done():
			return
		case packet := <-stream.buffer.GetChannel():
			if err := stream.host.Write(packet); err != nil {
				fmt.Println(err)
			}
		}
	}
}
