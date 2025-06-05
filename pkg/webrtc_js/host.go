package webrtc_js

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/intervalpli"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

type (
	SDPStatus    string
	ServerStatus string
)

const (
	SDPNotInitialised   SDPStatus = "not-initialised"
	SDPRequested        SDPStatus = "requested"
	SDPOfferSent        SDPStatus = "offer-sent"
	SDPWaitingForAnswer SDPStatus = "waiting-for-answer"
	SDPConnected        SDPStatus = "connected"
	SDPFailed           SDPStatus = "failed"

	ServerDown   ServerStatus = "SERVER_OFFLINE"
	ServerOnline ServerStatus = "SERVER_ONLINE"
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

type ClientSession struct {
	peerConnection  *webrtc.PeerConnection
	videoTrack      *webrtc.TrackLocalStaticRTP
	sdpStatus       SDPStatus
	ttl             time.Duration
	connectionState webrtc.PeerConnectionState
	rtpChan         chan *rtp.Packet
	lastActivity    time.Time
	mu              sync.RWMutex
}

func (cs *ClientSession) updateActivity() {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.lastActivity = time.Now()
}

func (cs *ClientSession) getLastActivity() time.Time {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.lastActivity
}

func (cs *ClientSession) isExpired() bool {
	return time.Since(cs.getLastActivity()) > cs.ttl
}

type Config struct {
	Addr             string
	Port             uint16
	ReadTimout       time.Duration
	WriteTimout      time.Duration
	KeepHosting      bool
	TotalConnections uint8
	AllowLocalOnly   bool
	ClientTTL        time.Duration
}

type metrics struct {
	Uptime            time.Duration `json:"uptime"`
	ActiveConnections uint8         `json:"active_connections"`
	FailedConnections uint8         `json:"failed_connections"`
	TotalDataSent     int64         `json:"total_data_sent"`
	mux               sync.RWMutex
	startTime         time.Time
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
	if m.ActiveConnections > 0 {
		m.ActiveConnections--
	}
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

func (m *metrics) Marshal() ([]byte, error) {
	m.mux.Lock()
	defer m.mux.Unlock()
	m.Uptime = time.Since(m.startTime)
	return json.Marshal(m)
}

type health struct {
	State        ServerStatus `json:"state"`
	RecentErrors []string     `json:"recent_errors"`
	mux          sync.RWMutex
}

func (h *health) addError(err string) {
	h.mux.Lock()
	defer h.mux.Unlock()

	h.RecentErrors = append(h.RecentErrors, fmt.Sprintf("%s: %s", time.Now().Format(time.RFC3339), err))

	// Keep only last 10 errors
	if len(h.RecentErrors) > 10 {
		h.RecentErrors = h.RecentErrors[1:]
	}
}

func (h *health) Marshal() ([]byte, error) {
	h.mux.RLock()
	defer h.mux.RUnlock()
	return json.Marshal(h)
}

type Request struct {
	ID        string        `json:"id"`
	Timestamp time.Time     `json:"timestamp"`
	TTL       time.Duration `json:"ttl"`
}

type Offer struct {
	ID        string                    `json:"id"`
	Offer     webrtc.SessionDescription `json:"offer"`
	Timestamp time.Time                 `json:"timestamp"`
}

type Answer struct {
	ID     string                    `json:"id"`
	Answer webrtc.SessionDescription `json:"answer"`
}

type Host struct {
	httpServer *http.Server
	config     Config
	clients    map[string]*ClientSession
	webrtcAPI  *webrtc.API

	once   sync.Once
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mux    sync.RWMutex

	metrics *metrics
	health  *health
}

func NewHost(config Config) *Host {
	router := http.NewServeMux()

	// Set default TTL if not specified
	if config.ClientTTL == 0 {
		config.ClientTTL = 5 * time.Minute
	}

	// Create WebRTC API with MediaEngine and Interceptors
	m := &webrtc.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		panic(fmt.Sprintf("Failed to register codecs: %v", err))
	}

	i := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, i); err != nil {
		panic(fmt.Sprintf("Failed to register interceptors: %v", err))
	}

	// Add PLI interceptor for key frame requests
	intervalPliFactory, err := intervalpli.NewReceiverInterceptor()
	if err != nil {
		panic(fmt.Sprintf("Failed to create PLI interceptor: %v", err))
	}
	i.Add(intervalPliFactory)

	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(i))

	server := &Host{
		httpServer: &http.Server{
			Addr:              fmt.Sprintf("%s:%d", config.Addr, config.Port),
			ReadHeaderTimeout: config.ReadTimout,
			WriteTimeout:      config.WriteTimout,
			Handler:           router,
		},
		config:    config,
		clients:   make(map[string]*ClientSession),
		webrtcAPI: api,
		metrics:   &metrics{startTime: time.Now()},
		health:    &health{RecentErrors: make([]string, 0)},
	}

	// Setup routes
	router.HandleFunc("/api/webrtc-sink/request/{ID}", server.requestHandler)
	router.HandleFunc("/api/webrtc-sink/answer/{ID}", server.answerHandler)
	router.HandleFunc("/api/webrtc-sink/metrics", server.metricsHandler)
	router.HandleFunc("/api/webrtc-sink/health", server.healthHandler)

	return server
}

func (h *Host) setStatus(status ServerStatus) {
	h.health.mux.Lock()
	defer h.health.mux.Unlock()
	h.health.State = status
}

func (h *Host) setSession(ctx context.Context) {
	h.mux.Lock()
	defer h.mux.Unlock()

	if h.cancel != nil {
		h.cancel()
	}

	h.ctx, h.cancel = context.WithCancel(ctx)
}

func (h *Host) Start(ctx context.Context) {
	h.setSession(ctx)
	defer h.close()

	// Start cleanup goroutine
	h.wg.Add(1)
	go h.cleanupExpiredClients()

	fmt.Printf("Starting WebRTC server on %h\n", h.httpServer.Addr)

loop:
	for {
		select {
		case <-h.ctx.Done():
			return
		default:
			h.setStatus(ServerOnline)

			if err := h.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				h.setStatus(ServerDown)
				errMsg := fmt.Sprintf("error while serving webrtc js server: %v", err)
				fmt.Println(errMsg)
				h.health.addError(errMsg)
			}

			h.setStatus(ServerDown)

			if !h.config.KeepHosting {
				break loop
			}

			fmt.Println("failed to host server, retrying in 5 seconds...")
			time.Sleep(5 * time.Second)
		}
	}
}

func (h *Host) requestHandler(w http.ResponseWriter, req *http.Request) {
	// Extract ID from path
	id := req.PathValue("ID")
	if id == "" {
		http.Error(w, "Missing ID parameter", http.StatusBadRequest)
		return
	}

	// Check if local only and validate remote address
	if h.config.AllowLocalOnly && !isLoopBack(req.RemoteAddr) {
		http.Error(w, "Access denied: local connections only", http.StatusForbidden)
		return
	}

	// Check connection limit
	if h.metrics.active() >= h.config.TotalConnections {
		http.Error(w, "Maximum connections reached", http.StatusTooManyRequests)
		return
	}

	switch req.Method {
	case http.MethodPost:
		h.handleOfferRequest(w, req, id)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Host) handleOfferRequest(w http.ResponseWriter, req *http.Request, id string) {
	// Parse request body
	var request Request
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	if err := json.Unmarshal(body, &request); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Create or get existing client session
	session, err := h.getOrCreateSession(id, request.TTL)
	if err != nil {
		errMsg := fmt.Sprintf("Failed to create session: %v", err)
		h.health.addError(errMsg)
		http.Error(w, errMsg, http.StatusInternalServerError)
		return
	}

	// Create offer
	offer, err := session.peerConnection.CreateOffer(nil)
	if err != nil {
		errMsg := fmt.Sprintf("Failed to create offer: %v", err)
		h.health.addError(errMsg)
		http.Error(w, errMsg, http.StatusInternalServerError)
		return
	}

	// Set local description
	if err := session.peerConnection.SetLocalDescription(offer); err != nil {
		errMsg := fmt.Sprintf("Failed to set local description: %v", err)
		h.health.addError(errMsg)
		http.Error(w, errMsg, http.StatusInternalServerError)
		return
	}

	// Update session status
	session.mu.Lock()
	session.sdpStatus = SDPOfferSent
	session.mu.Unlock()

	// Send offer response
	offerResponse := Offer{
		ID:        id,
		Offer:     offer,
		Timestamp: time.Now(),
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(offerResponse); err != nil {
		errMsg := fmt.Sprintf("Failed to encode response: %v", err)
		h.health.addError(errMsg)
		http.Error(w, errMsg, http.StatusInternalServerError)
		return
	}

	fmt.Printf("Sent offer for session: %h\n", id)
}

func (h *Host) answerHandler(w http.ResponseWriter, req *http.Request) {
	// Extract ID from path
	id := req.PathValue("ID")
	if id == "" {
		http.Error(w, "Missing ID parameter", http.StatusBadRequest)
		return
	}

	switch req.Method {
	case http.MethodPost:
		h.handleAnswerResponse(w, req, id)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Host) handleAnswerResponse(w http.ResponseWriter, req *http.Request, id string) {
	// Parse answer from request body
	var answer Answer
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	if err := json.Unmarshal(body, &answer); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Get existing session
	h.mux.RLock()
	session, exists := h.clients[id]
	h.mux.RUnlock()

	if !exists {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}

	// Set remote description (answer)
	if err := session.peerConnection.SetRemoteDescription(answer.Answer); err != nil {
		errMsg := fmt.Sprintf("Failed to set remote description: %v", err)
		h.health.addError(errMsg)
		http.Error(w, errMsg, http.StatusInternalServerError)
		return
	}

	// Update session status
	session.mu.Lock()
	session.sdpStatus = SDPWaitingForAnswer
	session.mu.Unlock()
	session.updateActivity()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "success"})

	fmt.Printf("Received answer for session: %h\n", id)
}

func (h *Host) metricsHandler(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		data, err := h.metrics.Marshal()
		if err != nil {
			http.Error(w, "Failed to marshal metrics", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Host) healthHandler(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		data, err := h.health.Marshal()
		if err != nil {
			http.Error(w, "Failed to marshal health data", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Host) getOrCreateSession(id string, ttl time.Duration) (*ClientSession, error) {
	h.mux.Lock()
	defer h.mux.Unlock()

	// Check if session already exists
	if session, exists := h.clients[id]; exists {
		session.updateActivity()
		return session, nil
	}

	// Create new peer connection
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	}

	pc, err := h.webrtcAPI.NewPeerConnection(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create peer connection: %v", err)
	}

	// Create video track
	videoTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264},
		"video",
		fmt.Sprintf("%h-video", id),
	)
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("failed to create video track: %v", err)
	}

	// Add track to peer connection
	rtpSender, err := pc.AddTrack(videoTrack)
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("failed to add track: %v", err)
	}

	// Set TTL
	if ttl == 0 {
		ttl = h.config.ClientTTL
	}

	// Create session
	session := &ClientSession{
		peerConnection:  pc,
		videoTrack:      videoTrack,
		sdpStatus:       SDPRequested,
		ttl:             ttl,
		connectionState: webrtc.PeerConnectionStateNew,
		rtpChan:         make(chan *rtp.Packet, 1000),
		lastActivity:    time.Now(),
	}

	// Handle RTCP feedback
	go func() {
		rtcpBuf := make([]byte, 1500)
		for {
			if _, _, rtcpErr := rtpSender.Read(rtcpBuf); rtcpErr != nil {
				return
			}
		}
	}()

	// Handle connection state changes
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		session.mu.Lock()
		session.connectionState = state
		session.mu.Unlock()

		fmt.Printf("Session %h connection state: %h\n", id, state.String())

		switch state {
		case webrtc.PeerConnectionStateConnected:
			session.mu.Lock()
			session.sdpStatus = SDPConnected
			session.mu.Unlock()
			h.metrics.increaseActiveConnections()
		case webrtc.PeerConnectionStateDisconnected, webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			session.mu.Lock()
			session.sdpStatus = SDPFailed
			session.mu.Unlock()
			h.metrics.decreaseActiveConnections()
			h.metrics.increaseFailedConnections()
		}
	})

	// Start RTP forwarding goroutine
	go h.forwardRTPPackets(session)

	// Store session
	h.clients[id] = session

	fmt.Printf("Created new session: %h\n", id)
	return session, nil
}

func (h *Host) forwardRTPPackets(session *ClientSession) {
	for packet := range session.rtpChan {
		// Forward to WebRTC track
		if err := session.videoTrack.WriteRTP(packet); err != nil {
			continue
		}

		// Update metrics
		h.metrics.addDataSent(int64(len(packet.Payload)))
	}
}

// ProcessRTPPacket forwards RTP packet to specific session
func (h *Host) ProcessRTPPacket(sessionID string, rtpData *rtp.Packet) error {
	h.mux.RLock()
	session, exists := h.clients[sessionID]
	h.mux.RUnlock()

	if !exists {
		return fmt.Errorf("session %h not found", sessionID)
	}

	select {
	case session.rtpChan <- rtpData:
		session.updateActivity()
		return nil
	default:
		return fmt.Errorf("RTP channel full for session %h", sessionID)
	}
}

// WriteRTP broadcasts an RTP packet to all connected client sessions
func (h *Host) WriteRTP(packet *rtp.Packet) error {
	h.mux.RLock()
	defer h.mux.RUnlock()

	if len(h.clients) == 0 {
		return fmt.Errorf("no active sessions")
	}

	var errors []string
	successCount := 0

	// Send to all sessions
	for sessionID := range h.clients {
		if err := h.ProcessRTPPacket(sessionID, packet); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", sessionID, err))
		} else {
			successCount++
		}
	}

	if successCount == 0 {
		return fmt.Errorf("failed to send to all sessions: %v", errors)
	}

	return nil
}

func (h *Host) cleanupExpiredClients() {
	defer h.wg.Done()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-h.ctx.Done():
			return
		case <-ticker.C:
			h.mux.Lock()
			for id, session := range h.clients {
				if session.isExpired() {
					fmt.Printf("Cleaning up expired session: %h\n", id)
					session.peerConnection.Close()
					close(session.rtpChan)
					delete(h.clients, id)
					h.metrics.decreaseActiveConnections()
				}
			}
			h.mux.Unlock()
		}
	}
}

func (h *Host) close() {
	fmt.Println("Shutting down WebRTC server...")

	// Cancel context
	if h.cancel != nil {
		h.cancel()
	}

	// Close HTTP server
	if err := h.httpServer.Close(); err != nil {
		fmt.Printf("Error closing HTTP server: %v\n", err)
	}

	// Close all client sessions
	h.mux.Lock()
	for id, session := range h.clients {
		fmt.Printf("Closing session: %h\n", id)
		session.peerConnection.Close()
		close(session.rtpChan)
	}
	h.clients = make(map[string]*ClientSession)
	h.mux.Unlock()

	// Wait for goroutines to finish
	h.wg.Wait()

	fmt.Println("WebRTC server shutdown complete")
}

func (h *Host) Close() error {
	h.close()

	return nil
}
