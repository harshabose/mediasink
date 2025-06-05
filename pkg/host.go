package mediasink

import (
	"context"
	"io"

	"github.com/pion/rtp"
)

type Host interface {
	Connect(context.Context)
	WriteRTP(*rtp.Packet) error
	Write([]byte) error
	io.Closer
}

type CanCallBackPayload interface {
	SetOnPayloadCallback(f func([]byte) error)
}
