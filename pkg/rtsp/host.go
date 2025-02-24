package rtsp

import (
	"errors"
	"fmt"
	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/pion/rtp"
)

type Host struct {
	server      *Server
	rtspAddress string
	client      *gortsplib.Client
	description *description.Session
}

func CreateHost(port int, name string, options ...Option) (*Host, error) {
	host := &Host{
		server:      CreateServer(port),
		rtspAddress: fmt.Sprintf("rtsp://localhost:%d/%s", port, name),
		client:      &gortsplib.Client{},
		description: &description.Session{},
	}

	for _, option := range options {
		if err := option(host); err != nil {
			return nil, err
		}
	}

	if len(host.description.Medias) == 0 {
		return nil, errors.New("options do not set media options")
	}

	fmt.Printf("started video rtsp on: %s\n", host.rtspAddress)

	go func() {
		panic(host.server.server.StartAndWait())
	}()

	return host, nil
}

func (host *Host) Write(packet *rtp.Packet) error {
	return host.client.WritePacketRTP(host.description.Medias[0], packet)
}
