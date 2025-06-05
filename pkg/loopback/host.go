package loopback

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"sync"
	"time"

	"github.com/pion/rtp"
)

type metrics struct {
	DataSent      int64
	DataRecvd     int64
	DataSentRate  float32
	DataRecvdRate float32
	mu            sync.RWMutex
	lastUpdate    time.Time
	lastSent      int64
	lastRecvd     int64
}

func (m *metrics) updateRates() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	if !m.lastUpdate.IsZero() {
		duration := now.Sub(m.lastUpdate).Seconds()
		if duration > 0 {
			m.DataSentRate = float32(m.DataSent-m.lastSent) / float32(duration)
			m.DataRecvdRate = float32(m.DataRecvd-m.lastRecvd) / float32(duration)
		}
	}

	m.lastUpdate = now
	m.lastSent = m.DataSent
	m.lastRecvd = m.DataRecvd
}

func (m *metrics) addSent(bytes int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.DataSent += bytes
}

func (m *metrics) addRecvd(bytes int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.DataRecvd += bytes
}

func (m *metrics) GetStats() (sent, recvd int64, sentRate, recvdRate float32) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.DataSent, m.DataRecvd, m.DataSentRate, m.DataRecvdRate
}

type OnMessage = func([]byte) error

type LoopBack struct {
	bindPort *net.UDPConn
	remote   *net.UDPAddr
	f        OnMessage

	metrics metrics

	cmd *exec.Cmd

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.RWMutex
}

// NewLoopBack creates a new LoopBack instance
func NewLoopBack(options ...Option) (*LoopBack, error) {
	l := &LoopBack{
		metrics: metrics{lastUpdate: time.Now()},
	}

	for _, option := range options {
		if err := option(l); err != nil {
			return nil, err
		}
	}

	return l, nil
}

func (l *LoopBack) Connect(ctx context.Context) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.cancel != nil {
		l.cancel()
	}
	l.ctx, l.cancel = context.WithCancel(ctx)

	// Start reading goroutine
	l.wg.Add(1)
	go l.readLoop()

	// Start metrics update goroutine
	l.wg.Add(1)
	go l.metricsLoop()

	fmt.Printf("LoopBack connected on %s\n", l.bindPort.LocalAddr().String())
}

func (l *LoopBack) WriteRTP(packet *rtp.Packet) error {
	if packet == nil {
		return fmt.Errorf("packet cannot be nil")
	}

	payload, err := packet.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal RTP packet: %v", err)
	}

	return l.Write(payload)
}

func (l *LoopBack) Write(payload []byte) error {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.bindPort == nil {
		return fmt.Errorf("bind port not yet set. Skipping message")
	}
	if l.remote == nil {
		return fmt.Errorf("remote port not yet discovered. Skipping message")
	}

	bytesWritten, err := l.bindPort.WriteToUDP(payload, l.remote)
	if err != nil {
		return fmt.Errorf("failed to write UDP message: %v", err)
	}

	if bytesWritten != len(payload) {
		return fmt.Errorf("written bytes (%d) != message length (%d)", bytesWritten, len(payload))
	}

	// Update metrics
	l.metrics.addSent(int64(bytesWritten))

	return nil
}

func (l *LoopBack) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	fmt.Println("Closing LoopBack...")

	// Cancel context to stop goroutines
	if l.cancel != nil {
		l.cancel()
	}

	// Close UDP connection
	var err error
	if l.bindPort != nil {
		err = l.bindPort.Close()
		l.bindPort = nil
	}

	// Wait for goroutines to finish
	l.wg.Wait()

	fmt.Println("LoopBack closed")
	return err
}

func (l *LoopBack) readLoop() {
	defer l.wg.Done()

	fmt.Println("Starting LoopBack read loop")

	for {
		select {
		case <-l.ctx.Done():
			fmt.Println("LoopBack read loop stopped")
			return
		default:
			// Set read timeout to allow periodic context checking
			l.bindPort.SetReadDeadline(time.Now().Add(1 * time.Second))

			buffer, nRead := l.readMessageFromUDPPort()
			if nRead > 0 && buffer != nil {
				// Update metrics
				l.metrics.addRecvd(int64(nRead))

				// Process message
				if l.f != nil {
					if err := l.f(buffer[:nRead]); err != nil {
						fmt.Printf("Error processing message: %v\n", err)
					}
				}
			}
		}
	}
}

func (l *LoopBack) metricsLoop() {
	defer l.wg.Done()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-l.ctx.Done():
			return
		case <-ticker.C:
			l.metrics.updateRates()
		}
	}
}

func (l *LoopBack) readMessageFromUDPPort() ([]byte, int) {
	buffer := make([]byte, 1500) // Standard MTU size

	nRead, senderAddr, err := l.bindPort.ReadFromUDP(buffer)
	if err != nil {
		// Check if it's a timeout (which is expected)
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			return nil, 0
		}
		fmt.Printf("Error while reading message from bind port: %v\n", err)
		return nil, 0
	}

	// Auto-discover remote address from first received packet
	l.mu.Lock()
	if l.remote == nil {
		l.remote = &net.UDPAddr{IP: senderAddr.IP, Port: senderAddr.Port}
		fmt.Printf("Auto-discovered remote address: %s\n", l.remote.String())
	}
	l.mu.Unlock()

	// Validate sender (optional security check)
	if senderAddr != nil && l.remote != nil && senderAddr.Port != l.remote.Port {
		fmt.Printf("Warning: expected port %d but got %d\n", l.remote.Port, senderAddr.Port)
	}

	return buffer, nRead
}

// SetRemoteAddress manually sets the remote address (alternative to auto-discovery)
func (l *LoopBack) SetRemoteAddress(address string) error {
	addr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return fmt.Errorf("failed to resolve remote address: %v", err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.remote = addr

	fmt.Printf("Remote address set to: %s\n", addr.String())
	return nil
}

// GetMetrics returns current metrics
func (l *LoopBack) GetMetrics() (sent, recvd int64, sentRate, recvdRate float32) {
	return l.metrics.GetStats()
}

// GetLocalAddress returns the local binding address
func (l *LoopBack) GetLocalAddress() string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.bindPort == nil {
		return ""
	}
	return l.bindPort.LocalAddr().String()
}

// GetRemoteAddress returns the current remote address
func (l *LoopBack) GetRemoteAddress() string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.remote == nil {
		return ""
	}
	return l.remote.String()
}

// IsConnected returns true if the LoopBack is actively running
func (l *LoopBack) IsConnected() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.ctx != nil && l.ctx.Err() == nil
}

func (l *LoopBack) SetOnPayloadCallback(f OnMessage) {
	l.f = f
}
