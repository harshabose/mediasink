package mediasink

import (
	"context"
	"errors"
)

import "github.com/pion/rtp"

type Host interface {
	Write(*rtp.Packet) error
}

type Sinks struct {
	sinks map[string]*Sink
	ctx   context.Context
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
