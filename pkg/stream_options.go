package mediasink

import (
	"github.com/harshabose/simple_webrtc_comm/mediasink/pkg/rtsp"
)

type StreamOption = func(*Stream) error

func WithRTSPHost(port int, path string, options ...rtsp.Option) StreamOption {
	return func(stream *Stream) error {
		var err error
		stream.host, err = rtsp.CreateHost(port, path, options...)
		return err
	}
}
