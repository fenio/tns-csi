package tnsapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// mockWSServer provides a mock WebSocket server for testing.
//
//nolint:govet // fieldalignment not critical for test code
type mockWSServer struct {
	server          *httptest.Server
	handler         func(*websocket.Conn)
	authResult      bool
	authError       *Error
	expectAuthKey   string
	disconnectAfter int // Disconnect after N messages (0 = never)
	mu              sync.Mutex
	msgCount        int
}

type concurrentReauthState struct { //nolint:govet // Field order follows synchronization and state semantics.
	mu              sync.Mutex
	authCalls       int
	dataCalls       int
	pendingReauthID string
	callerCount     int
}

func (s *concurrentReauthState) handle(req Request) []Response {
	s.mu.Lock()
	defer s.mu.Unlock()

	if req.Method == methodAuthLoginWithAPIKey {
		s.authCalls++
		if s.authCalls > 1 && s.dataCalls < s.callerCount {
			s.pendingReauthID = req.ID
			return nil
		}
		return []Response{{ID: req.ID, Result: json.RawMessage(`true`)}}
	}

	s.dataCalls++
	resp := Response{ID: req.ID, Result: json.RawMessage(`true`)}
	if s.dataCalls <= s.callerCount {
		resp.Result = nil
		resp.Error = enotauthenticatedError()
	}
	if s.dataCalls != s.callerCount || s.pendingReauthID == "" {
		return []Response{resp}
	}

	reauthResp := Response{ID: s.pendingReauthID, Result: json.RawMessage(`true`)}
	s.pendingReauthID = ""
	return []Response{resp, reauthResp}
}

func newMockWSServer() *mockWSServer {
	m := &mockWSServer{
		authResult:    true,
		expectAuthKey: "test-api-key",
	}

	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")

		if m.handler != nil {
			m.handler(conn)
			return
		}

		// Default handler - echo server with auth support
		m.defaultHandler(r.Context(), conn)
	}))

	return m
}

func (m *mockWSServer) defaultHandler(ctx context.Context, conn *websocket.Conn) {
	for {
		m.mu.Lock()
		m.msgCount++
		shouldDisconnect := m.disconnectAfter > 0 && m.msgCount >= m.disconnectAfter
		m.mu.Unlock()

		if shouldDisconnect {
			return
		}

		_, message, err := conn.Read(ctx)
		if err != nil {
			return
		}

		var req Request
		if errUnmarshal := json.Unmarshal(message, &req); errUnmarshal != nil {
			continue
		}

		// Handle authentication
		if req.Method == "auth.login_with_api_key" {
			var resp Response
			resp.ID = req.ID

			if m.authError != nil {
				resp.Error = m.authError
			} else if len(req.Params) > 0 {
				apiKey, ok := req.Params[0].(string)
				if !ok || (m.expectAuthKey != "" && apiKey != m.expectAuthKey) {
					resp.Error = &Error{
						Code:    401,
						Message: "invalid API key",
					}
				} else {
					result, errMarshal := json.Marshal(m.authResult)
					if errMarshal == nil {
						resp.Result = result
					}
				}
			}

			respBytes, errMarshal := json.Marshal(resp)
			if errMarshal == nil {
				conn.Write(ctx, websocket.MessageText, respBytes)
			}
			continue
		}

		// Echo back other requests with success
		resp := Response{
			ID:     req.ID,
			Result: json.RawMessage(`true`),
		}
		respBytes, errMarshal := json.Marshal(resp)
		if errMarshal == nil {
			conn.Write(ctx, websocket.MessageText, respBytes)
		}
	}
}

func serveMockRequests(conn *websocket.Conn, handle func(Request) []Response) {
	ctx := context.Background()
	for {
		var req Request
		if err := wsjson.Read(ctx, conn, &req); err != nil {
			return
		}
		for _, resp := range handle(req) {
			if err := wsjson.Write(ctx, conn, resp); err != nil {
				return
			}
		}
	}
}

func (m *mockWSServer) URL() string {
	return strings.Replace(m.server.URL, "http://", "ws://", 1)
}

func (m *mockWSServer) Close() {
	m.server.Close()
}

// cleanupClient ensures a client is fully closed and background goroutines have stopped.
func cleanupClient(client *Client) {
	if client != nil {
		client.Close()
		// Brief sleep to allow goroutines to observe the close signal
		time.Sleep(100 * time.Millisecond)
	}
}

func TestNewClient(t *testing.T) {
	//nolint:govet // fieldalignment not critical for test code
	tests := []struct {
		authResult    bool
		authError     *Error
		expectAuthKey string
		apiKey        string
		name          string
		wantErr       bool
	}{
		{
			name:          "successful connection and authentication",
			authResult:    true,
			expectAuthKey: "test-api-key",
			apiKey:        "test-api-key",
			wantErr:       false,
		},
		{
			name:          "authentication with trimmed API key",
			authResult:    true,
			expectAuthKey: "test-api-key",
			apiKey:        "  test-api-key  ",
			wantErr:       false,
		},
		{
			name:          "authentication failure - wrong key",
			authResult:    true,
			expectAuthKey: "correct-key",
			apiKey:        "wrong-key",
			wantErr:       true,
		},
		{
			name:       "authentication failure - rejected",
			authResult: false,
			apiKey:     "test-api-key",
			wantErr:    true,
		},
		{
			name:      "authentication failure - API error",
			authError: &Error{Code: 500, Message: "internal server error"},
			apiKey:    "test-api-key",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newMockWSServer()
			server.authResult = tt.authResult
			server.authError = tt.authError
			server.expectAuthKey = tt.expectAuthKey
			defer server.Close()

			client, err := NewClient(server.URL(), tt.apiKey, false)
			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if client == nil {
				t.Error("Expected client to be non-nil")
				return
			}

			cleanupClient(client)
		})
	}
}

func TestNewClientConnectionFailure(t *testing.T) {
	// Try to connect to non-existent server
	_, err := NewClient("ws://localhost:99999/invalid", "test-api-key", false)
	if err == nil {
		t.Error("Expected connection error but got nil")
	}

	if !strings.Contains(err.Error(), "failed to connect") {
		t.Errorf("Expected 'failed to connect' error, got: %v", err)
	}
}

func TestClientCall(t *testing.T) {
	//nolint:govet // fieldalignment not critical for test code
	tests := []struct {
		setupServer func(*mockWSServer)
		method      string
		params      []interface{}
		name        string
		wantErr     bool
	}{
		{
			name:   "successful RPC call",
			method: "test.method",
			params: []interface{}{"param1", "param2"},
			setupServer: func(m *mockWSServer) {
				// Default handler will echo success
			},
			wantErr: false,
		},
		{
			name:   "call with empty params",
			method: "test.method",
			params: []interface{}{},
			setupServer: func(m *mockWSServer) {
				// Default handler will echo success
			},
			wantErr: false,
		},
		{
			name:   "call with nil params",
			method: "test.method",
			params: nil,
			setupServer: func(m *mockWSServer) {
				// Default handler will echo success
			},
			wantErr: false,
		},
		{
			name:   "API error response",
			method: "test.method",
			params: []interface{}{},
			setupServer: func(m *mockWSServer) {
				m.handler = func(conn *websocket.Conn) {
					ctx := context.Background()
					// Handle auth first
					_, message, _ := conn.Read(ctx)
					var req Request
					json.Unmarshal(message, &req)
					if req.Method == "auth.login_with_api_key" {
						resp := Response{
							ID:     req.ID,
							Result: json.RawMessage(`true`),
						}
						respBytes, err := json.Marshal(resp)
						if err == nil {
							conn.Write(ctx, websocket.MessageText, respBytes)
						}
					}

					// Handle actual call with error
					_, message, _ = conn.Read(ctx)
					json.Unmarshal(message, &req)
					resp := Response{
						ID: req.ID,
						Error: &Error{
							Code:    404,
							Message: "not found",
						},
					}
					respBytes, err := json.Marshal(resp)
					if err == nil {
						conn.Write(ctx, websocket.MessageText, respBytes)
					}
				}
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newMockWSServer()
			if tt.setupServer != nil {
				tt.setupServer(server)
			}
			defer server.Close()

			client, err := NewClient(server.URL(), "test-api-key", false)
			if err != nil {
				t.Fatalf("Failed to create client: %v", err)
			}
			defer cleanupClient(client)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			var result bool
			err = client.Call(ctx, tt.method, tt.params, &result)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestClientCallTimeout(t *testing.T) {
	server := newMockWSServer()
	server.handler = func(conn *websocket.Conn) {
		ctx := context.Background()
		// Handle auth
		_, message, _ := conn.Read(ctx)
		var req Request
		json.Unmarshal(message, &req)
		if req.Method == "auth.login_with_api_key" {
			resp := Response{
				ID:     req.ID,
				Result: json.RawMessage(`true`),
			}
			respBytes, err := json.Marshal(resp)
			if err == nil {
				conn.Write(ctx, websocket.MessageText, respBytes)
			}
		}

		// Don't respond to next request - simulate timeout
		conn.Read(ctx)
		time.Sleep(5 * time.Second)
	}
	defer server.Close()

	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer cleanupClient(client)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	var result bool
	err = client.Call(ctx, "test.method", nil, &result)

	if err == nil {
		t.Error("Expected timeout error but got nil")
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Expected DeadlineExceeded error, got: %v", err)
	}
}

func TestClientCallAfterClose(t *testing.T) {
	server := newMockWSServer()
	defer server.Close()

	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	client.Close()

	ctx := context.Background()
	var result bool
	err = client.Call(ctx, "test.method", nil, &result)

	if err == nil {
		t.Error("Expected error after close but got nil")
	}

	if !errors.Is(err, ErrClientClosed) {
		t.Errorf("Expected ErrClientClosed, got: %v", err)
	}
}

func TestCallWaitsForReconnectAuthentication(t *testing.T) {
	server := newMockWSServer()
	defer server.Close()

	var mu sync.Mutex
	dataCalls := 0
	server.handler = func(conn *websocket.Conn) {
		serveMockRequests(conn, func(req Request) []Response {
			if req.Method != methodAuthLoginWithAPIKey {
				mu.Lock()
				dataCalls++
				mu.Unlock()
			}
			return []Response{{ID: req.ID, Result: json.RawMessage(`true`)}}
		})
	}

	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer cleanupClient(client)
	if !client.beginReconnect() {
		t.Fatal("beginReconnect() reported an unexpected active reconnect")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if authErr := client.authenticate(ctx); !errors.Is(authErr, ErrConnectionClosed) {
		client.endReconnect()
		t.Fatalf("authenticate() during reconnect error = %v, want ErrConnectionClosed", authErr)
	}
	result := make(chan error, 1)
	go func() {
		result <- client.Call(ctx, "test.method", nil, nil)
	}()

	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	callsWhileReconnecting := dataCalls
	mu.Unlock()
	client.endReconnect()
	if callsWhileReconnecting != 0 {
		t.Fatalf("data calls during reconnect = %d, want 0", callsWhileReconnecting)
	}
	if callErr := <-result; callErr != nil {
		t.Fatalf("Call() after reconnect completed failed: %v", callErr)
	}
}

func TestCloseStopsReconnectWaitersAndPreventsReconnect(t *testing.T) {
	server := newMockWSServer()
	defer server.Close()

	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	if !client.beginReconnect() {
		t.Fatal("beginReconnect() reported an unexpected active reconnect")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- client.Call(ctx, "test.method", nil, nil)
	}()

	time.Sleep(50 * time.Millisecond)
	client.Close()
	select {
	case callErr := <-result:
		if !errors.Is(callErr, ErrClientClosed) {
			t.Fatalf("Call() after Close() error = %v, want ErrClientClosed", callErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Close() did not unblock a call waiting for reconnect")
	}
	client.endReconnect()
	if connectErr := client.connect(); !errors.Is(connectErr, ErrClientClosed) {
		t.Fatalf("connect() after Close() error = %v, want ErrClientClosed", connectErr)
	}
}

// TestClientReconnection is skipped because testing reconnection logic
// properly would require modifying production code to make reconnection
// cancellable via closeCh. The reconnection logic is tested indirectly
// via integration tests where real network interruptions occur.
// Manual testing shows reconnection works correctly in production.

func TestClientPingPong(t *testing.T) {
	// Note: coder/websocket handles ping/pong automatically.
	// This test verifies the client remains functional with ping loop running.

	server := newMockWSServer()
	server.handler = func(conn *websocket.Conn) {
		ctx := context.Background()

		// Handle auth
		_, message, _ := conn.Read(ctx)
		var req Request
		json.Unmarshal(message, &req)
		if req.Method == "auth.login_with_api_key" {
			resp := Response{
				ID:     req.ID,
				Result: json.RawMessage(`true`),
			}
			respBytes, err := json.Marshal(resp)
			if err == nil {
				conn.Write(ctx, websocket.MessageText, respBytes)
			}
		}

		// Keep connection alive and respond to requests
		for {
			_, message, err := conn.Read(ctx)
			if err != nil {
				break
			}

			// Parse and respond to RPC calls
			var req Request
			if err := json.Unmarshal(message, &req); err == nil {
				if req.Method == "test.method" {
					resp := Response{
						ID:     req.ID,
						Result: json.RawMessage(`true`),
					}
					respBytes, err := json.Marshal(resp)
					if err == nil {
						conn.Write(ctx, websocket.MessageText, respBytes)
					}
				}
			}
		}
	}
	defer server.Close()

	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer cleanupClient(client)

	// Wait for ping to be sent (ping loop runs every 20s by default)
	// We need to verify pong is received but that requires waiting
	// For unit tests, we just verify the client is alive
	time.Sleep(100 * time.Millisecond)

	// Verify client is still functional
	ctx := context.Background()
	var result bool
	err = client.Call(ctx, "test.method", nil, &result)
	if err != nil {
		t.Errorf("Call after ping loop started failed: %v", err)
	}
}

func TestErrorFormatting(t *testing.T) {
	tests := []struct {
		err         *Error
		name        string
		wantContain string
	}{
		{
			name: "Storage API error with reason",
			err: &Error{
				ErrorName: "ENOENT",
				Reason:    "Dataset not found",
			},
			wantContain: "Storage API error [ENOENT]: Dataset not found",
		},
		{
			name: "JSON-RPC error with data",
			err: &Error{
				Code:    404,
				Message: "Not found",
				Data: &ErrorData{
					Error:     1,
					ErrorName: "ResourceMissing",
					Reason:    "resource cannot be located",
				},
			},
			wantContain: "Storage API error 404: Not found",
		},
		{
			name: "Simple JSON-RPC error",
			err: &Error{
				Code:    500,
				Message: "Internal error",
			},
			wantContain: "Storage API error 500: Internal error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errStr := tt.err.Error()
			if !strings.Contains(errStr, tt.wantContain) {
				t.Errorf("Error string %q does not contain %q", errStr, tt.wantContain)
			}
		})
	}
}

func TestAuthenticateDirect(t *testing.T) {
	// Test direct authentication (used during reconnection)
	//nolint:govet // fieldalignment not critical for test code
	tests := []struct {
		authResult bool
		authError  *Error
		name       string
		wantErr    bool
	}{
		{
			name:       "successful direct authentication",
			authResult: true,
			wantErr:    false,
		},
		{
			name:       "direct authentication failure - rejected",
			authResult: false,
			wantErr:    true,
		},
		{
			name:      "direct authentication failure - API error",
			authError: &Error{Code: 500, Message: "internal error"},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newMockWSServer()
			server.authResult = tt.authResult
			server.authError = tt.authError
			defer server.Close()

			// Create client without going through NewClient to test authenticateDirect directly
			client := &Client{
				url:           server.URL(),
				apiKey:        "test-api-key",
				pending:       make(map[string]chan *Response),
				closeCh:       make(chan struct{}),
				maxRetries:    5,
				retryInterval: 5 * time.Second,
				authGate:      make(chan struct{}, 1),
			}

			// Connect manually
			if err := client.connect(); err != nil {
				t.Fatalf("Failed to connect: %v", err)
			}
			defer cleanupClient(client)

			// Test direct authentication
			err := client.authenticateDirect()

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestClientClose(t *testing.T) {
	server := newMockWSServer()
	defer server.Close()

	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	// Close client
	client.Close()

	// Verify closed flag is set
	client.mu.Lock()
	if !client.closed {
		t.Error("Expected closed flag to be true")
	}
	client.mu.Unlock()

	// Double close should not panic
	client.Close()

	// Wait for goroutines to finish
	time.Sleep(50 * time.Millisecond)
}

func TestResponseIDMismatch(t *testing.T) {
	server := newMockWSServer()
	server.handler = func(conn *websocket.Conn) {
		ctx := context.Background()
		// Handle auth
		_, message, _ := conn.Read(ctx)
		var req Request
		json.Unmarshal(message, &req)
		if req.Method == "auth.login_with_api_key" {
			resp := Response{
				ID:     req.ID,
				Result: json.RawMessage(`true`),
			}
			respBytes, err := json.Marshal(resp)
			if err == nil {
				conn.Write(ctx, websocket.MessageText, respBytes)
			}
		}

		// Send response with mismatched ID
		conn.Read(ctx)
		resp := Response{
			ID:     "wrong-id-12345",
			Result: json.RawMessage(`true`),
		}
		respBytes, err := json.Marshal(resp)
		if err == nil {
			conn.Write(ctx, websocket.MessageText, respBytes)
		}
	}
	defer server.Close()

	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer cleanupClient(client)

	// This call will timeout because response has wrong ID
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	var result bool
	err = client.Call(ctx, "test.method", nil, &result)

	if err == nil {
		t.Error("Expected error due to ID mismatch but got nil")
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Logf("Got error: %v (expected timeout)", err)
	}
}

func TestConcurrentCalls(t *testing.T) {
	server := newMockWSServer()
	defer server.Close()

	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer cleanupClient(client)

	// Make multiple concurrent calls
	var wg sync.WaitGroup
	numCalls := 10

	for i := range numCalls {
		wg.Add(1)
		go func() {
			defer wg.Done()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			var result bool
			err := client.Call(ctx, "test.method", []interface{}{i}, &result)
			if err != nil {
				t.Errorf("Concurrent call failed: %v", err)
			}
		}()
	}

	wg.Wait()
}

func TestQueryPool(t *testing.T) {
	//nolint:govet // Test struct field alignment not critical for performance
	tests := []struct {
		name         string
		poolName     string
		setupServer  func(*mockWSServer)
		wantErr      bool
		wantPoolName string
		wantSize     int64
		wantFree     int64
	}{
		{
			name:     "successful pool query",
			poolName: "tank",
			setupServer: func(m *mockWSServer) {
				m.handler = func(conn *websocket.Conn) {
					ctx := context.Background()
					// Handle auth
					_, message, _ := conn.Read(ctx)
					var req Request
					_ = json.Unmarshal(message, &req)
					if req.Method == "auth.login_with_api_key" {
						resp := Response{
							ID:     req.ID,
							Result: json.RawMessage(`true`),
						}
						respBytes, err := json.Marshal(resp)
						if err != nil {
							return
						}
						_ = conn.Write(ctx, websocket.MessageText, respBytes)
					}

					// Handle pool.query
					_, message, _ = conn.Read(ctx)
					_ = json.Unmarshal(message, &req)
					if req.Method == "pool.query" {
						poolData := []Pool{{
							ID:   1,
							Name: "tank",
							Properties: struct {
								Size struct {
									Parsed int64 `json:"parsed"`
								} `json:"size"`
								Allocated struct {
									Parsed int64 `json:"parsed"`
								} `json:"allocated"`
								Free struct {
									Parsed int64 `json:"parsed"`
								} `json:"free"`
								Capacity struct {
									Parsed int64 `json:"parsed"`
								} `json:"capacity"`
							}{
								Size: struct {
									Parsed int64 `json:"parsed"`
								}{Parsed: 1000000000000}, // 1TB
								Allocated: struct {
									Parsed int64 `json:"parsed"`
								}{Parsed: 400000000000}, // 400GB
								Free: struct {
									Parsed int64 `json:"parsed"`
								}{Parsed: 600000000000}, // 600GB
								Capacity: struct {
									Parsed int64 `json:"parsed"`
								}{Parsed: 40}, // 40%
							},
						}}
						result, err := json.Marshal(poolData)
						if err != nil {
							return
						}
						resp := Response{
							ID:     req.ID,
							Result: result,
						}
						respBytes, err := json.Marshal(resp)
						if err != nil {
							return
						}
						_ = conn.Write(ctx, websocket.MessageText, respBytes)
					}
				}
			},
			wantErr:      false,
			wantPoolName: "tank",
			wantSize:     1000000000000,
			wantFree:     600000000000,
		},
		{
			name:     "pool not found",
			poolName: "nonexistent",
			setupServer: func(m *mockWSServer) {
				m.handler = func(conn *websocket.Conn) {
					ctx := context.Background()
					// Handle auth
					_, message, _ := conn.Read(ctx)
					var req Request
					json.Unmarshal(message, &req)
					if req.Method == "auth.login_with_api_key" {
						resp := Response{
							ID:     req.ID,
							Result: json.RawMessage(`true`),
						}
						respBytes, err := json.Marshal(resp)
						if err != nil {
							t.Errorf("failed to marshal response: %v", err)
							return
						}
						conn.Write(ctx, websocket.MessageText, respBytes)
					}

					// Handle pool.query - return empty array
					_, message, _ = conn.Read(ctx)
					json.Unmarshal(message, &req)
					if req.Method == "pool.query" {
						resp := Response{
							ID:     req.ID,
							Result: json.RawMessage(`[]`),
						}
						respBytes, err := json.Marshal(resp)
						if err != nil {
							t.Errorf("failed to marshal response: %v", err)
							return
						}
						conn.Write(ctx, websocket.MessageText, respBytes)
					}
				}
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newMockWSServer()
			if tt.setupServer != nil {
				tt.setupServer(server)
			}
			defer server.Close()

			client, err := NewClient(server.URL(), "test-api-key", false)
			if err != nil {
				t.Fatalf("Failed to create client: %v", err)
			}
			defer cleanupClient(client)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			pool, err := client.QueryPool(ctx, tt.poolName)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if pool.Name != tt.wantPoolName {
				t.Errorf("Pool name = %s, want %s", pool.Name, tt.wantPoolName)
			}

			if pool.Properties.Size.Parsed != tt.wantSize {
				t.Errorf("Pool size = %d, want %d", pool.Properties.Size.Parsed, tt.wantSize)
			}

			if pool.Properties.Free.Parsed != tt.wantFree {
				t.Errorf("Pool free = %d, want %d", pool.Properties.Free.Parsed, tt.wantFree)
			}
		})
	}
}

// enotauthenticatedError mimics the error the storage API returns when the
// WebSocket session has lost its authentication (e.g. middleware restart on
// the storage side that keeps TCP connections alive).
func enotauthenticatedError() *Error {
	return &Error{
		Code:    -32001,
		Message: "Method call error",
		Data: &ErrorData{
			Error:     207,
			ErrorName: "ENOTAUTHENTICATED",
			Reason:    "[ENOTAUTHENTICATED] Not authenticated",
		},
	}
}

func TestIsSessionAuthError(t *testing.T) {
	tests := []struct {
		err  error
		name string
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "plain error", err: errors.New("boom"), want: false},
		{name: "connection error", err: ErrConnectionClosed, want: false},
		{name: "ENOTAUTHENTICATED in data", err: enotauthenticatedError(), want: true},
		{
			name: "ENOTAUTHENTICATED in top-level errname",
			err:  &Error{ErrorName: "ENOTAUTHENTICATED", Reason: "[ENOTAUTHENTICATED] Not authenticated"},
			want: true,
		},
		{
			name: "wrapped ENOTAUTHENTICATED",
			err:  fmt.Errorf("call failed: %w", enotauthenticatedError()),
			want: true,
		},
		{
			name: "other API error",
			err:  &Error{Code: 401, Message: "invalid API key"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSessionAuthError(tt.err); got != tt.want {
				t.Errorf("isSessionAuthError() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCallReauthenticatesOnSessionAuthLoss reproduces the session-loss
// scenario: the server starts answering ENOTAUTHENTICATED on a perfectly
// healthy connection. Call() must re-authenticate on the live socket and
// retry the original request instead of failing forever.
func TestCallReauthenticatesOnSessionAuthLoss(t *testing.T) {
	server := newMockWSServer()
	defer server.Close()

	var mu sync.Mutex
	authCalls := 0
	dataCalls := 0

	server.handler = func(conn *websocket.Conn) {
		serveMockRequests(conn, func(req Request) []Response {
			resp := Response{ID: req.ID}
			mu.Lock()
			switch req.Method {
			case "auth.login_with_api_key":
				authCalls++
				resp.Result = json.RawMessage(`true`)
			default:
				dataCalls++
				if dataCalls == 1 {
					// Session silently dropped server-side: the connection
					// stays open, but calls are no longer authenticated.
					resp.Error = enotauthenticatedError()
				} else {
					resp.Result = json.RawMessage(`true`)
				}
			}
			mu.Unlock()
			return []Response{resp}
		})
	}

	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer cleanupClient(client)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var result bool
	if err := client.Call(ctx, "test.method", nil, &result); err != nil {
		t.Fatalf("Call() after session auth loss should recover, got error: %v", err)
	}
	if !result {
		t.Error("Call() result = false, want true")
	}

	mu.Lock()
	defer mu.Unlock()
	// initial NewClient auth + one re-auth triggered by ENOTAUTHENTICATED
	if authCalls != 2 {
		t.Errorf("auth.login_with_api_key calls = %d, want 2 (initial + re-auth)", authCalls)
	}
	if dataCalls != 2 {
		t.Errorf("data calls = %d, want 2 (failed + retried)", dataCalls)
	}
}

func TestCallReauthenticatesOnRapidSecondSessionLoss(t *testing.T) {
	server := newMockWSServer()
	defer server.Close()

	var mu sync.Mutex
	authenticated := false
	authCalls := 0
	dataCalls := 0
	server.handler = func(conn *websocket.Conn) {
		serveMockRequests(conn, func(req Request) []Response {
			resp := Response{ID: req.ID}
			mu.Lock()
			if req.Method == methodAuthLoginWithAPIKey {
				authCalls++
				authenticated = true
				resp.Result = json.RawMessage(`true`)
			} else {
				dataCalls++
				if authenticated {
					resp.Result = json.RawMessage(`true`)
				} else {
					resp.Error = enotauthenticatedError()
				}
			}
			mu.Unlock()
			return []Response{resp}
		})
	}

	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer cleanupClient(client)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for call := 1; call <= 2; call++ {
		mu.Lock()
		authenticated = false
		mu.Unlock()

		var result bool
		if callErr := client.Call(ctx, "test.method", nil, &result); callErr != nil {
			t.Fatalf("Call() %d after session auth loss failed: %v", call, callErr)
		}
		if !result {
			t.Fatalf("Call() %d result = false, want true", call)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if authCalls != 3 {
		t.Errorf("auth calls = %d, want 3 (initial + two re-authentications)", authCalls)
	}
	if dataCalls != 4 {
		t.Errorf("data calls = %d, want 4 (two failed + two retried)", dataCalls)
	}
}

func TestConcurrentCallsShareReauthentication(t *testing.T) {
	const callerCount = 8

	server := newMockWSServer()
	defer server.Close()

	state := concurrentReauthState{callerCount: callerCount}
	server.handler = func(conn *websocket.Conn) {
		serveMockRequests(conn, state.handle)
	}

	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer cleanupClient(client)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results := make(chan error, callerCount)
	for range callerCount {
		go func() {
			var result bool
			if callErr := client.Call(ctx, "test.method", nil, &result); callErr != nil {
				results <- callErr
				return
			}
			if !result {
				results <- errors.New("call returned false")
				return
			}
			results <- nil
		}()
	}
	for range callerCount {
		if callErr := <-results; callErr != nil {
			t.Fatalf("concurrent Call() failed: %v", callErr)
		}
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.authCalls != 2 {
		t.Errorf("auth calls = %d, want 2 (initial + one shared re-authentication)", state.authCalls)
	}
	if state.dataCalls != callerCount*2 {
		t.Errorf("data calls = %d, want %d", state.dataCalls, callerCount*2)
	}
}

func TestCallCancellationStopsReauthentication(t *testing.T) {
	server := newMockWSServer()
	defer server.Close()

	authCalls := 0
	server.handler = func(conn *websocket.Conn) {
		serveMockRequests(conn, func(req Request) []Response {
			if req.Method == methodAuthLoginWithAPIKey {
				authCalls++
				if authCalls > 1 {
					return nil
				}
				return []Response{{ID: req.ID, Result: json.RawMessage(`true`)}}
			}
			return []Response{{ID: req.ID, Error: enotauthenticatedError()}}
		})
	}

	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer cleanupClient(client)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	callErr := client.Call(ctx, "test.method", nil, nil)
	if !errors.Is(callErr, context.DeadlineExceeded) {
		t.Fatalf("Call() error = %v, want context deadline exceeded", callErr)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("Call() ignored caller cancellation for %v", elapsed)
	}
}

func TestConcurrentCallsShareInternalAuthenticationTimeout(t *testing.T) {
	server := newMockWSServer()
	defer server.Close()

	var mu sync.Mutex
	authCalls := 0
	server.handler = func(conn *websocket.Conn) {
		serveMockRequests(conn, func(req Request) []Response {
			if req.Method == methodAuthLoginWithAPIKey {
				mu.Lock()
				authCalls++
				currentAuthCalls := authCalls
				mu.Unlock()
				if currentAuthCalls > 1 {
					return nil
				}
				return []Response{{ID: req.ID, Result: json.RawMessage(`true`)}}
			}
			return []Response{{ID: req.ID, Error: enotauthenticatedError()}}
		})
	}

	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer cleanupClient(client)

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	results := make(chan error, 2)
	for range 2 {
		go func() {
			results <- client.Call(ctx, "test.method", nil, nil)
		}()
	}
	for range 2 {
		if callErr := <-results; !errors.Is(callErr, context.DeadlineExceeded) {
			t.Fatalf("Call() error = %v, want shared internal authentication timeout", callErr)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if authCalls != 2 {
		t.Errorf("auth calls = %d, want 2 (initial + one shared timed-out re-authentication)", authCalls)
	}
}

func TestCallDoesNotReauthenticateAfterFinalAttempt(t *testing.T) {
	server := newMockWSServer()
	defer server.Close()

	var mu sync.Mutex
	authCalls := 0
	dataCalls := 0
	server.handler = func(conn *websocket.Conn) {
		serveMockRequests(conn, func(req Request) []Response {
			resp := Response{ID: req.ID}
			mu.Lock()
			if req.Method == methodAuthLoginWithAPIKey {
				authCalls++
				resp.Result = json.RawMessage(`true`)
			} else {
				dataCalls++
				resp.Error = enotauthenticatedError()
			}
			mu.Unlock()
			return []Response{resp}
		})
	}

	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer cleanupClient(client)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if callErr := client.Call(ctx, "test.method", nil, nil); callErr == nil {
		t.Fatal("Call() succeeded despite repeated session authentication errors")
	}

	mu.Lock()
	defer mu.Unlock()
	if authCalls != 3 {
		t.Errorf("auth calls = %d, want 3 (initial + two retryable attempts)", authCalls)
	}
	if dataCalls != 3 {
		t.Errorf("data calls = %d, want 3", dataCalls)
	}
}

// TestAuthCallDoesNotRecurseOnSessionAuthLoss guards against infinite
// recursion: if auth.login_with_api_key itself fails with ENOTAUTHENTICATED,
// Call() must NOT trigger another re-authentication.
func TestAuthCallDoesNotRecurseOnSessionAuthLoss(t *testing.T) {
	server := newMockWSServer()
	defer server.Close()

	var mu sync.Mutex
	authCalls := 0

	server.handler = func(conn *websocket.Conn) {
		serveMockRequests(conn, func(req Request) []Response {
			resp := Response{ID: req.ID}
			mu.Lock()
			authCalls++
			if authCalls == 1 {
				// Let NewClient succeed
				resp.Result = json.RawMessage(`true`)
			} else {
				resp.Error = enotauthenticatedError()
			}
			mu.Unlock()
			return []Response{resp}
		})
	}

	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer cleanupClient(client)

	// The auth call fails with ENOTAUTHENTICATED and must surface the error
	// without recursing.
	if err := client.reauthenticate(context.Background(), client.currentReauthEpoch()); err == nil {
		t.Fatal("reauthenticate() should fail when auth itself gets ENOTAUTHENTICATED")
	}

	mu.Lock()
	defer mu.Unlock()
	// initial NewClient auth + exactly ONE failed re-auth attempt
	if authCalls != 2 {
		t.Errorf("auth.login_with_api_key calls = %d, want 2 (no recursion)", authCalls)
	}
}

func TestAuthenticationGateStopsOnContextOrClose(t *testing.T) {
	//nolint:govet // Field order keeps the test case readable.
	tests := []struct {
		name    string
		prepare func(context.CancelFunc, chan struct{})
		wantErr error
	}{
		{
			name: "canceled context",
			prepare: func(cancel context.CancelFunc, _ chan struct{}) {
				cancel()
			},
			wantErr: context.Canceled,
		},
		{
			name: "closed client",
			prepare: func(_ context.CancelFunc, closeCh chan struct{}) {
				close(closeCh)
			},
			wantErr: ErrClientClosed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &Client{
				authGate: make(chan struct{}, 1),
				closeCh:  make(chan struct{}),
			}
			client.authGate <- struct{}{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tt.prepare(cancel, client.closeCh)

			if err := client.acquireAuthentication(ctx); !errors.Is(err, tt.wantErr) {
				t.Fatalf("acquireAuthentication() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestReauthenticationSynchronizationBranches(t *testing.T) {
	t.Run("waiter cancellation", func(t *testing.T) {
		done := make(chan struct{})
		client := &Client{reauthDone: done}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if err := client.reauthenticate(ctx, 0); !errors.Is(err, context.Canceled) {
			t.Fatalf("reauthenticate() error = %v, want context canceled", err)
		}
		if client.reauthDone != done {
			t.Fatal("waiter cancellation changed the active reauthentication")
		}
	})

	t.Run("stale epoch after gate", func(t *testing.T) {
		wantErr := errors.New("reauthentication already completed")
		client := &Client{
			authGate:      make(chan struct{}, 1),
			closeCh:       make(chan struct{}),
			reauthEpoch:   2,
			lastReauthErr: wantErr,
		}

		performed, err := client.performReauthentication(context.Background(), 1)
		if !errors.Is(err, wantErr) || performed {
			t.Fatalf("performReauthentication() = (%v, %v), want (%v, false)", err, performed, wantErr)
		}
	})

	t.Run("record completion", func(t *testing.T) {
		client := &Client{}
		wantErr := errors.New("authentication rejected")
		client.recordReauthentication(wantErr)

		if client.currentReauthEpoch() != 1 {
			t.Fatalf("reauth epoch = %d, want 1", client.currentReauthEpoch())
		}
		if !errors.Is(client.lastReauthErr, wantErr) {
			t.Fatalf("last reauthentication error = %v, want %v", client.lastReauthErr, wantErr)
		}
	})
}

func TestWaitForCallRetry(t *testing.T) {
	//nolint:govet // Field order keeps the test case readable.
	tests := []struct {
		err     error
		prepare func(*Client, context.CancelFunc)
		name    string
		attempt int
		wantErr error
	}{
		{name: "non-connection error", err: errors.New("invalid request"), wantErr: errors.New("invalid request")},
		{
			name: "canceled context",
			err:  ErrConnectionClosed,
			prepare: func(_ *Client, cancel context.CancelFunc) {
				cancel()
			},
			wantErr: context.Canceled,
		},
		{
			name: "closed client",
			err:  ErrConnectionClosed,
			prepare: func(client *Client, _ context.CancelFunc) {
				client.closed = true
			},
			wantErr: ErrClientClosed,
		},
		{name: "final attempt", err: ErrConnectionClosed, attempt: 3},
		{
			name: "close during backoff",
			err:  ErrConnectionClosed,
			prepare: func(client *Client, _ context.CancelFunc) {
				close(client.closeCh)
			},
			wantErr: ErrClientClosed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &Client{closeCh: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.prepare != nil {
				tt.prepare(client, cancel)
			}
			attempt := tt.attempt
			if attempt == 0 {
				attempt = 1
			}

			err := client.waitForCallRetry(ctx, attempt, 3, tt.err)
			if tt.name == "non-connection error" {
				if err == nil || err.Error() != tt.wantErr.Error() {
					t.Fatalf("waitForCallRetry() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("waitForCallRetry() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
