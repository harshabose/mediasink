package socket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

const (
	ServerDown string = "SERVER_OFFLINE"
	ServerUp   string = "SERVER_ONLINE"
)

func isLoopBack(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return strings.ToLower(host) == "localhost"
	}

	return ip.IsLoopback()
}

type Config struct {
	Addr             string
	Port             uint16
	ReadTimout       time.Duration
	WriteTimout      time.Duration
	TotalConnections uint8
	KeepHosting      bool
}

type metrics struct {
	Uptime            time.Duration `json:"uptime"`
	ActiveConnections uint8         `json:"active_connections"`
	FailedConnections uint8         `json:"failed_connections"`
	TotalDataSent     int64         `json:"total_data_sent"`
	TotalDataRecvd    int64         `json:"total_data_recvd"`
	mux               sync.RWMutex
}

func (m *metrics) active() uint8 {
	m.mux.RLock()
	defer m.mux.RUnlock()

	return m.ActiveConnections
}

func (m *metrics) failed() uint8 {
	m.mux.RLock()
	defer m.mux.RUnlock()

	return m.FailedConnections
}

func (m *metrics) increaseActiveConnections() {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.ActiveConnections++
}

func (m *metrics) decreaseActiveConnections() {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.ActiveConnections--
}

func (m *metrics) increaseFailedConnections() {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.FailedConnections++
}

func (m *metrics) addDataSent(len int64) {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.TotalDataSent = m.TotalDataSent + len
}

func (m *metrics) addDataRecvd(len int64) {
	m.mux.Lock()
	defer m.mux.Unlock()

	m.TotalDataRecvd = m.TotalDataRecvd + len
}

func (m *metrics) Marshal() ([]byte, error) {
	m.mux.RLock()
	defer m.mux.RUnlock()

	return json.Marshal(m)
}

type health struct {
	State        string   `json:"state"`
	RecentErrors []string `json:"recent_errors"` // TODO: IMPLEMENT THIS LATER
	mux          sync.RWMutex
}

func (h *health) Marshal() ([]byte, error) {
	h.mux.RLock()
	defer h.mux.RUnlock()

	return json.Marshal(h)
}

type Server struct {
	httpServer *http.Server
	config     Config
	host       *websocket.Conn
	clients    map[string]*websocket.Conn

	once   sync.Once
	ctx    context.Context
	cancel context.CancelFunc
	mux    sync.RWMutex

	metrics *metrics
	health  *health
}

func NewServer(config Config) *Server {
	router := http.NewServeMux()

	server := &Server{
		httpServer: &http.Server{
			Addr:              fmt.Sprintf("%s:%d", config.Addr, config.Port),
			ReadHeaderTimeout: config.ReadTimout,
			WriteTimeout:      config.WriteTimout,
			Handler:           router,
		},
		config:  config,
		host:    nil,
		clients: make(map[string]*websocket.Conn),
		metrics: &metrics{},
		health:  &health{},
	}

	router.HandleFunc("/ws", server.wsHandler)
	router.HandleFunc("/status", server.statusHandler)
	router.HandleFunc("/metrics", server.metricsHandler)

	return server
}

// Start starts the server with the given configuration and listens for clients.
// This is a blocking call and must be called in a separate goroutine.
func (s *Server) Start(ctx context.Context) {
	s.setSession(ctx)
	defer s.close()

loop:
	for {
		select {
		case <-s.ctx.Done():
			return
		default:
			s.setStatus(ServerUp)
			if err := s.httpServer.ListenAndServe(); err != nil {
				s.setStatus(ServerDown)
				fmt.Printf("error while serving socket; err: %s\n", err.Error())
			}

			s.setStatus(ServerDown)

			if !s.config.KeepHosting {
				break loop
			}

			fmt.Println("failed to host server, retrying in 5 seconds...")
			time.Sleep(5 * time.Second)
		}
	}
}

func (s *Server) setStatus(status string) {
	s.mux.Lock()
	defer s.mux.Unlock()

	s.health.State = status
}

func (s *Server) setSession(ctx context.Context) {
	s.mux.Lock()
	defer s.mux.Unlock()

	if s.cancel != nil {
		s.cancel()
	}

	s.ctx, s.cancel = context.WithCancel(ctx)
}

func (s *Server) wsHandler(w http.ResponseWriter, req *http.Request) {
	fmt.Println("got ws request from:", req.RemoteAddr)
	if s.getHost() == nil && isLoopBack(req.RemoteAddr) {
		defer s.setHost(nil)
		conn, err := s.upgradeRequest(true, w, req)
		if err != nil {
			fmt.Println("error while handling http request; err:", err.Error())
			return
		}
		s.setHost(conn)
		s.hostHandler()
		return
	}

	conn, err := s.upgradeRequest(false, w, req)
	if err != nil {
		fmt.Println("error while handling http request; err:", err.Error())
		return
	}

	s.metrics.increaseActiveConnections()
	defer s.metrics.decreaseActiveConnections()

	fmt.Println("socket found a client with IP:", req.RemoteAddr)
	id := uuid.NewString()

	s.addClient(id, conn)
	s.clientHandler(id)
}

func (s *Server) getHost() *websocket.Conn {
	s.mux.RLock()
	defer s.mux.RUnlock()

	return s.host
}

func (s *Server) setHost(conn *websocket.Conn) {
	s.mux.Lock()
	defer s.mux.Unlock()

	fmt.Println("host set")
	s.host = conn
}

func (s *Server) upgradeRequest(asHost bool, w http.ResponseWriter, req *http.Request) (*websocket.Conn, error) {
	if !asHost {
		if s.metrics.active()+1 > s.config.TotalConnections {
			s.metrics.increaseFailedConnections()
			fmt.Printf("current number of clients: %d; max allowed: %d\n", s.metrics.active(), s.config.TotalConnections)
			return nil, errors.New("max clients reached")
		}
		s.metrics.increaseActiveConnections()
	}

	conn, err := websocket.Accept(w, req, nil)
	if err != nil {
		if !asHost {
			s.metrics.decreaseActiveConnections()
			s.metrics.increaseFailedConnections()
		}
		return nil, fmt.Errorf("error while upgrading http request to websocket; err: %s", err.Error())
	}

	return conn, nil
}

func (s *Server) statusHandler(w http.ResponseWriter, _ *http.Request) {
	s.mux.RLock()

	msg, err := s.health.Marshal()
	if err != nil {
		http.Error(w, "Failed to marshal health status", http.StatusInternalServerError)
		return
	}

	s.mux.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(msg); err != nil {
		fmt.Println("error while sending status response")
		return
	}
}

func (s *Server) metricsHandler(w http.ResponseWriter, _ *http.Request) {
	s.mux.RLock()

	msg, err := s.metrics.Marshal()
	if err != nil {
		http.Error(w, "Failed to marshal metrics", http.StatusInternalServerError)
		return
	}

	s.mux.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(msg); err != nil {
		fmt.Println("error while sending status response")
		return
	}
}

func (s *Server) hostHandler() {
	fmt.Println("starting host routine...")
	for {
		select {
		case <-s.ctx.Done():
			return
		default:
			msgType, msg, err := s.readHost()
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					fmt.Println("error while reading from host; err:", err.Error())
					continue
				}
				fmt.Println("error while reading from host; err:", err.Error())
				return
			}

			if err := s.sendAllClients(msgType, msg); err != nil {
				fmt.Println("error while sending msg from host to clients; err:", err.Error())
				continue
			}
		}
	}
}

func (s *Server) clientHandler(id string) {
	defer s.removeClient(id)

	for {
		select {
		case <-s.ctx.Done():
			return
		default:
			msgType, msg, err := s.readClient(id)
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					fmt.Println("error while reading from client; err:", err.Error())
					continue
				}
				return
			}

			if err := s.sendHost(msgType, msg); err != nil {
				fmt.Println("error while sending message from client to host; err:", err.Error())
				continue
			}
		}
	}
}

func (s *Server) removeClient(id string) {
	s.mux.Lock()
	defer s.mux.Unlock()

	delete(s.clients, id)
}

func (s *Server) addClient(id string, conn *websocket.Conn) {
	s.mux.Lock()
	defer s.mux.Unlock()

	s.clients[id] = conn
}

func (s *Server) readHost() (websocket.MessageType, []byte, error) {
	s.mux.RLock()
	host := s.host
	s.mux.RUnlock()

	if host == nil {
		return websocket.MessageBinary, nil, errors.New("host is nil")
	}

	return s.read(host)
}

func (s *Server) sendHost(msgType websocket.MessageType, msg []byte) error {
	s.mux.RLock()
	host := s.host
	s.mux.RUnlock()

	if host == nil {
		return errors.New("host not available. this is not normal. socket in invalid State")
	}

	return s.send(msgType, host, msg)
}

func (s *Server) readClient(id string) (websocket.MessageType, []byte, error) {
	s.mux.RLock()
	client, exists := s.clients[id]
	s.mux.RUnlock()

	if !exists {
		return 0, nil, errors.New("read called on unknown client")
	}

	return s.read(client)
}

func (s *Server) read(conn *websocket.Conn) (websocket.MessageType, []byte, error) {
	msgType, msg, err := conn.Read(s.ctx)
	if err != nil {
		return 0, nil, err
	}

	s.metrics.addDataRecvd(int64(len(msg)))

	return msgType, msg, nil
}

func (s *Server) sendAllClients(msgType websocket.MessageType, msg []byte) error {
	s.mux.RLock()
	clientList := make([]*websocket.Conn, 0, len(s.clients))
	for _, client := range s.clients {
		clientList = append(clientList, client)
	}
	s.mux.RUnlock()

	if len(clientList) <= 0 {
		return errors.New("no clients to send")
	}

	for _, client := range clientList {
		if err := s.send(msgType, client, msg); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) send(msgType websocket.MessageType, conn *websocket.Conn, msg []byte) error {
	if err := conn.Write(s.ctx, msgType, msg); err != nil {
		return err
	}

	s.metrics.addDataSent(int64(len(msg)))
	return nil
}

func (s *Server) close() {
	s.mux.Lock()
	defer s.mux.Unlock()

	s.cancel()
	s.host = nil
	s.clients = make(map[string]*websocket.Conn)
	s.metrics = &metrics{}
	s.health = &health{}
}

func (s *Server) Close() error {
	var err error
	s.once.Do(func() {
		s.close()

		s.mux.Lock()
		defer s.mux.Unlock()

		s.config = Config{}
		err = s.httpServer.Close()
	})

	return err
}
