package mediasink

import (
	"context"
	"errors"

	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"
)

import "github.com/pion/rtp"

type Host interface {
	Write(*rtp.Packet) error
}

type Sinks struct {
	mediaEngine         *webrtc.MediaEngine
	interceptorRegistry *interceptor.Registry
	sinks               map[string]*Sink
	ctx                 context.Context
}

func CreateSinks(ctx context.Context, mediaEngine *webrtc.MediaEngine, interceptorRegistry *interceptor.Registry, options ...SinksOptions) (*Sinks, error) {
	sinks := &Sinks{
		mediaEngine:         mediaEngine,
		interceptorRegistry: interceptorRegistry,
		sinks:               make(map[string]*Sink),
		ctx:                 ctx,
	}

	for _, option := range options {
		if err := option(sinks); err != nil {
			return nil, err
		}
	}

	return sinks, nil
}

func (sinks *Sinks) CreateSink(id string, options ...StreamOption) error {
	var (
		stream *Stream
		err    error
	)

	if _, exists := sinks.sinks[id]; exists {
		return errors.New("track already exists")
	}
	if stream, err = CreateStream(sinks.ctx, 60, options...); err != nil {
		return err
	}
	sinks.sinks[id] = CreateSink(sinks.ctx, stream)

	return nil
}

func (sinks *Sinks) GetSink(id string) (*Sink, error) {
	if _, exists := sinks.sinks[id]; !exists {
		return nil, errors.New("sink does not exist")
	}
	return sinks.sinks[id], nil
}
