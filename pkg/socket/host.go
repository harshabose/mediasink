package socket

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/rtp"
)

type HostConfig struct {
	WriteTimeout time.Duration
	ReadTimout   time.Duration
	ConnectRetry bool
}

type OnServerMessage = func(msg []byte) error

type Host struct {
	addr   string
	port   uint16
	server *websocket.Conn
	config HostConfig
	f      OnServerMessage

	mux    sync.RWMutex
	ctx    context.Context
	cancel context.CancelFunc
}

func NewHost(addr string, port uint16, config HostConfig, f OnServerMessage) *Host {
	return &Host{
		addr:   addr,
		port:   port,
		config: config,
		f:      f,
	}
}

func (h *Host) Connect(ctx context.Context) {
	h.setSession(ctx)
	defer h.close()

loop:
	for {
		select {
		case <-h.ctx.Done():
			return
		default:
			url := fmt.Sprintf("ws://%s:%d/ws", h.addr, h.port)
			conn, _, err := websocket.Dial(h.ctx, url, nil)
			if err != nil {
				fmt.Printf("error while dailing to the socket server; err: %s\n", err.Error())
			}

			if conn == nil {
				fmt.Println("error while dialing to the socket server; conn is nil")
				return
			}

			h.setServer(conn)
			h.serverHandler()

			h.setServer(nil)

			if !h.config.ConnectRetry {
				break loop
			}

			fmt.Println("connection lost, retrying in 5 seconds...")
			time.Sleep(5 * time.Second)
		}
	}
}

func (h *Host) setSession(ctx context.Context) {
	h.mux.Lock()
	defer h.mux.Unlock()

	if h.cancel != nil {
		h.cancel()
	}

	h.ctx, h.cancel = context.WithCancel(ctx)
}

func (h *Host) setServer(conn *websocket.Conn) {
	h.mux.Lock()
	defer h.mux.Unlock()

	h.server = conn
}

func (h *Host) WriteRTP(packet *rtp.Packet) error {
	h.mux.RLock()
	defer h.mux.RUnlock()

	msg, err := packet.Marshal()
	if err != nil {
		return fmt.Errorf("error while marshalling the packet; err: %s", err.Error())
	}

	if h.server == nil {
		return fmt.Errorf("yet to connect to server. skipping message")
	}

	return h.send(h.server, websocket.MessageBinary, msg)
}

func (h *Host) Write(msg []byte) error {
	return h.send(h.server, websocket.MessageBinary, msg)
}

// func (h *Host) Write(msgType websocket.MessageType, msg []byte) error {
// 	return h.send(h.server, msgType, msg)
// }

func (h *Host) send(conn *websocket.Conn, msgType websocket.MessageType, msg []byte) error {
	if err := conn.Write(h.ctx, msgType, msg); err != nil {
		return err
	}

	return nil
}

func (h *Host) read(conn *websocket.Conn) ([]byte, error) {
	_, msg, err := conn.Read(h.ctx)
	if err != nil {
		return nil, err
	}

	return msg, nil
}

func (h *Host) readFromServer() ([]byte, error) {
	h.mux.RLock()
	defer h.mux.RUnlock()

	return h.read(h.server)
}

func (h *Host) sendToSource(msg []byte) error {
	h.mux.RLock()
	defer h.mux.RUnlock()

	if h.f == nil {
		return errors.New("server sent a message but no handler was passed to send back to source")
	}

	return h.f(msg)
}

func (h *Host) serverHandler() {
	fmt.Println("starting server routine...")
	for {
		select {
		case <-h.ctx.Done():
			return
		default:
			msg, err := h.readFromServer()
			if err != nil {
				if errors.Is(err, context.Canceled) {
					fmt.Println("error while reading from server; err:", err.Error())
					continue
				}
				fmt.Println("error while reading from server; err:", err.Error())
				return
			}

			if err := h.sendToSource(msg); err != nil {
				fmt.Println("error while sending msg back to source; err:", err.Error())
				continue
			}
		}
	}
}

func (h *Host) close() {
	h.mux.Lock()
	defer h.mux.Unlock()

	if h.cancel != nil {
		h.cancel()
	}

	if h.server != nil {
		_ = h.server.Close(websocket.StatusNormalClosure, "host closing connection")
		h.server = nil
	}
}

func (h *Host) SetOnPayloadCallback(f OnServerMessage) {
	h.f = f
}

func (h *Host) Close() error {
	h.close()

	return nil
}
