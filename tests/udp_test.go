package tests

import (
	"context"
	"testing"

	"github.com/harshabose/simple_webrtc_comm/mediasink/pkg"
	"github.com/harshabose/simple_webrtc_comm/mediasink/pkg/udp"
)

func TestUDP(t *testing.T) {
	ctx := context.Background()
	sinks, err := mediasink.CreateSinks(ctx)
	if err != nil {
		t.Error(err)
	}

	host, err := udp.CreateHost(8554)
	if err != nil {
		t.Error(err)
	}

	if err := sinks.CreateSink("test-sink", mediasink.WithHost(host)); err != nil {
		t.Error(err)
	}

	gotSink, err := sinks.GetSink("test-sink")
	if err != nil {
		t.Error(err)
	}

	gotSink.Start()
}
