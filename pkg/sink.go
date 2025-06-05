package mediasink

import (
	"context"
	"fmt"
	"time"

	"github.com/pion/webrtc/v4"
)

type Sink struct {
	track  *webrtc.TrackRemote
	stream *Stream
	ctx    context.Context
}

func CreateSink(ctx context.Context, stream *Stream) *Sink {
	return &Sink{
		stream: stream,
		ctx:    ctx,
	}
}

func (sink *Sink) GetStream() *Stream {
	return sink.stream
}

func (sink *Sink) SetTrack(track *webrtc.TrackRemote) {
	sink.track = track
}

func (sink *Sink) SetOnPayloadCallback(f func([]byte) error) {
	s, ok := sink.stream.host.(CanCallBackPayload)
	if !ok {
		return
	}

	s.SetOnPayloadCallback(f)
}

func (sink *Sink) Start() {
	sink.stream.Start()
	go sink.loop()
	fmt.Println("media sink sink started")
}

func (sink *Sink) loop() {
	defer sink.close()
loop:
	for {
		select {
		case <-sink.ctx.Done():
			return
		default:
			if sink.track == nil {
				time.Sleep(10 * time.Millisecond)
			}
			packet, _, err := sink.track.ReadRTP()
			if err != nil {
				time.Sleep(10 * time.Millisecond)
				continue loop
			}

			if err := sink.stream.WriteRTPPacket(packet); err != nil {
				continue loop
			}
		}
	}
}

func (sink *Sink) close() {}
