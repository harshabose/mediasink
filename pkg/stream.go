package mediasink

import (
	"context"
	"fmt"
	"time"

	"github.com/pion/rtp"

	"github.com/harshabose/tools/buffer/pkg"

	"github.com/harshabose/simple_webrtc_comm/mediasink/internal"
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
	go stream.host.Connect(stream.ctx)
	go stream.loop()
	fmt.Println("media sink stream started")
}

func (stream *Stream) loop() {
	defer stream.close()

	for {
		select {
		case <-stream.ctx.Done():
			return
		default:
			packet, err := stream.getPacket()
			if err != nil {
				continue
			}

			if err := stream.host.WriteRTP(packet); err != nil {
				continue
			}
			stream.buffer.PutBack(packet)
		}
	}
}

func (stream *Stream) GetHost() Host {
	return stream.host
}

func (stream *Stream) getPacket() (*rtp.Packet, error) {
	ctx, cancel := context.WithTimeout(stream.ctx, 50*time.Millisecond)
	defer cancel()

	return stream.buffer.Pop(ctx)
}

func (stream *Stream) close() {
	_ = stream.host.Close()
}
