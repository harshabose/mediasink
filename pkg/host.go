package interfaces

import (
	"context"
	"io"

	"github.com/pion/rtp"
)

type Host interface {
	Connect(context.Context)
	Write(*rtp.Packet) error
	io.Closer
}
