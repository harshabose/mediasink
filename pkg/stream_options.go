package mediasink

import (
	"mediasink/pkg/rtsp"
	"mediasink/pkg/udp"
)

type StreamOption = func(*Stream) error

func WithRTSPHost(port int, path string, options ...rtsp.Option) StreamOption {
	return func(stream *Stream) error {
		var err error
		stream.host, err = rtsp.CreateHost(port, path, options...)
		return err
	}
}

func WithUDPHost(port int) StreamOption {
	return func(stream *Stream) error {
		var err error
		stream.host, err = udp.CreateHost(port)
		return err
	}
}
