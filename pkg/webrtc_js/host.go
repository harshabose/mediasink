package webrtc_js

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
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

var (
	// Remove the start codes (0 0 0 1) for RTP - only keep the NAL unit
	spsData = []byte{103, 66, 192, 31, 166, 128, 216, 61, 230, 225, 0, 0, 3, 0, 1, 0, 0, 3, 0, 50, 224, 32, 0, 19, 18, 208, 0, 9, 137, 110, 41, 32, 7, 140, 25, 80}
	ppsData = []byte{104, 206, 62, 128}
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
	primarySSRC     webrtc.SSRC
	rtxSSRC         webrtc.SSRC
	RTPPayloadType  webrtc.PayloadType
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

	server.httpServer.Handler = server.enableCORS(router)

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

func (h *Host) Connect(ctx context.Context) {
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

func (h *Host) enableCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set CORS headers
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Accept, Authorization, Content-Type, X-CSRF-Token")

		// Handle preflight requests
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (h *Host) requestHandler(w http.ResponseWriter, req *http.Request) {
	fmt.Printf("got request")
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

	// Set local description FIRST
	if err := session.peerConnection.SetLocalDescription(offer); err != nil {
		errMsg := fmt.Sprintf("Failed to set local description: %v", err)
		h.health.addError(errMsg)
		http.Error(w, errMsg, http.StatusInternalServerError)
		return
	}

	// THEN wait for ICE gathering
	fmt.Printf("Waiting for ICE gathering to complete for session: %s\n", id)
	<-webrtc.GatheringCompletePromise(session.peerConnection)
	fmt.Printf("ICE gathering complete for session: %s\n", id)

	// Get the complete local description with ICE candidates
	finalOffer := session.peerConnection.LocalDescription()

	localSDP := session.peerConnection.LocalDescription()
	primarySSRC, rtxSSRC := extractSSRCFromSDP(localSDP.SDP)

	session.primarySSRC = webrtc.SSRC(primarySSRC)
	session.rtxSSRC = webrtc.SSRC(rtxSSRC)

	// Update session status
	session.mu.Lock()
	session.sdpStatus = SDPOfferSent
	session.mu.Unlock()

	// Send offer response with complete ICE candidates
	offerResponse := Offer{
		ID:        id,
		Offer:     *finalOffer, // Use the final description with ICE candidates
		Timestamp: time.Now(),
	}

	fmt.Printf("offer: %s\n", finalOffer.SDP)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(offerResponse); err != nil {
		errMsg := fmt.Sprintf("Failed to encode response: %v", err)
		h.health.addError(errMsg)
		http.Error(w, errMsg, http.StatusInternalServerError)
		return
	}

	fmt.Printf("Sent offer for session: %s\n", id)
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

	fmt.Printf("answer: %s\n", answer.Answer.SDP)

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
		webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeH264,
			ClockRate:   90000,
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f", // Match PT 102
		},
		"video",
		fmt.Sprintf("%s-video", id),
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

		fmt.Printf("Session %s connection state: %s\n", id, state.String())

		switch state {
		case webrtc.PeerConnectionStateConnected:
			session.mu.Lock()
			session.sdpStatus = SDPConnected
			session.mu.Unlock()
			h.metrics.increaseActiveConnections()
			fmt.Printf("🚀 Connection established! Sending SPS/PPS for session %s\n", id)
			if err := h.sendParameterSets(id); err != nil {
				fmt.Printf("❌ Failed to send parameter sets: %v\n", err)
			}

			// Start periodic SPS/PPS sending
			h.startParameterSetTimer(id)
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
		fmt.Printf("Forwarding packet - PT: %d, Seq: %d, TS: %d, SSRC: %d, Size: %d, Marker: %t\n",
			packet.Header.PayloadType,
			packet.Header.SequenceNumber,
			packet.Header.Timestamp,
			packet.Header.SSRC,
			len(packet.Payload),
			packet.Header.Marker)

		// Validate H.264 payload
		if len(packet.Payload) > 0 {
			nalType := packet.Payload[0] & 0x1F
			nalHeader := packet.Payload[0]
			fmt.Printf("H.264 NAL - Type: %d, Header: 0x%02x, First 8 bytes: %x\n",
				nalType, nalHeader, packet.Payload[:min(8, len(packet.Payload))])

			// Check for valid NAL unit types
			if nalType == 0 || nalType > 23 {
				fmt.Printf("WARNING: Invalid NAL unit type %d\n", nalType)
			}
		} else {
			fmt.Printf("WARNING: Empty payload\n")
		}
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
		fmt.Printf("Session %s not found\n", sessionID)
		return fmt.Errorf("session %s not found", sessionID)
	}

	rtpData.Header.SSRC = uint32(session.primarySSRC)

	select {
	case session.rtpChan <- rtpData:
		session.updateActivity()
		return nil
	default:
		fmt.Printf("RTP channel full for session %s\n", sessionID)
		return fmt.Errorf("RTP channel full for session %s", sessionID)
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

func extractSSRCFromSDP(sdp string) (primarySSRC uint32, rtxSSRC uint32) {
	lines := strings.Split(sdp, "\n")
	ssrcs := make([]uint32, 0)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "a=ssrc:") && !strings.Contains(line, "rtx") {
			// Extract SSRC number
			parts := strings.Split(line, " ")
			if len(parts) >= 1 {
				ssrcStr := strings.Split(parts[0], ":")[1]
				if ssrc, err := strconv.ParseUint(ssrcStr, 10, 32); err == nil {
					ssrcs = append(ssrcs, uint32(ssrc))
				}
			}
		}
	}

	// Remove duplicates and sort
	uniqueSSRCs := make(map[uint32]bool)
	for _, ssrc := range ssrcs {
		uniqueSSRCs[ssrc] = true
	}

	ssrcList := make([]uint32, 0, len(uniqueSSRCs))
	for ssrc := range uniqueSSRCs {
		ssrcList = append(ssrcList, ssrc)
	}

	if len(ssrcList) >= 1 {
		primarySSRC = ssrcList[0] // 3420211287
	}
	if len(ssrcList) >= 2 {
		rtxSSRC = ssrcList[1] // 230256171
	}

	return primarySSRC, rtxSSRC
}

func (h *Host) sendParameterSets(sessionID string) error {
	h.mux.RLock()
	session, exists := h.clients[sessionID]
	h.mux.RUnlock()

	if !exists {
		return fmt.Errorf("session %s not found", sessionID)
	}

	// Create SPS packet
	spsPacket := &rtp.Packet{
		Header: rtp.Header{
			Version:     2,
			PayloadType: 102,
			Timestamp:   uint32(time.Now().Unix() * 90000), // Use current timestamp
			SSRC:        uint32(session.primarySSRC),       // Use the extracted SSRC
			Marker:      false,                             // SPS should not have marker bit
		},
		Payload: spsData,
	}

	// Create PPS packet
	ppsPacket := &rtp.Packet{
		Header: rtp.Header{
			Version:     2,
			PayloadType: 102,
			Timestamp:   uint32(time.Now().Unix() * 90000), // Same timestamp as SPS
			SSRC:        uint32(session.primarySSRC),
			Marker:      false, // PPS should not have marker bit
		},
		Payload: ppsData,
	}

	// Send SPS first
	select {
	case session.rtpChan <- spsPacket:
		session.updateActivity()
		session.mu.Lock()
		session.mu.Unlock()
	default:
		return fmt.Errorf("failed to send SPS: channel full")
	}

	// Send PPS second
	select {
	case session.rtpChan <- ppsPacket:
		session.updateActivity()
		session.mu.Lock()
		session.mu.Unlock()
	default:
		return fmt.Errorf("failed to send PPS: channel full")
	}

	return nil
}

func (h *Host) startParameterSetTimer(sessionID string) {
	go func() {
		ticker := time.NewTicker(3 * time.Second) // Send every 3 seconds
		defer ticker.Stop()

		fmt.Printf("⏰ Started periodic SPS/PPS timer for session %s\n", sessionID)

		for {
			select {
			case <-h.ctx.Done():
				fmt.Printf("⏰ Stopping SPS/PPS timer for session %s (context done)\n", sessionID)
				return
			case <-ticker.C:
				h.mux.RLock()
				session, exists := h.clients[sessionID]
				h.mux.RUnlock()

				if !exists {
					fmt.Printf("⏰ Stopping SPS/PPS timer for session %s (session not found)\n", sessionID)
					return
				}

				session.mu.RLock()
				connected := session.connectionState == webrtc.PeerConnectionStateConnected
				session.mu.RUnlock()

				if connected {
					fmt.Printf("⏰ Periodic SPS/PPS send for session %s\n", sessionID)
					if err := h.sendParameterSets(sessionID); err != nil {
						fmt.Printf("❌ Failed periodic SPS/PPS send: %v\n", err)
					}
				} else {
					fmt.Printf("⏰ Stopping SPS/PPS timer for session %s (not connected)\n", sessionID)
					return
				}
			}
		}
	}()
}

func (h *Host) Write(_ []byte) error {
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
