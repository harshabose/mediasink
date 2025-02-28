package mediasink

import (
	"context"
	"errors"
)

type Sinks struct {
	sinks map[string]*Sink
	ctx   context.Context
}

func CreateSinks(ctx context.Context, options ...SinksOptions) (*Sinks, error) {
	sinks := &Sinks{
		sinks: make(map[string]*Sink),
		ctx:   ctx,
	}

	for _, option := range options {
		if err := option(sinks); err != nil {
			return nil, err
		}
	}

	return sinks, nil
}

func (sinks *Sinks) CreateSink(id string, options ...StreamOption) (*Sink, error) {
	var (
		stream *Stream
		err    error
	)

	if _, exists := sinks.sinks[id]; exists {
		return nil, errors.New("track already exists")
	}
	if stream, err = CreateStream(sinks.ctx, 60, options...); err != nil {
		return nil, err
	}
	sink := CreateSink(sinks.ctx, stream)
	sinks.sinks[id] = sink

	return sink, nil
}

func (sinks *Sinks) GetSink(id string) (*Sink, error) {
	if _, exists := sinks.sinks[id]; !exists {
		return nil, errors.New("sink does not exist")
	}
	return sinks.sinks[id], nil
}
