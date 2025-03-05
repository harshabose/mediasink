package rtsp

import (
	"fmt"
	"sync"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/pion/rtp"
)

type Server struct {
	server    *gortsplib.Server
	mutex     sync.Mutex
	stream    *gortsplib.ServerStream
	publisher *gortsplib.ServerSession
}

func CreateServer(port int) *Server {
	handler := &Server{}
	handler.server = &gortsplib.Server{
		Handler:           handler,
		RTSPAddress:       fmt.Sprintf(":%d", port),
		WriteQueueSize:    4096,
		UDPRTPAddress:     ":8000",
		UDPRTCPAddress:    ":8001",
		MulticastIPRange:  "224.1.0.0/16",
		MulticastRTPPort:  8002,
		MulticastRTCPPort: 8003,
	}

	return handler
}

func (server *Server) Start() error {
	if err := server.server.Start(); err != nil {
		return err
	}
	return nil
}

func (server *Server) GetServer() *gortsplib.Server {
	return server.server
}

func (server *Server) OnConnOpen(ctx *gortsplib.ServerHandlerOnConnOpenCtx) {
	fmt.Printf("conn opened\n")
}

func (server *Server) OnConnClose(ctx *gortsplib.ServerHandlerOnConnCloseCtx) {
	fmt.Printf("conn closed (%v)\n", ctx.Error)
}

func (server *Server) OnSessionOpen(ctx *gortsplib.ServerHandlerOnSessionOpenCtx) {
	fmt.Printf("session opened\n")
}

func (server *Server) OnSessionClose(ctx *gortsplib.ServerHandlerOnSessionCloseCtx) {
	fmt.Printf("session closed\n")

	server.mutex.Lock()
	defer server.mutex.Unlock()

	if server.stream != nil && ctx.Session == server.publisher {
		server.stream.Close()
		server.stream = nil
	}
}

func (server *Server) OnDescribe(ctx *gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	fmt.Printf("describe request\n")

	server.mutex.Lock()
	defer server.mutex.Unlock()

	if server.stream == nil {
		return &base.Response{
			StatusCode: base.StatusNotFound,
		}, nil, nil
	}

	return &base.Response{
		StatusCode: base.StatusOK,
	}, server.stream, nil
}

func (server *Server) OnAnnounce(ctx *gortsplib.ServerHandlerOnAnnounceCtx) (*base.Response, error) {
	fmt.Printf("announce request\n")

	server.mutex.Lock()
	defer server.mutex.Unlock()

	if server.stream != nil {
		server.stream.Close()
		server.publisher.Close()
	}

	server.stream = gortsplib.NewServerStream(server.server, ctx.Description)
	server.publisher = ctx.Session

	return &base.Response{
		StatusCode: base.StatusOK,
	}, nil
}

func (server *Server) OnSetup(ctx *gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	fmt.Printf("setup request\n")

	if server.stream == nil {
		return &base.Response{
			StatusCode: base.StatusNotFound,
		}, nil, nil
	}

	return &base.Response{
		StatusCode: base.StatusOK,
	}, server.stream, nil
}

func (server *Server) OnPlay(ctx *gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	fmt.Printf("play request\n")

	return &base.Response{
		StatusCode: base.StatusOK,
	}, nil
}

func (server *Server) OnRecord(ctx *gortsplib.ServerHandlerOnRecordCtx) (*base.Response, error) {
	fmt.Printf("record request\n")

	ctx.Session.OnPacketRTPAny(func(media *description.Media, format format.Format, pkt *rtp.Packet) {
		if err := server.stream.WritePacketRTP(media, pkt); err != nil {
			fmt.Printf("error while writing rtp packet to rtsp stream: %s. skipping...", err.Error())
		}
	})

	return &base.Response{
		StatusCode: base.StatusOK,
	}, nil
}

func (server *Server) Stop() {
	server.server.Close()
}
