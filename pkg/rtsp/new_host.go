package rtsp

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/pion/rtp"
)

type HostState int

const (
	HostStateDisconnected HostState = iota
	HostStateConnecting
	HostStateConnected
	HostStateRecording
	HostStateError
)

type NewHost struct {
	// Connection details
	serverAddr string
	streamPath string
	rtspURL    string

	// RTSP client
	client      *gortsplib.Client
	description *description.Session

	// State management
	state    HostState
	stateMux sync.RWMutex
	ctx      context.Context
	cancel   context.CancelFunc

	// Configuration
	config *HostConfig

	// Error handling
	lastError error
	errorMux  sync.RWMutex

	// Reconnection
	reconnectChan chan struct{}

	// Metrics
	packetsWritten uint64
	bytesWritten   uint64
	lastWriteTime  time.Time
}

type HostConfig struct {
	ServerAddr        string
	ServerPort        int
	StreamPath        string
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	DialTimeout       time.Duration
	ReconnectAttempts int
	ReconnectDelay    time.Duration
	UserAgent         string
}

func LocalHostConfig() *HostConfig {
	return &HostConfig{
		ServerAddr:        "localhost",
		ServerPort:        8554,
		StreamPath:        "stream",
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		DialTimeout:       10 * time.Second,
		ReconnectAttempts: 3,
		ReconnectDelay:    2 * time.Second,
		UserAgent:         "GoRTSP-Host/1.0",
	}
}

func SimpleSkylineSonataHostConfig() *HostConfig {
	return nil
}

func NewNewHost(config *HostConfig, des *description.Session, options ...HostOption) (*NewHost, error) {
	if config == nil {
		config = LocalHostConfig()
	}

	if des == nil {
		des = &description.Session{}
	}

	host := &NewHost{
		serverAddr:    config.ServerAddr,
		streamPath:    config.StreamPath,
		rtspURL:       fmt.Sprintf("rtsp://%s:%d/%s", config.ServerAddr, config.ServerPort, config.StreamPath),
		description:   des,
		config:        config,
		state:         HostStateDisconnected,
		reconnectChan: make(chan struct{}, 1),
	}

	// Apply options
	for _, option := range options {
		if err := option(host); err != nil {
			return nil, fmt.Errorf("failed to apply option: %w", err)
		}
	}

	// Validate des after options
	if len(host.description.Medias) == 0 {
		return nil, errors.New("options do not set media options")
	}

	fmt.Printf("Host configured for RTSP URL: %s\n", host.rtspURL)
	return host, nil
}

func (h *NewHost) Connect(ctx context.Context) {
	h.ctx, h.cancel = context.WithCancel(ctx)
	h.setState(HostStateConnecting)

	fmt.Printf("Connecting to RTSP server: %s with persistent connection management\n", h.rtspURL)

	// This blocks and manages connection for the lifetime of the context
	h.connectionManager()
}

func (h *NewHost) connectionManager() {
	currentDelay := h.config.ReconnectDelay

	for {
		// Check if we should stop
		select {
		case <-h.ctx.Done():
			fmt.Printf("Connection manager stopping due to context cancellation\n")
			h.setState(HostStateDisconnected)
			return
		default:
		}

		// Attempt connection
		fmt.Printf("Attempting to connect to RTSP server...\n")
		if err := h.attemptConnection(); err != nil {
			h.setState(HostStateError)
			h.setError(err)
			fmt.Printf("Connection failed: %v\n", err)

			// Check if we should retry
			if h.config.ReconnectAttempts == 0 {
				// No retries configured, exit
				fmt.Printf("No retries configured, stopping connection attempts\n")
				return
			}
			// For ReconnectAttempts == -1 (infinite) or > 0 (limited), continue retrying
			// Note: We don't track attempt count for infinite retries
			// TODO: ADD COUNTER

			fmt.Printf("Retrying connection in %v...\n", currentDelay)
			select {
			case <-h.ctx.Done():
				fmt.Printf("Connection manager stopping during retry delay\n")
				h.setState(HostStateDisconnected)
				return
			case <-time.After(currentDelay):
				// Continue to next attempt
			}

			// Exponential backoff (cap at 30 seconds)
			currentDelay = time.Duration(float64(currentDelay) * 1.5)
			if currentDelay > 30*time.Second {
				currentDelay = 30 * time.Second
			}

			continue
		}

		// Connection successful
		h.setState(HostStateRecording)
		fmt.Printf("Successfully connected and recording to RTSP server\n")
		currentDelay = h.config.ReconnectDelay // Reset delay

		h.monitorConnection()

		select {
		case <-h.ctx.Done():
			fmt.Printf("Connection manager stopping - context cancelled\n")
			h.setState(HostStateDisconnected)
			return
		default:
			// Connection was lost, loop back to retry
			fmt.Printf("Connection lost, attempting to reconnect...\n")
			h.setState(HostStateConnecting)
		}
	}
}

func (h *NewHost) monitorConnection() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-h.ctx.Done():
			return
		case <-ticker.C:
			// Check connection health
			if h.client == nil {
				fmt.Printf("Client is nil, connection lost\n")
				return
			}

			if time.Since(h.lastWriteTime) > 60*time.Second {
				fmt.Printf("No write activity for 60s, connection might be stale\n")
				// Don't return here, just log - let write errors trigger reconnection
			}

			// Additional health checks could go here
			// For now, write errors are to detect connection issues
		}
	}
}

func (h *NewHost) attemptConnection() error {
	// Create new client for each attempt
	h.client = &gortsplib.Client{
		ReadTimeout:  h.config.ReadTimeout,
		WriteTimeout: h.config.WriteTimeout,
		UserAgent:    h.config.UserAgent,
	}

	// Parse RTSP URL
	parsedURL, err := url.Parse(h.rtspURL)
	if err != nil {
		return fmt.Errorf("invalid RTSP URL: %w", err)
	}

	// Start recording (ANNOUNCE, SETUP, and RECORD)
	if err := h.client.StartRecording(parsedURL.String(), h.description); err != nil {
		return fmt.Errorf("failed to start recording: %w", err)
	}

	h.setState(HostStateRecording)
	h.lastWriteTime = time.Now()
	return nil
}

func (h *NewHost) connectionMonitor() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-h.ctx.Done():
			return
		case <-ticker.C:
			// Check connection health
			if h.getState() == HostStateRecording {
				if time.Since(h.lastWriteTime) > 30*time.Second {
					fmt.Printf("No write activity for 30s, connection might be stale\n")
				}
			}
		case <-h.reconnectChan:
			// Reconnection requested
			if h.getState() == HostStateError {
				fmt.Printf("Attempting reconnection...\n")
				if err := h.attemptConnection(); err != nil {
					fmt.Printf("Reconnection failed: %v\n", err)
				} else {
					fmt.Printf("Reconnection successful\n")
				}
			}
		}
	}
}

func (h *NewHost) WriteRTP(packet *rtp.Packet) error {
	if packet == nil {
		fmt.Println("Nil packet, not sending to server")
		return nil
	}

	state := h.getState()
	if state != HostStateRecording {
		return fmt.Errorf("cannot write: host state is %v, expected %v", state, HostStateRecording)
	}

	if h.client == nil {
		return errors.New("client not initialized")
	}

	// Validate and fix payload type if needed
	if len(h.description.Medias) == 0 || len(h.description.Medias[0].Formats) == 0 {
		return errors.New("no media formats available")
	}

	expectedPayloadType := h.description.Medias[0].Formats[0].PayloadType()
	if packet.PayloadType != expectedPayloadType {
		fmt.Printf("Fixing payload type: expected %d, got %d\n", expectedPayloadType, packet.PayloadType)
		packet.PayloadType = expectedPayloadType
	}

	// Write packet to server
	if err := h.client.WritePacketRTP(h.description.Medias[0], packet); err != nil {
		h.setState(HostStateError)
		h.setError(err)

		// Trigger reconnection attempt
		select {
		case h.reconnectChan <- struct{}{}:
		default:
			// Channel full, skip
		}

		return fmt.Errorf("failed to write RTP packet: %w", err)
	}

	// Update metrics
	h.packetsWritten++
	h.bytesWritten += uint64(len(packet.Payload))
	h.lastWriteTime = time.Now()

	return nil
}

func (h *NewHost) Write(_ []byte) error {
	return nil
}

func (h *NewHost) Close() error {
	fmt.Printf("Closing RTSP host connection\n")

	// Cancel context
	if h.cancel != nil {
		h.cancel()
	}

	// Close client
	if h.client != nil {
		h.client.Close()
	}

	h.setState(HostStateDisconnected)
	fmt.Printf("RTSP host connection closed\n")
	return nil
}

// State management methods
func (h *NewHost) getState() HostState {
	h.stateMux.RLock()
	defer h.stateMux.RUnlock()
	return h.state
}

func (h *NewHost) setState(state HostState) {
	h.stateMux.Lock()
	defer h.stateMux.Unlock()
	h.state = state
}

func (h *NewHost) setError(err error) {
	h.errorMux.Lock()
	defer h.errorMux.Unlock()
	h.lastError = err
}

func (h *NewHost) GetLastError() error {
	h.errorMux.RLock()
	defer h.errorMux.RUnlock()
	return h.lastError
}

// Status methods

func (h *NewHost) IsConnected() bool {
	state := h.getState()
	return state == HostStateConnected || state == HostStateRecording
}

func (h *NewHost) IsRecording() bool {
	return h.getState() == HostStateRecording
}

func (h *NewHost) GetStats() (uint64, uint64, time.Time) {
	return h.packetsWritten, h.bytesWritten, h.lastWriteTime
}

func (h *NewHost) GetRTSPURL() string {
	return h.rtspURL
}

func (h *NewHost) AppendRTSPMediaDescription(media *description.Media) {
	h.description.Medias = append(h.description.Medias, media)
}
