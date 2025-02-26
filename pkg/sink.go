package mediasink

import (
	"context"
	"fmt"

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

func (sink *Sink) SetTrack(track *webrtc.TrackRemote) {
	sink.track = track
}

func (sink *Sink) Start() {
	sink.stream.Start()
	go sink.loop()
	fmt.Println("media sink sink started")
}

func (sink *Sink) loop() {
loop:
	for {
		select {
		case <-sink.ctx.Done():
			return
		default:
			packet, _, err := sink.track.ReadRTP()
			if err != nil {
				continue loop
			}

			if err := sink.stream.WriteRTPPacket(packet); err != nil {
				continue loop
			}
		}
	}
}
