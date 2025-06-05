package rtsp

//
// import (
// 	"context"
// 	"errors"
// 	"fmt"
//
// 	"github.com/bluenviron/gortsplib/v4"
// 	"github.com/bluenviron/gortsplib/v4/pkg/description"
// 	"github.com/pion/rtp"
// )
//
// type Host struct {
// 	server      *Server
// 	rtspAddress string
// 	client      *gortsplib.Client
// 	description *description.Session
// }
//
// func CreateHost(port int, name string, options ...HostOption) (*Host, error) {
// 	host := &Host{
// 		server:      CreateServer(port),
// 		rtspAddress: fmt.Sprintf("rtsp://localhost:%d/%s", port, name),
// 		client:      &gortsplib.Client{},
// 		description: &description.Session{},
// 	}
//
// 	for _, option := range options {
// 		if err := option(host); err != nil {
// 			return nil, err
// 		}
// 	}
//
// 	if len(host.description.Medias) == 0 {
// 		return nil, errors.New("options do not set media options")
// 	}
//
// 	fmt.Printf("started video rtsp on: %s\n", host.rtspAddress)
//
// 	if err := host.server.Start(); err != nil {
// 		return nil, err
// 	}
//
// 	if err := host.client.StartRecording(host.rtspAddress, host.description); err != nil {
// 		return nil, err
// 	}
//
// 	return host, nil
// }
//
// func (host *Host) Connect(ctx context.Context) {
//
// }
//
// func (host *Host) Write(packet *rtp.Packet) error {
// 	if packet == nil {
// 		fmt.Println("nil packet, not sending to sink host")
// 		return nil
// 	}
// 	expPayloadType := host.description.Medias[0].Formats[0].PayloadType()
// 	if packet.PayloadType != expPayloadType {
// 		fmt.Printf("expected payload type '%d' but got '%d'\n", expPayloadType, packet.PayloadType)
// 		packet.PayloadType = expPayloadType
// 	}
// 	return host.client.WritePacketRTP(host.description.Medias[0], packet)
// }
//
// func (host *Host) Close() error {
// 	host.client.Close()
// 	host.server.Close()
//
// 	return nil
// }
