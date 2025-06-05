package mediasink

import (
	"github.com/bluenviron/gortsplib/v4/pkg/description"

	"github.com/harshabose/simple_webrtc_comm/mediasink/pkg/loopback"
	"github.com/harshabose/simple_webrtc_comm/mediasink/pkg/rtsp"
	"github.com/harshabose/simple_webrtc_comm/mediasink/pkg/socket"
	"github.com/harshabose/simple_webrtc_comm/mediasink/pkg/webrtc_js"
)

type StreamOption = func(*Stream) error

func WithRTSPHost(config *rtsp.HostConfig, description *description.Session, options ...rtsp.HostOption) StreamOption {
	return func(stream *Stream) error {
		var err error
		stream.host, err = rtsp.NewNewHost(config, description, options...)
		return err
	}
}

func WithLocalSocket(addr string, port uint16, config socket.HostConfig, f socket.OnServerMessage) StreamOption {
	return func(stream *Stream) error {
		stream.host = socket.NewHost(addr, port, config, f)
		return nil
	}
}

func WithLoopBackHost(options ...loopback.Option) StreamOption {
	return func(stream *Stream) error {
		host, err := loopback.NewLoopBack(options...)
		if err != nil {
			return err
		}

		stream.host = host
		return nil
	}
}

func WithWebRTCRestream(config webrtc_js.Config) StreamOption {
	return func(stream *Stream) error {
		stream.host = webrtc_js.NewHost(config)
		return nil
	}
}
