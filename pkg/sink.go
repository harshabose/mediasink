package mediasink

import (
	"context"
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
}

func (sink *Sink) loop() {
	for {
		select {
		case <-sink.ctx.Done():
			return
		default:

		}
	}
}
