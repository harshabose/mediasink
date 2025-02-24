package tests

import (
	"context"
	"testing"

	"mediasink/pkg"
	"mediasink/pkg/rtsp"
)

func TestRTSP(t *testing.T) {
	ctx := context.Background()
	sinks, err := mediasink.CreateSinks(ctx)
	if err != nil {
		t.Error(err)
	}

	host, err := rtsp.CreateHost(8554, "test-sink-video", rtsp.WithH264Options(rtsp.PacketisationMode1, nil, nil))
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
