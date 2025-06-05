package rtsp

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/pion/rtp"
)

type ClientSession struct {
	ID         string
	Session    *gortsplib.ServerSession
	RemoteAddr string
	IsLocal    bool
	ConnTime   time.Time
	LastActive time.Time
}

type StreamInfo struct {
	Stream              *gortsplib.ServerStream
	Publisher           *gortsplib.ServerSession
	PublisherLastActive time.Time
	Description         *description.Session
	Clients             map[string]*ClientSession
	CreatedAt           time.Time
	mutex               sync.RWMutex
}

func (si *StreamInfo) AddClient(client *ClientSession) {
	si.mutex.Lock()
	defer si.mutex.Unlock()
	si.Clients[client.ID] = client
}

func (si *StreamInfo) RemoveClient(clientID string) {
	si.mutex.Lock()
	defer si.mutex.Unlock()
	delete(si.Clients, clientID)
}

func (si *StreamInfo) GetClientCount() int {
	si.mutex.RLock()
	defer si.mutex.RUnlock()
	return len(si.Clients)
}

func (si *StreamInfo) GetClients() []*ClientSession {
	si.mutex.RLock()
	defer si.mutex.RUnlock()
	clients := make([]*ClientSession, 0, len(si.Clients))
	for _, client := range si.Clients {
		clients = append(clients, client)
	}
	return clients
}

type NewServer struct {
	server *gortsplib.Server
	config *Config

	// Stream management
	streams map[string]*StreamInfo
	mutex   sync.RWMutex

	// Context management
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Metrics
	// TODO: SHIFT TO METRICS STRUCT AND EXPOSE A SIMPLE API ENDPOINT
	totalConnections int64
}

type Config struct {
	Port                    int
	MaxClients              int
	MaxStreams              int
	ReadTimeout             time.Duration
	WriteTimeout            time.Duration
	PublisherSessionTimeout time.Duration
	ClientSessionTimeout    time.Duration
	AllowLocalOnly          bool
	UDPRTPAddress           string
	UDPRTCPAddress          string
	MulticastIPRange        string
	MulticastRTPPort        int
	MulticastRTCPPort       int
	WriteQueueSize          int
}

func DefaultConfig() *Config {
	return &Config{
		Port:                    8554,
		MaxClients:              100,
		MaxStreams:              10,
		ReadTimeout:             10 * time.Second,
		WriteTimeout:            10 * time.Second,
		PublisherSessionTimeout: 60 * time.Second,
		ClientSessionTimeout:    60 * time.Second,
		AllowLocalOnly:          false,
		UDPRTPAddress:           ":8000",
		UDPRTCPAddress:          ":8001",
		MulticastIPRange:        "224.1.0.0/16",
		MulticastRTPPort:        8002,
		MulticastRTCPPort:       8003,
		WriteQueueSize:          4096,
	}
}

func NewNewServer(config *Config) *NewServer {
	if config == nil {
		config = DefaultConfig()
	}

	server := &NewServer{
		config:  config,
		streams: make(map[string]*StreamInfo),
	}

	server.server = &gortsplib.Server{
		Handler:           server,
		RTSPAddress:       fmt.Sprintf("0.0.0.0:%d", config.Port),
		ReadTimeout:       config.ReadTimeout,
		WriteTimeout:      config.WriteTimeout,
		WriteQueueSize:    config.WriteQueueSize,
		UDPRTPAddress:     config.UDPRTPAddress,
		UDPRTCPAddress:    config.UDPRTCPAddress,
		MulticastIPRange:  config.MulticastIPRange,  // NOTE: NOT NEEDED
		MulticastRTPPort:  config.MulticastRTPPort,  // NOTE: NOT NEEDED
		MulticastRTCPPort: config.MulticastRTCPPort, // NOTE: NOT NEEDED
	}

	return server
}

func (s *NewServer) Start(ctx context.Context) {
	s.setSession(ctx)
	fmt.Printf("Starting RTSP server on port %d\n", s.config.Port)

	if err := s.server.Start(); err != nil {
		fmt.Println("failed to start RTSP server:", err.Error())
	}

	// Start background tasks
	s.wg.Add(2)
	go s.cleanupRoutine()
	go s.metricsRoutine()

	fmt.Println("RTSP server started successfully")
}

func (s *NewServer) setSession(ctx context.Context) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.cancel != nil {
		s.cancel()
	}

	s.ctx, s.cancel = context.WithCancel(ctx)
}

func (s *NewServer) Close() error {
	fmt.Println("Stopping RTSP server...")

	// Close all streams
	s.mutex.Lock()
	s.cancel()

	for path, streamInfo := range s.streams {
		if streamInfo.Stream != nil {
			streamInfo.Stream.Close()
		}
		if streamInfo.Publisher != nil {
			streamInfo.Publisher.Close()
		}
		fmt.Printf("Closed stream: %s\n", path)
	}
	s.streams = make(map[string]*StreamInfo)
	s.mutex.Unlock()

	// Stop server
	s.server.Close()

	// Wait for background routines
	s.wg.Wait()

	s.totalConnections = 0

	fmt.Println("RTSP server stopped")
	return nil
}

func (s *NewServer) cleanupRoutine() {
	defer s.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.cleanupInactiveSessions()
		}
	}
}

func (s *NewServer) metricsRoutine() {
	defer s.wg.Done()
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.logMetrics()
		}
	}
}

func (s *NewServer) cleanupInactiveSessions() {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	now := time.Now()
	for path, streamInfo := range s.streams {
		streamInfo.mutex.Lock()

		publisherInactive := now.Sub(streamInfo.PublisherLastActive) > s.config.PublisherSessionTimeout

		if publisherInactive {
			// Publisher inactive - close stream
			streamInfo.mutex.Unlock()

			fmt.Printf("Publisher inactive for stream %s, closing stream\n", path)
			if streamInfo.Stream != nil {
				streamInfo.Stream.Close()
			}
			if streamInfo.Publisher != nil {
				streamInfo.Publisher.Close()
			}
			delete(s.streams, path)
			continue
		}

		toRemove := make([]string, 0)
		for clientID, client := range streamInfo.Clients {
			if now.Sub(client.LastActive) > s.config.ClientSessionTimeout {
				toRemove = append(toRemove, clientID)
			}
		}

		for _, clientID := range toRemove {
			delete(streamInfo.Clients, clientID)
			fmt.Printf("Removed inactive client %s from stream %s\n", clientID, path)
		}

		streamInfo.mutex.Unlock()
	}
}

func (s *NewServer) logMetrics() {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	totalClients := 0
	for path, streamInfo := range s.streams {
		clientCount := streamInfo.GetClientCount()
		totalClients += clientCount
		fmt.Printf("Stream %s: %d clients\n", path, clientCount)
	}

	fmt.Printf("Total streams: %d, Total clients: %d\n", len(s.streams), totalClients)
}

func (s *NewServer) isLocalhost(remoteAddr string) bool {
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

func (s *NewServer) validateConnection(remoteAddr string) error {
	if s.config.AllowLocalOnly && !s.isLocalhost(remoteAddr) {
		return fmt.Errorf("only localhost connections allowed")
	}

	// Check max clients across all streams
	s.mutex.RLock()
	totalClients := 0
	for _, streamInfo := range s.streams {
		totalClients += streamInfo.GetClientCount()
	}
	s.mutex.RUnlock()

	if totalClients >= s.config.MaxClients {
		return fmt.Errorf("maximum client limit reached")
	}

	return nil
}

// OnConnOpen is called when a publisher/client completes the TCP handshake
func (s *NewServer) OnConnOpen(ctx *gortsplib.ServerHandlerOnConnOpenCtx) {
	if err := s.validateConnection(ctx.Conn.NetConn().RemoteAddr().String()); err != nil {
		fmt.Printf("Connection rejected: %v\n", err)
		ctx.Conn.Close()
		return
	}

	// TODO: MAYBE REMOVE totalConnections
	s.totalConnections++
	fmt.Printf("Connection opened from %s (total: %d)\n", ctx.Conn.NetConn().RemoteAddr(), s.totalConnections)
}

// OnConnClose is called when a publisher/client disconnects TCP
func (s *NewServer) OnConnClose(ctx *gortsplib.ServerHandlerOnConnCloseCtx) {
	s.totalConnections--
	fmt.Printf("Connection closed from %s: %v\n", ctx.Conn.NetConn().RemoteAddr(), ctx.Error)
}

// OnSessionOpen is called after OnConnOpen and indicates RTSP session start.
func (s *NewServer) OnSessionOpen(ctx *gortsplib.ServerHandlerOnSessionOpenCtx) {
	clientID := fmt.Sprintf("%s-%d", ctx.Conn.NetConn().RemoteAddr(), time.Now().UnixNano())
	fmt.Printf("Session opened: %s from %s\n", clientID, ctx.Conn.NetConn().RemoteAddr())

	// Store client info in session
	ctx.Session.SetUserData(map[string]interface{}{
		"clientID":   clientID,
		"remoteAddr": ctx.Conn.NetConn().RemoteAddr().String(),
		"isLocal":    s.isLocalhost(ctx.Conn.NetConn().RemoteAddr().String()),
		"connTime":   time.Now(),
	})
}

// OnSessionClose is called after OnConnClose and indicates RTSP session close.
func (s *NewServer) OnSessionClose(ctx *gortsplib.ServerHandlerOnSessionCloseCtx) {
	userData := ctx.Session.UserData()
	if userData == nil {
		return
	}

	userMap, ok := userData.(map[string]interface{})
	if !ok {
		return
	}

	clientID, _ := userMap["clientID"].(string)
	fmt.Printf("Session closed: %s\n", clientID)

	s.mutex.Lock()
	defer s.mutex.Unlock()

	// Remove client from all streams
	for path, streamInfo := range s.streams {
		streamInfo.RemoveClient(clientID)

		// If this was the publisher, close the stream
		if streamInfo.Publisher == ctx.Session {
			if streamInfo.Stream != nil {
				streamInfo.Stream.Close()
			}
			delete(s.streams, path)
			fmt.Printf("Publisher disconnected, stream %s closed\n", path)
		}
	}
}

func (s *NewServer) OnDescribe(ctx *gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	path := ctx.Path
	fmt.Printf("Describe request for path: %s from %s\n", path, ctx.Conn.NetConn().RemoteAddr())

	s.mutex.RLock()
	streamInfo, exists := s.streams[path]
	s.mutex.RUnlock()

	if !exists || streamInfo.Stream == nil {
		fmt.Printf("Stream not found: %s\n", path)
		return &base.Response{
			StatusCode: base.StatusNotFound,
		}, nil, nil
	}

	return &base.Response{
		StatusCode: base.StatusOK,
	}, streamInfo.Stream, nil
}

func (s *NewServer) OnAnnounce(ctx *gortsplib.ServerHandlerOnAnnounceCtx) (*base.Response, error) {
	path := ctx.Path
	fmt.Printf("Announce request for path: %s from %s\n", path, ctx.Conn.NetConn().RemoteAddr())

	s.mutex.Lock()
	defer s.mutex.Unlock()

	// Check max streams limit
	if len(s.streams) >= s.config.MaxStreams {
		fmt.Println("Maximum stream limit reached")
		return &base.Response{
			StatusCode: base.StatusServiceUnavailable,
		}, nil
	}

	// Close existing stream if it exists
	if existingStream, exists := s.streams[path]; exists {
		if existingStream.Stream != nil {
			existingStream.Stream.Close()
		}
		if existingStream.Publisher != nil {
			existingStream.Publisher.Close()
		}
		fmt.Printf("Replaced existing stream: %s\n", path)
	}

	// Create new stream
	stream := gortsplib.NewServerStream(s.server, ctx.Description)
	streamInfo := &StreamInfo{
		Stream:              stream,
		Publisher:           ctx.Session,
		PublisherLastActive: time.Now(),
		Description:         ctx.Description,
		Clients:             make(map[string]*ClientSession),
		CreatedAt:           time.Now(),
	}

	s.streams[path] = streamInfo
	fmt.Printf("Stream created: %s\n", path)

	return &base.Response{
		StatusCode: base.StatusOK,
	}, nil
}

func (s *NewServer) OnSetup(ctx *gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	path := ctx.Path
	fmt.Printf("Setup request for path: %s from %s\n", path, ctx.Conn.NetConn().RemoteAddr())

	s.mutex.RLock()
	streamInfo, exists := s.streams[path]
	s.mutex.RUnlock()

	if !exists || streamInfo.Stream == nil {
		fmt.Printf("Stream not found for setup: %s\n", path)
		return &base.Response{
			StatusCode: base.StatusNotFound,
		}, nil, nil
	}

	isPublisher := streamInfo.Publisher == ctx.Session

	if !isPublisher {
		// Add client to stream
		userData := ctx.Session.UserData()
		if userData != nil {
			if userMap, ok := userData.(map[string]interface{}); ok {
				clientID, _ := userMap["clientID"].(string)
				remoteAddr, _ := userMap["remoteAddr"].(string)
				isLocal, _ := userMap["isLocal"].(bool)
				connTime, _ := userMap["connTime"].(time.Time)

				client := &ClientSession{
					ID:         clientID,
					Session:    ctx.Session,
					RemoteAddr: remoteAddr,
					IsLocal:    isLocal,
					ConnTime:   connTime,
					LastActive: time.Now(),
				}

				streamInfo.AddClient(client)
				fmt.Printf("Client %s added to stream %s\n", clientID, path)
			}
		}
	}

	return &base.Response{
		StatusCode: base.StatusOK,
	}, streamInfo.Stream, nil
}

func (s *NewServer) OnPlay(ctx *gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	path := ctx.Path
	fmt.Printf("Play request for path: %s from %s\n", path, ctx.Conn.NetConn().RemoteAddr())

	// Update client's last active time
	s.mutex.RLock()
	if streamInfo, exists := s.streams[path]; exists {
		userData := ctx.Session.UserData()
		if userData != nil {
			if userMap, ok := userData.(map[string]interface{}); ok {
				clientID, _ := userMap["clientID"].(string)
				streamInfo.mutex.Lock()
				if client, exists := streamInfo.Clients[clientID]; exists {
					client.LastActive = time.Now()
				}
				streamInfo.mutex.Unlock()
			}
		}
	}
	s.mutex.RUnlock()

	return &base.Response{
		StatusCode: base.StatusOK,
	}, nil
}

func (s *NewServer) OnRecord(ctx *gortsplib.ServerHandlerOnRecordCtx) (*base.Response, error) {
	path := ctx.Path
	fmt.Printf("Record request for path: %s from %s\n", path, ctx.Conn.NetConn().RemoteAddr())

	s.mutex.RLock()
	streamInfo, exists := s.streams[path]
	s.mutex.RUnlock()

	if !exists || streamInfo.Stream == nil {
		fmt.Printf("Stream not found for record: %s\n", path)
		return &base.Response{
			StatusCode: base.StatusNotFound,
		}, nil
	}

	// Set up packet handling
	ctx.Session.OnPacketRTPAny(func(media *description.Media, format format.Format, pkt *rtp.Packet) {
		if err := streamInfo.Stream.WritePacketRTP(media, pkt); err != nil {
			fmt.Printf("Error writing RTP packet to stream %s: %v\n", path, err)
		}

		// TODO: CONSIDER NOT ADDING PUBLISHER TO CLIENTS LIST AS THEIR LIFELINE NEEDS TO BE SEPARATE FROM OTHER CLIENTS
		streamInfo.mutex.Lock()
		now := time.Now()

		streamInfo.PublisherLastActive = now

		for _, client := range streamInfo.Clients {
			client.LastActive = now
		}
		streamInfo.mutex.Unlock()
	})

	return &base.Response{
		StatusCode: base.StatusOK,
	}, nil
}

// GetStreamInfo returns information about a specific stream
func (s *NewServer) GetStreamInfo(path string) (*StreamInfo, bool) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	streamInfo, exists := s.streams[path]
	return streamInfo, exists
}

// GetAllStreams returns information about all active streams
func (s *NewServer) GetAllStreams() map[string]*StreamInfo {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	result := make(map[string]*StreamInfo)
	for path, streamInfo := range s.streams {
		result[path] = streamInfo
	}
	return result
}
