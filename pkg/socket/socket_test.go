package socket

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test helper functions
func createTestServer(port uint16) *Server {
	config := config{
		addr:             "localhost",
		port:             port, // Let OS choose port
		readTimout:       5 * time.Second,
		writeTimout:      5 * time.Second,
		totalConnections: 5,
		keepHosting:      false,
	}
	return NewServer(config)
}

func createTestHost(addr string, port uint16, onMessage OnServerMessage) *Host {
	config := HostConfig{
		writeTimeout: 5 * time.Second,
		readTimout:   5 * time.Second,
		connectRetry: false,
	}
	return &Host{
		addr:   addr,
		port:   port,
		config: config,
		f:      onMessage,
	}
}

// Test port allocation - each test gets a dedicated port
const (
	testPortBase        = 9000
	testPortStatus      = testPortBase + 1 // 9001
	testPortMetrics     = testPortBase + 2 // 9002
	testPortIntegration = testPortBase + 3 // 9003
	testPortMultiClient = testPortBase + 4 // 9004
	testPortConnLimit   = testPortBase + 5 // 9005
	testPortBenchmark   = testPortBase + 6 // 9006
)

func TestServer_NewServer(t *testing.T) {
	server := createTestServer(testPortBase)

	assert.NotNil(t, server)
	assert.NotNil(t, server.clients)
	assert.NotNil(t, server.metrics)
	assert.NotNil(t, server.health)
	assert.Nil(t, server.host)
}

func TestServer_Close(t *testing.T) {
	server := createTestServer(testPortBase)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.Start(ctx)

	time.Sleep(500 * time.Millisecond)
	err := server.Close()

	assert.Nil(t, err)
}

func TestServer_StatusHandler(t *testing.T) {
	server := createTestServer(testPortStatus)

	// Start server in background
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.Start(ctx)
	time.Sleep(100 * time.Millisecond) // Wait for server to start

	// Test status endpoint
	resp, err := http.Get(fmt.Sprintf("http://localhost:%d/status", testPortStatus))
	require.NoError(t, err)
	defer resp.Body.Close() // Important: always close the response body

	// Read the response body
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	fmt.Println("status response:", string(body))

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
}

func TestServer_MetricsHandler(t *testing.T) {
	server := createTestServer(testPortMetrics)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	// Test metrics endpoint
	resp, err := http.Get(fmt.Sprintf("http://localhost:%d/metrics", testPortMetrics))
	require.NoError(t, err)
	defer resp.Body.Close() // Important: always close the response body

	// Read the response body
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	fmt.Println("metrics response:", string(body))

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
}

func TestMetrics_ThreadSafety(t *testing.T) {
	metrics := &metrics{}

	// Test concurrent access
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			metrics.increaseActiveConnections()
			metrics.addDataSent(100)
			metrics.addDataRecvd(50)
			active := metrics.active()
			assert.GreaterOrEqual(t, active, uint8(0))
		}()
	}

	wg.Wait()
	assert.Equal(t, uint8(10), metrics.active())
}

func TestHost_Connect_NoServer(t *testing.T) {
	receivedMessages := make([][]byte, 0)
	var mu sync.Mutex

	onMessage := func(msg []byte) error {
		mu.Lock()
		defer mu.Unlock()
		receivedMessages = append(receivedMessages, msg)
		return nil
	}

	host := createTestHost("localhost", 9999, onMessage) // Non-existent server

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Should fail to connect and return quickly since connectRetry is false
	host.Connect(ctx)

	// Verify no messages received
	mu.Lock()
	defer mu.Unlock()
	assert.Empty(t, receivedMessages)
}

func TestHost_WriteWithoutConnection(t *testing.T) {
	host := createTestHost("localhost", 8080, nil)

	// Create a simple RTP packet
	packet := &rtp.Packet{
		Header: rtp.Header{
			Version:     2,
			PayloadType: 96,
		},
		Payload: []byte("test payload"),
	}

	err := host.Write(packet)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "yet to connect to server")
}

func TestHost_CloseConnection(t *testing.T) {
	host := createTestHost("localhost", 8080, nil)

	// Test closing without connection
	err := host.Close()
	assert.NoError(t, err)

	// Test setting session and closing
	ctx := context.Background()
	host.setSession(ctx)

	err = host.Close()
	assert.NoError(t, err)
}

func TestServerHost_Integration(t *testing.T) {
	// Skip if integration tests are not enabled
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create and start server
	server := createTestServer(testPortIntegration)

	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

	go server.Start(serverCtx)
	time.Sleep(200 * time.Millisecond) // Wait for server to start

	// Create host client
	receivedMessages := make([][]byte, 0)
	var mu sync.Mutex

	onMessage := func(msg []byte) error {
		mu.Lock()
		defer mu.Unlock()
		receivedMessages = append(receivedMessages, msg)
		return nil
	}

	host := createTestHost("localhost", testPortIntegration, onMessage)
	host.config.connectRetry = false

	// Connect host
	hostCtx, hostCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer hostCancel()

	go host.Connect(hostCtx)
	time.Sleep(500 * time.Millisecond) // Wait for connection

	// Verify host is connected (by checking server has a host)
	server.mux.RLock()
	hasHost := server.host != nil
	server.mux.RUnlock()

	assert.True(t, hasHost, "Server should have a host connection")

	// Clean up
	host.Close()
	time.Sleep(100 * time.Millisecond)
}

func TestServerHost_MultipleClients(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create and start server
	server := createTestServer(testPortMultiClient)
	server.config.port = testPortMultiClient
	server.config.totalConnections = 3
	server.httpServer.Addr = fmt.Sprintf("localhost:%d", testPortMultiClient)

	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

	go server.Start(serverCtx)
	time.Sleep(200 * time.Millisecond)

	// Connect host first
	host := createTestHost("localhost", testPortMultiClient, func(msg []byte) error { return nil })
	host.config.connectRetry = false

	hostCtx, hostCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer hostCancel()

	go host.Connect(hostCtx)
	time.Sleep(300 * time.Millisecond)

	// Try to connect multiple clients using raw websocket connections
	clients := make([]*websocket.Conn, 0)

	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		conn, _, err := websocket.Dial(ctx, fmt.Sprintf("ws://localhost:%d/ws", testPortMultiClient), nil)
		cancel()

		if err == nil {
			clients = append(clients, conn)
		}
	}

	// Should have successfully connected clients
	assert.Greater(t, len(clients), 0, "Should have connected at least one client")

	// Clean up clients
	for _, client := range clients {
		client.Close(websocket.StatusNormalClosure, "test cleanup")
	}

	host.Close()
}

func TestServer_ConnectionLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	server := createTestServer(testPortConnLimit)

	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

	go server.Start(serverCtx)
	time.Sleep(200 * time.Millisecond)

	// Connect host first
	host := createTestHost("localhost", testPortConnLimit, func(msg []byte) error { return nil })
	host.config.connectRetry = false

	go host.Connect(context.Background())
	time.Sleep(300 * time.Millisecond)

	// Try to connect more clients than the limit
	successfulConnections := 0
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		conn, _, err := websocket.Dial(ctx, fmt.Sprintf("ws://localhost:%d/ws", testPortConnLimit), nil)
		cancel()

		if err == nil {
			successfulConnections++
			go func(c *websocket.Conn) {
				defer c.Close(websocket.StatusNormalClosure, "test")
				// Keep connection alive briefly
				time.Sleep(100 * time.Millisecond)
			}(conn)
		}
	}

	// Should respect connection limit
	assert.LessOrEqual(t, successfulConnections, int(server.config.totalConnections))

	// Check that failed connections were recorded
	failed := server.metrics.failed()
	assert.Greater(t, failed, uint8(0), "Should have recorded failed connections")

	host.Close()
}

func BenchmarkServer_WebSocketUpgrade(b *testing.B) {
	config := config{
		addr:             "localhost",
		port:             testPortBenchmark, // Let OS choose port
		readTimout:       100 * time.Millisecond,
		writeTimout:      100 * time.Millisecond,
		totalConnections: 255,
		keepHosting:      false,
	}
	server := NewServer(config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.Start(ctx)
	time.Sleep(200 * time.Millisecond)

	b.ResetTimer()

	// Limit to 200 concurrent connections
	oldMaxProcs := runtime.GOMAXPROCS(200)
	defer runtime.GOMAXPROCS(oldMaxProcs)

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			conn, _, err := websocket.Dial(ctx, fmt.Sprintf("ws://localhost:%d/ws", testPortBenchmark), nil)
			cancel()

			if err == nil {
				conn.Close(websocket.StatusNormalClosure, "benchmark")
			}
		}
	})
}
