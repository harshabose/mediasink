package interfaces

import (
	"context"
	"io"
)

type Server interface {
	Start(ctx context.Context)
	io.Closer
}
