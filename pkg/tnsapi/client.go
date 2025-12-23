// Package tnsapi provides a WebSocket client for TrueNAS Scale API.
package tnsapi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fenio/tns-csi/pkg/metrics"
	"github.com/gorilla/websocket"
	"k8s.io/klog/v2"
)

// Static errors for client operations.
var (
	ErrAuthenticationRejected = errors.New("authentication failed: Storage system rejected API key - verify key is correct and not revoked in System Settings -> API Keys")
	ErrResponseIDMismatch     = errors.New("authentication response ID mismatch")
	ErrClientClosed           = errors.New("client is closed")
	ErrConnectionClosed       = errors.New("connection closed while waiting for response")
	ErrCloneFailed            = errors.New("clone operation returned false (unsuccessful)")
	ErrClonedDatasetNotFound  = errors.New("cloned dataset not found after successful clone")
	ErrSubsystemNotFound      = errors.New("subsystem not found - ensure subsystem is pre-configured in TrueNAS")
	ErrMultipleSubsystems     = errors.New("multiple subsystems found with same NQN")
	ErrListSubsystemsFailed   = errors.New("failed to list NVMe-oF subsystems with all methods")
	ErrDatasetNotFound        = errors.New("dataset not found")
	ErrJobNotFound            = errors.New("job not found")
	ErrJobFailed              = errors.New("job failed")
	ErrJobAborted             = errors.New("job was aborted")

	// Deletion operation errors - TrueNAS API returned false (unsuccessful).
	ErrDatasetDeletionFailed   = errors.New("dataset deletion returned false (unsuccessful)")
	ErrNFSShareDeletionFailed  = errors.New("NFS share deletion returned false (unsuccessful)")
	ErrSubsystemDeletionFailed = errors.New("NVMe-oF subsystem deletion returned false (unsuccessful)")
	ErrNamespaceDeletionFailed = errors.New("NVMe-oF namespace deletion returned false (unsuccessful)")
	ErrSnapshotDeletionFailed  = errors.New("snapshot deletion returned false (unsuccessful)")
)

// Client is a storage API client using JSON-RPC 2.0 over WebSocket.
//
//nolint:govet // fieldalignment: struct field order optimized for readability over memory layout
type Client struct {
	mu            sync.Mutex
	conn          *websocket.Conn
	pending       map[string]chan *Response
	closeCh       chan struct{}
	url           string
	apiKey        string
	connectedAt   time.Time // Track connection start time for metrics
	retryInterval time.Duration
	reqID         uint64
	maxRetries    int
	closed        bool
	reconnecting  bool
	skipTLSVerify bool // Skip TLS certificate verification
}

// Request represents a storage API WebSocket request (JSON-RPC 2.0 format).
type Request struct {
	ID      string        `json:"id"`
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params,omitempty"`
}

// Response represents a storage API WebSocket response.
type Response struct {
	Error  *Error          `json:"error,omitempty"`
	ID     string          `json:"id"`
	Msg    string          `json:"msg,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

// Error represents a storage API error.
type Error struct {
	Data      interface{} `json:"data,omitempty"`
	ErrorName string      `json:"errname"`
	Reason    string      `json:"reason"`
	Type      string      `json:"type"`
	Message   string      `json:"message"`
	ErrorCode int         `json:"error"`
	Code      int         `json:"code"`
}

func (e *Error) Error() string {
	// Try storage API error format first
	if e.Reason != "" {
		return fmt.Sprintf("Storage API error [%s]: %s", e.ErrorName, e.Reason)
	}
	// Fallback to JSON-RPC 2.0 format
	if e.Data != nil {
		// Try to format Data as JSON for better error messages
		if dataBytes, err := json.Marshal(e.Data); err == nil {
			return fmt.Sprintf("Storage API error %d: %s (data: %s)", e.Code, e.Message, string(dataBytes))
		}
		return fmt.Sprintf("Storage API error %d: %s (data: %v)", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("Storage API error %d: %s", e.Code, e.Message)
}

// isAuthenticationError checks if an error is a permanent authentication failure.
// These include:
// - 401 (unauthorized/invalid API key)
// - Rejected API key errors
// - 500 errors during authentication (likely indicates broken auth endpoint, not transient)
// These should not be retried as they indicate configuration/server issues.
func isAuthenticationError(err error) bool {
	if err == nil {
		return false
	}

	// Check for explicit authentication errors
	if errors.Is(err, ErrAuthenticationRejected) {
		return true
	}

	// Check error message for authentication-related failures
	errMsg := err.Error()
	return strings.Contains(errMsg, "401") ||
		strings.Contains(errMsg, "invalid API key") ||
		strings.Contains(errMsg, "rejected API key") ||
		(strings.Contains(errMsg, "authentication failed") && strings.Contains(errMsg, "500"))
}

// NewClient creates a new storage API client.
// skipTLSVerify should be set to true only for self-signed certificates (common in TrueNAS deployments).
func NewClient(url, apiKey string, skipTLSVerify bool) (*Client, error) {
	klog.V(4).Infof("Creating new storage API client for %s (skipTLSVerify=%v)", url, skipTLSVerify)

	// Trim whitespace from API key (common issue with secrets)
	apiKey = strings.TrimSpace(apiKey)
	klog.V(5).Infof("API key length after trim: %d characters", len(apiKey))

	c := &Client{
		url:           url,
		apiKey:        apiKey,
		pending:       make(map[string]chan *Response),
		closeCh:       make(chan struct{}),
		maxRetries:    5,
		retryInterval: 5 * time.Second,
		skipTLSVerify: skipTLSVerify,
	}

	// Connect to WebSocket with retry logic
	// This is critical for driver initialization in environments with intermittent network connectivity
	maxAttempts := 5
	retryDelays := []time.Duration{0, 5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second}

	var lastConnErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			klog.Warningf("Connection attempt %d/%d to TrueNAS failed: %v", attempt-1, maxAttempts, lastConnErr)
			delay := retryDelays[attempt-1]
			klog.Infof("Retrying connection in %v...", delay)
			time.Sleep(delay)

			// Create a fresh client instance for retry to avoid goroutine conflicts
			c = &Client{
				url:           url,
				apiKey:        apiKey,
				pending:       make(map[string]chan *Response),
				closeCh:       make(chan struct{}),
				maxRetries:    5,
				retryInterval: 5 * time.Second,
				skipTLSVerify: skipTLSVerify,
			}
		}

		klog.V(4).Infof("Attempting to connect to TrueNAS (attempt %d/%d)", attempt, maxAttempts)

		// Connect to WebSocket
		if err := c.connect(); err != nil {
			lastConnErr = err
			if attempt == maxAttempts {
				return nil, fmt.Errorf("failed to connect after %d attempts: %w", maxAttempts, err)
			}
			continue
		}

		// Start response handler
		go c.readLoop()

		// Start ping handler for connection health monitoring
		go c.pingLoop()

		// Authenticate
		if err := c.authenticate(); err != nil {
			c.Close()
			lastConnErr = err

			// Don't retry on authentication errors (401, rejected API key) - these are permanent failures
			// Only retry on network/connection errors
			if errors.Is(err, ErrAuthenticationRejected) || isAuthenticationError(err) {
				klog.Errorf("Authentication failed permanently: %v", err)
				return nil, fmt.Errorf("authentication failed: %w", err)
			}

			if attempt == maxAttempts {
				return nil, fmt.Errorf("failed to authenticate after %d attempts: %w", maxAttempts, err)
			}
			continue
		}

		// Success!
		klog.Infof("Successfully connected to TrueNAS on attempt %d/%d", attempt, maxAttempts)
		return c, nil
	}

	// This should never be reached due to the return in the loop, but keep for safety
	return nil, fmt.Errorf("failed to initialize client after %d attempts: %w", maxAttempts, lastConnErr)
}

// connect establishes WebSocket connection.
func (c *Client) connect() error {
	klog.V(4).Infof("Connecting to storage WebSocket at %s", c.url)

	dialer := &websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	// For wss:// connections, configure TLS based on skipTLSVerify setting
	if strings.HasPrefix(c.url, "wss://") {
		if c.skipTLSVerify {
			klog.V(4).Info("TLS certificate verification disabled (skipTLSVerify=true)")
			//nolint:gosec // G402: TLS InsecureSkipVerify set true - intentional when user explicitly enables skipTLSVerify for self-signed certs
			dialer.TLSClientConfig = &tls.Config{
				InsecureSkipVerify: true,
				MinVersion:         tls.VersionTLS12,
			}
		} else {
			// Use secure TLS config with system CA pool
			dialer.TLSClientConfig = &tls.Config{
				MinVersion: tls.VersionTLS12,
			}
		}
	}

	//nolint:bodyclose // WebSocket connections don't return response bodies to close
	conn, _, err := dialer.Dial(c.url, nil)
	if err != nil {
		return fmt.Errorf("failed to dial: %w", err)
	}

	// Set up pong handler to respond to server pings
	// Note: TrueNAS does not send pings, but it responds to ours with pongs
	// Reset read deadline when we receive pongs to keep connection alive through firewalls
	conn.SetPongHandler(func(_ string) error {
		klog.V(6).Info("Received WebSocket pong")
		// Reset read deadline since we got activity
		if err := conn.SetReadDeadline(time.Now().Add(40 * time.Second)); err != nil {
			klog.Warningf("Failed to reset read deadline in pong handler: %v", err)
		}
		return nil
	})

	// Set up ping handler in case server sends pings (respond automatically)
	conn.SetPingHandler(func(appData string) error {
		klog.V(6).Info("Received WebSocket ping, sending pong")
		// gorilla/websocket automatically sends pongs, just reset deadline
		if err := conn.SetReadDeadline(time.Now().Add(40 * time.Second)); err != nil {
			klog.Warningf("Failed to reset read deadline in ping handler: %v", err)
		}
		return nil
	})

	c.conn = conn
	c.connectedAt = time.Now()

	// Update connection metrics
	metrics.SetWSConnectionStatus(true)

	return nil
}

// authenticate performs API key authentication using JSON-RPC 2.0.
func (c *Client) authenticate() error {
	klog.V(4).Info("Authenticating with storage system using auth.login_with_api_key")

	// Storage system uses JSON-RPC 2.0 for authentication
	// Call auth.login_with_api_key with the API key
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var authResult bool
	if err := c.Call(ctx, "auth.login_with_api_key", []interface{}{c.apiKey}, &authResult); err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	if !authResult {
		klog.Errorf("Storage system rejected API key (length: %d)", len(c.apiKey))
		return ErrAuthenticationRejected
	}

	klog.V(4).Info("Successfully authenticated with storage system")
	return nil
}

// authenticateDirect performs API key authentication by directly reading from WebSocket
// This is used during reconnection when readLoop is blocked and can't handle responses.
func (c *Client) authenticateDirect() error {
	klog.V(4).Info("Authenticating with storage system using auth.login_with_api_key (direct mode)")

	c.mu.Lock()

	// Generate request ID
	id := strconv.FormatUint(atomic.AddUint64(&c.reqID, 1), 10)

	// Create authentication request
	req := &Request{
		ID:      id,
		JSONRPC: "2.0",
		Method:  "auth.login_with_api_key",
		Params:  []interface{}{c.apiKey},
	}

	// Send request (log method only, not params which contain sensitive data)
	klog.V(5).Infof("Sending authentication request: method=%s, id=%s", req.Method, req.ID)
	if err := c.conn.WriteJSON(req); err != nil {
		c.mu.Unlock()
		return fmt.Errorf("failed to send authentication request: %w", err)
	}

	// Set read deadline for authentication response
	if err := c.conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		c.mu.Unlock()
		return fmt.Errorf("failed to set read deadline: %w", err)
	}
	c.mu.Unlock()

	// Read response directly (don't use readLoop)
	_, rawMsg, err := c.conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("failed to read authentication response: %w", err)
	}

	klog.V(5).Infof("Received raw response: %s", string(rawMsg))

	// Parse response
	var resp Response
	if err := json.Unmarshal(rawMsg, &resp); err != nil {
		return fmt.Errorf("failed to unmarshal authentication response: %w", err)
	}

	klog.V(5).Infof("Parsed response: %+v", resp)

	// Check for errors
	if resp.Error != nil {
		return fmt.Errorf("authentication error: %w", resp.Error)
	}

	// Verify response ID matches
	if resp.ID != id {
		return fmt.Errorf("%w: expected %s, got %s", ErrResponseIDMismatch, id, resp.ID)
	}

	// Parse auth result
	var authResult bool
	if resp.Result != nil {
		if err := json.Unmarshal(resp.Result, &authResult); err != nil {
			return fmt.Errorf("failed to unmarshal authentication result: %w", err)
		}
	}

	if !authResult {
		klog.Errorf("Storage system rejected API key (length: %d)", len(c.apiKey))
		return ErrAuthenticationRejected
	}

	klog.V(4).Info("Successfully authenticated with storage system (direct mode)")
	return nil
}

// isConnectionError checks if the error is a connection-related error that should trigger a retry.
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrConnectionClosed) {
		return true
	}
	errStr := err.Error()
	return strings.Contains(errStr, "use of closed network connection") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "i/o timeout")
}

// Call makes a JSON-RPC 2.0 call with automatic retry on connection failures.
func (c *Client) Call(ctx context.Context, method string, params []interface{}, result interface{}) error {
	// Start timing for metrics
	timer := metrics.NewWSMessageTimer(method)
	defer timer.Observe()

	// Retry configuration: 3 attempts with exponential backoff (1s, 2s, 4s)
	const maxRetries = 3
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := c.callOnce(ctx, method, params, result)
		if err == nil {
			return nil
		}

		lastErr = err

		// Check if this is a retryable connection error
		if !isConnectionError(err) {
			// Not a connection error, don't retry
			return err
		}

		// Check if context is still valid for retry
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Don't retry if client is closed
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return ErrClientClosed
		}

		if attempt < maxRetries {
			// Exponential backoff: 1s, 2s, 4s
			backoff := time.Duration(1<<(attempt-1)) * time.Second
			klog.V(4).Infof("Request failed with connection error (attempt %d/%d): %v, retrying in %v...",
				attempt, maxRetries, err, backoff)

			select {
			case <-time.After(backoff):
				// Continue to next attempt
			case <-ctx.Done():
				return ctx.Err()
			case <-c.closeCh:
				return ErrClientClosed
			}
		}
	}

	return fmt.Errorf("request failed after %d attempts: %w", maxRetries, lastErr)
}

// callOnce makes a single JSON-RPC 2.0 call attempt.
func (c *Client) callOnce(ctx context.Context, method string, params []interface{}, result interface{}) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClientClosed
	}

	// Generate request ID
	id := strconv.FormatUint(atomic.AddUint64(&c.reqID, 1), 10)

	// Create request
	req := &Request{
		ID:      id,
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}

	// Create response channel
	respCh := make(chan *Response, 1)
	c.pending[id] = respCh

	// Send request (log method and id only to avoid logging sensitive data in params)
	klog.V(5).Infof("Sending request: method=%s, id=%s", method, id)
	if err := c.conn.WriteJSON(req); err != nil {
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("failed to send request: %w", err)
	}
	metrics.RecordWSMessage("sent")
	c.mu.Unlock()

	// Wait for response
	select {
	case resp, ok := <-respCh:
		if !ok {
			// Channel was closed, connection error occurred
			return ErrConnectionClosed
		}
		metrics.RecordWSMessage("received")
		if resp.Error != nil {
			return resp.Error
		}
		if result != nil && resp.Result != nil {
			if err := json.Unmarshal(resp.Result, result); err != nil {
				return fmt.Errorf("failed to unmarshal result: %w", err)
			}
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	case <-c.closeCh:
		return ErrClientClosed
	}
}

// readLoop reads responses from WebSocket.
func (c *Client) readLoop() {
	defer c.cleanupReadLoop()

	for {
		if err := c.setReadDeadlineIfConnected(); err != nil {
			klog.Warningf("Failed to set read deadline: %v", err)
		}

		_, rawMsg, err := c.conn.ReadMessage()
		if err != nil {
			if c.handleReadError(err) {
				continue // Successfully handled, continue loop
			}
			return // Unrecoverable error, exit loop
		}

		c.processResponse(rawMsg)
	}
}

// cleanupReadLoop performs cleanup when readLoop exits.
func (c *Client) cleanupReadLoop() {
	c.mu.Lock()
	c.closed = true
	for _, ch := range c.pending {
		close(ch)
	}
	c.pending = make(map[string]chan *Response)
	c.mu.Unlock()
	close(c.closeCh)
}

// setReadDeadlineIfConnected sets read deadline if connection is active.
func (c *Client) setReadDeadlineIfConnected() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	return c.conn.SetReadDeadline(time.Now().Add(40 * time.Second))
}

// handleReadError handles WebSocket read errors with reconnection logic.
// Returns true if error was handled and loop should continue, false if loop should exit.
func (c *Client) handleReadError(err error) bool {
	// Check if client was intentionally closed
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return false
	}
	c.mu.Unlock()

	// Log error only if not a normal closure
	if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		klog.Errorf("WebSocket read error: %v", err)
	}

	// Attempt to reconnect
	if c.reconnect() {
		klog.Info("Successfully reconnected to storage WebSocket")
		return true
	}

	// Reconnection failed, attempt full reinitialization
	return c.reinitializeConnection()
}

// reinitializeConnection performs full connection reinitialization after reconnect failures.
// Returns true if reinitialization succeeded, false if it failed.
func (c *Client) reinitializeConnection() bool {
	klog.Warning("Failed to reconnect after 5 attempts, will reinitialize connection in 30 seconds...")
	time.Sleep(30 * time.Second)

	klog.Info("Reinitializing WebSocket connection from scratch...")
	if err := c.connect(); err != nil {
		klog.Errorf("Connection reinitialization failed: %v, will retry", err)
		return true // Continue loop to retry
	}

	if err := c.authenticateDirect(); err != nil {
		klog.Errorf("Re-authentication after reinitialization failed: %v, will retry", err)
		return true // Continue loop to retry
	}

	klog.Info("Successfully reinitialized WebSocket connection")
	return true
}

// processResponse unmarshals and dispatches a response to the waiting caller.
func (c *Client) processResponse(rawMsg []byte) {
	klog.V(5).Infof("Received raw response: %s", string(rawMsg))

	var resp Response
	if err := json.Unmarshal(rawMsg, &resp); err != nil {
		klog.Errorf("Failed to unmarshal response: %v", err)
		return
	}

	klog.V(5).Infof("Parsed response: %+v", resp)

	c.mu.Lock()
	if ch, ok := c.pending[resp.ID]; ok {
		delete(c.pending, resp.ID)
		ch <- &resp
		close(ch)
	}
	c.mu.Unlock()
}

// reconnect attempts to reconnect to the WebSocket and re-authenticate.
func (c *Client) reconnect() bool {
	c.mu.Lock()
	if c.reconnecting {
		c.mu.Unlock()
		return false
	}
	c.reconnecting = true
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		c.reconnecting = false
		c.mu.Unlock()
	}()

	klog.Warning("WebSocket connection lost, attempting to reconnect...")

	// Update metrics - connection lost
	metrics.SetWSConnectionStatus(false)

	for attempt := 1; attempt <= c.maxRetries; attempt++ {
		// Record reconnection attempt
		metrics.RecordWSReconnection()
		// Exponential backoff: 2^(attempt-1) * retryInterval, max 60s
		// Use max(0, attempt-1) to satisfy gosec G115 (integer overflow check)
		shift := attempt - 1
		if shift < 0 {
			shift = 0
		}
		backoff := time.Duration(1<<shift) * c.retryInterval
		if backoff > 60*time.Second {
			backoff = 60 * time.Second
		}

		klog.Infof("Reconnection attempt %d/%d (waiting %v)...", attempt, c.maxRetries, backoff)
		// Wait with cancellation support
		select {
		case <-time.After(backoff):
		case <-c.closeCh:
			klog.Info("Reconnection canceled - client is closing")
			return false
		}

		// Close old connection
		c.mu.Lock()
		if c.conn != nil {
			if err := c.conn.Close(); err != nil {
				klog.V(5).Infof("Error closing old connection: %v", err)
			}
		}
		// Reset pending requests for new connection
		for _, ch := range c.pending {
			close(ch)
		}
		c.pending = make(map[string]chan *Response)
		c.mu.Unlock()

		// Attempt to reconnect
		if err := c.connect(); err != nil {
			klog.Errorf("Reconnection attempt %d failed: %v", attempt, err)
			continue
		}

		// Re-authenticate using direct read (since readLoop is blocked here)
		if err := c.authenticateDirect(); err != nil {
			klog.Errorf("Re-authentication attempt %d failed: %v", attempt, err)
			continue
		}

		klog.Infof("Successfully reconnected on attempt %d", attempt)
		return true
	}

	return false
}

// pingLoop sends periodic pings to keep the connection alive and detect failures.
func (c *Client) pingLoop() {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.mu.Lock()
			if c.closed || c.conn == nil {
				c.mu.Unlock()
				return
			}

			// Update connection duration metric
			if !c.connectedAt.IsZero() {
				metrics.SetWSConnectionDuration(time.Since(c.connectedAt))
			}

			// Set write deadline for ping
			if err := c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
				klog.Errorf("Failed to set write deadline: %v", err)
				c.mu.Unlock()
				continue
			}

			// Send ping
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				klog.Warningf("Failed to send ping: %v", err)
				c.mu.Unlock()
				continue
			}

			// Reset write deadline
			if err := c.conn.SetWriteDeadline(time.Time{}); err != nil {
				klog.Warningf("Failed to reset write deadline: %v", err)
			}
			c.mu.Unlock()

			klog.V(6).Info("Sent WebSocket ping")

		case <-c.closeCh:
			return
		}
	}
}

// Close closes the client connection.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return
	}

	klog.V(4).Info("Closing storage API client")
	c.closed = true

	if c.conn != nil {
		if err := c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")); err != nil {
			klog.V(5).Infof("Error sending close message: %v", err)
		}
		if err := c.conn.Close(); err != nil {
			klog.V(5).Infof("Error closing connection: %v", err)
		}
	}
}

// Pool API methods

var (
	// ErrPoolNotFound is returned when a requested pool is not found.
	ErrPoolNotFound = errors.New("pool not found")
)

// Pool represents a ZFS storage pool.
//
//nolint:govet // Field alignment optimized for JSON unmarshaling performance
type Pool struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Topology struct {
		Data []interface{} `json:"data"`
	} `json:"topology"`
	Status string `json:"status"`
	Path   string `json:"path"`
	// Capacity fields from the TrueNAS pool.query API
	Properties struct {
		Size struct {
			Parsed int64 `json:"parsed"` // Total pool size in bytes
		} `json:"size"`
		Allocated struct {
			Parsed int64 `json:"parsed"` // Used space in bytes
		} `json:"allocated"`
		Free struct {
			Parsed int64 `json:"parsed"` // Available space in bytes
		} `json:"free"`
		Capacity struct {
			Parsed int64 `json:"parsed"` // Capacity percentage (0-100)
		} `json:"capacity"`
	} `json:"properties"`
}

// QueryPool retrieves information about a specific ZFS pool.
func (c *Client) QueryPool(ctx context.Context, poolName string) (*Pool, error) {
	klog.V(4).Infof("Querying pool: %s", poolName)

	var result []Pool
	err := c.Call(ctx, "pool.query", []interface{}{
		[]interface{}{
			[]interface{}{"name", "=", poolName},
		},
	}, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to query pool: %w", err)
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrPoolNotFound, poolName)
	}

	klog.V(4).Infof("Successfully queried pool %s: size=%d bytes, free=%d bytes, used=%d bytes",
		result[0].Name,
		result[0].Properties.Size.Parsed,
		result[0].Properties.Free.Parsed,
		result[0].Properties.Allocated.Parsed)

	return &result[0], nil
}

// Dataset API methods

// DatasetCreateParams represents parameters for dataset creation.
// Supports configurable ZFS properties via StorageClass parameters.
type DatasetCreateParams struct {
	Name string `json:"name"`
	Type string `json:"type"` // FILESYSTEM, VOLUME

	// ZFS Properties (optional - passed to TrueNAS pool.dataset.create API)
	// These can be configured per-StorageClass with the "zfs." prefix
	// Example StorageClass parameter: zfs.compression: "lz4"

	// Compression algorithm: off, lz4, gzip, gzip-1 through gzip-9, zstd, zstd-1 through zstd-19, lzjb, zle
	Compression string `json:"compression,omitempty"`
	// Deduplication: off, on, verify, sha256, sha512
	Dedup string `json:"dedup,omitempty"`
	// Access time updates: on, off
	Atime string `json:"atime,omitempty"`
	// Synchronous write behavior: standard, always, disabled
	Sync string `json:"sync,omitempty"`
	// Record size: 512, 1K, 2K, 4K, 8K, 16K, 32K, 64K, 128K, 256K, 512K, 1M
	Recordsize string `json:"recordsize,omitempty"`
	// Number of data copies: 1, 2, 3
	Copies *int `json:"copies,omitempty"`
	// Snapshot directory visibility: hidden, visible
	Snapdir string `json:"snapdir,omitempty"`
	// Read-only mode: on, off
	Readonly string `json:"readonly,omitempty"`
	// Executable files: on, off
	Exec string `json:"exec,omitempty"`
	// ACL mode: passthrough, restricted, discard, groupmask
	Aclmode string `json:"aclmode,omitempty"`
	// ACL type: off, nfsv4, posix
	Acltype string `json:"acltype,omitempty"`
	// Case sensitivity: sensitive, insensitive, mixed (only at creation, cannot be changed)
	Casesensitivity string `json:"casesensitivity,omitempty"`
}

// Dataset represents a ZFS dataset.
type Dataset struct {
	Available  map[string]interface{} `json:"available,omitempty"`
	Used       map[string]interface{} `json:"used,omitempty"`
	Volsize    map[string]interface{} `json:"volsize,omitempty"` // ZVOL size (for VOLUME type datasets)
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	Type       string                 `json:"type"`
	Mountpoint string                 `json:"mountpoint,omitempty"`
}

// CreateDataset creates a new ZFS dataset.
func (c *Client) CreateDataset(ctx context.Context, params DatasetCreateParams) (*Dataset, error) {
	klog.V(4).Infof("Creating dataset: %s", params.Name)

	var result Dataset
	err := c.Call(ctx, "pool.dataset.create", []interface{}{params}, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to create dataset: %w", err)
	}

	klog.V(4).Infof("Successfully created dataset: %s", result.Name)
	return &result, nil
}

// DeleteDataset deletes a ZFS dataset.
func (c *Client) DeleteDataset(ctx context.Context, datasetID string) error {
	klog.Infof("DeleteDataset: Starting deletion of dataset %s", datasetID)

	// Use recursive and force flags to ensure dataset is deleted even if it has snapshots or children
	// This prevents orphaned datasets when volumes are deleted after creating snapshots
	var result bool
	params := []interface{}{
		datasetID,
		map[string]interface{}{
			"recursive": true,
			"force":     true,
		},
	}
	err := c.Call(ctx, "pool.dataset.delete", params, &result)
	if err != nil {
		klog.Errorf("DeleteDataset: API call failed for %s: %v", datasetID, err)
		return fmt.Errorf("failed to delete dataset: %w", err)
	}

	klog.Infof("DeleteDataset: TrueNAS API returned result=%v for dataset %s", result, datasetID)

	// TrueNAS API returns true on success, false on failure
	// We must check this because the API may return false without an error
	if !result {
		klog.Errorf("DeleteDataset: TrueNAS returned false for %s - deletion unsuccessful", datasetID)
		return fmt.Errorf("%w: %s", ErrDatasetDeletionFailed, datasetID)
	}

	klog.Infof("DeleteDataset: Successfully deleted dataset %s", datasetID)
	return nil
}

// Dataset retrieves dataset information.
func (c *Client) Dataset(ctx context.Context, datasetID string) (*Dataset, error) {
	klog.V(4).Infof("Getting dataset: %s", datasetID)

	var result Dataset
	err := c.Call(ctx, "pool.dataset.query", []interface{}{
		[]interface{}{
			[]interface{}{"id", "=", datasetID},
		},
	}, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to get dataset: %w", err)
	}

	return &result, nil
}

// NFS Share API methods

// NFSShareCreateParams represents parameters for NFS share creation.
type NFSShareCreateParams struct {
	Path         string   `json:"path"`
	Comment      string   `json:"comment,omitempty"`
	MaprootUser  string   `json:"maproot_user,omitempty"`
	MaprootGroup string   `json:"maproot_group,omitempty"`
	Hosts        []string `json:"hosts,omitempty"`
	Networks     []string `json:"networks,omitempty"`
	Enabled      bool     `json:"enabled"`
}

// NFSShare represents an NFS share.
type NFSShare struct {
	Path    string   `json:"path"`
	Comment string   `json:"comment"`
	Hosts   []string `json:"hosts"`
	ID      int      `json:"id"`
	Enabled bool     `json:"enabled"`
}

// CreateNFSShare creates a new NFS share.
func (c *Client) CreateNFSShare(ctx context.Context, params NFSShareCreateParams) (*NFSShare, error) {
	klog.V(4).Infof("Creating NFS share for path: %s", params.Path)

	var result NFSShare
	err := c.Call(ctx, "sharing.nfs.create", []interface{}{params}, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to create NFS share: %w", err)
	}

	klog.V(4).Infof("Successfully created NFS share with ID: %d", result.ID)
	return &result, nil
}

// DeleteNFSShare deletes an NFS share.
func (c *Client) DeleteNFSShare(ctx context.Context, shareID int) error {
	klog.V(4).Infof("Deleting NFS share: %d", shareID)

	var result bool
	err := c.Call(ctx, "sharing.nfs.delete", []interface{}{shareID}, &result)
	if err != nil {
		return fmt.Errorf("failed to delete NFS share: %w", err)
	}

	// TrueNAS API returns true on success, false on failure
	if !result {
		return fmt.Errorf("%w: share ID %d", ErrNFSShareDeletionFailed, shareID)
	}

	klog.V(4).Infof("Successfully deleted NFS share: %d", shareID)
	return nil
}

// QueryNFSShare queries NFS shares by path.
func (c *Client) QueryNFSShare(ctx context.Context, path string) ([]NFSShare, error) {
	klog.V(4).Infof("Querying NFS shares for path: %s", path)

	var result []NFSShare
	err := c.Call(ctx, "sharing.nfs.query", []interface{}{
		[]interface{}{
			[]interface{}{"path", "=", path},
		},
	}, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to query NFS shares: %w", err)
	}

	return result, nil
}

// NVMe-oF API methods

// ZvolCreateParams represents parameters for ZVOL creation.
// Supports configurable ZFS properties via StorageClass parameters.
//
//nolint:govet // fieldalignment: struct layout prioritizes readability over memory optimization
type ZvolCreateParams struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Volblocksize string `json:"volblocksize,omitempty"`
	Volsize      int64  `json:"volsize"`

	// ZFS Properties (optional - passed to TrueNAS pool.dataset.create API)
	// These can be configured per-StorageClass with the "zfs." prefix
	// Example StorageClass parameter: zfs.compression: "lz4"

	// Compression algorithm: off, lz4, gzip, gzip-1 through gzip-9, zstd, zstd-1 through zstd-19, lzjb, zle
	Compression string `json:"compression,omitempty"`
	// Deduplication: off, on, verify, sha256, sha512
	Dedup string `json:"dedup,omitempty"`
	// Synchronous write behavior: standard, always, disabled
	Sync string `json:"sync,omitempty"`
	// Number of data copies: 1, 2, 3
	Copies *int `json:"copies,omitempty"`
	// Read-only mode: on, off
	Readonly string `json:"readonly,omitempty"`
	// Sparse ZVOL (thin provisioning): true allocates space on demand
	Sparse *bool `json:"sparse,omitempty"`
}

// CreateZvol creates a new ZVOL (block device).
func (c *Client) CreateZvol(ctx context.Context, params ZvolCreateParams) (*Dataset, error) {
	klog.V(4).Infof("Creating ZVOL: %s (size: %d)", params.Name, params.Volsize)

	var result Dataset
	err := c.Call(ctx, "pool.dataset.create", []interface{}{params}, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to create ZVOL: %w", err)
	}

	klog.V(4).Infof("Successfully created ZVOL: %s", result.Name)
	return &result, nil
}

// NVMeOFSubsystemCreateParams represents parameters for NVMe-oF subsystem creation.
type NVMeOFSubsystemCreateParams struct {
	Name         string `json:"name"`
	AllowAnyHost bool   `json:"allow_any_host"` // Allow any host to connect
}

// NVMeOFSubsystem represents an NVMe-oF subsystem.
type NVMeOFSubsystem struct {
	Name    string `json:"name"`   // Short NQN without UUID prefix
	NQN     string `json:"subnqn"` // Full NQN with UUID prefix
	Serial  string `json:"serial"`
	ID      int    `json:"id"`
	Enabled bool   `json:"enabled"`
}

// CreateNVMeOFSubsystem creates a new NVMe-oF subsystem.
func (c *Client) CreateNVMeOFSubsystem(ctx context.Context, params NVMeOFSubsystemCreateParams) (*NVMeOFSubsystem, error) {
	klog.V(4).Infof("Creating NVMe-oF subsystem: %s", params.Name)

	var result NVMeOFSubsystem
	err := c.Call(ctx, "nvmet.subsys.create", []interface{}{params}, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to create NVMe-oF subsystem: %w", err)
	}

	klog.V(4).Infof("Successfully created NVMe-oF subsystem with ID: %d", result.ID)
	return &result, nil
}

// DeleteNVMeOFSubsystem deletes an NVMe-oF subsystem.
func (c *Client) DeleteNVMeOFSubsystem(ctx context.Context, subsystemID int) error {
	klog.V(4).Infof("Deleting NVMe-oF subsystem: %d", subsystemID)

	var result bool
	err := c.Call(ctx, "nvmet.subsys.delete", []interface{}{subsystemID}, &result)
	if err != nil {
		return fmt.Errorf("failed to delete NVMe-oF subsystem: %w", err)
	}

	// TrueNAS API returns true on success, false on failure
	if !result {
		return fmt.Errorf("%w: subsystem ID %d", ErrSubsystemDeletionFailed, subsystemID)
	}

	klog.V(4).Infof("Successfully deleted NVMe-oF subsystem: %d", subsystemID)
	return nil
}

// NVMeOFNamespaceCreateParams represents parameters for NVMe-oF namespace creation.
type NVMeOFNamespaceCreateParams struct {
	DevicePath string `json:"device_path"`
	DeviceType string `json:"device_type"`
	SubsysID   int    `json:"subsys_id"`
	NSID       int    `json:"nsid,omitempty"`
}

// NVMeOFNamespaceSubsystem represents the nested subsystem object in namespace responses.
type NVMeOFNamespaceSubsystem struct {
	Name   string `json:"name"`   // Short NQN (e.g., "nqn.2137.csi.tns:pvc-...")
	SubNQN string `json:"subnqn"` // Full NQN with UUID prefix
	ID     int    `json:"id"`
}

// NVMeOFNamespace represents an NVMe-oF namespace.
type NVMeOFNamespace struct {
	Subsys     *NVMeOFNamespaceSubsystem `json:"subsys"`      // Nested subsystem object from TrueNAS API
	Device     string                    `json:"device"`      // Device path from API response
	DevicePath string                    `json:"device_path"` // Alternative field name that TrueNAS might use
	ID         int                       `json:"id"`
	NSID       int                       `json:"nsid"`
}

// GetDevice returns the device path, trying both possible field names.
func (n *NVMeOFNamespace) GetDevice() string {
	if n.Device != "" {
		return n.Device
	}
	return n.DevicePath
}

// GetSubsystemID returns the subsystem ID from the nested subsys object.
func (n *NVMeOFNamespace) GetSubsystemID() int {
	if n.Subsys != nil {
		return n.Subsys.ID
	}
	return 0
}

// GetSubsystemNQN returns the short subsystem NQN (name field) from the nested subsys object.
func (n *NVMeOFNamespace) GetSubsystemNQN() string {
	if n.Subsys != nil {
		return n.Subsys.Name
	}
	return ""
}

// GetSubsystemSubNQN returns the full subsystem NQN (subnqn field) from the nested subsys object.
func (n *NVMeOFNamespace) GetSubsystemSubNQN() string {
	if n.Subsys != nil {
		return n.Subsys.SubNQN
	}
	return ""
}

// CreateNVMeOFNamespace creates a new NVMe-oF namespace.
func (c *Client) CreateNVMeOFNamespace(ctx context.Context, params NVMeOFNamespaceCreateParams) (*NVMeOFNamespace, error) {
	klog.V(4).Infof("Creating NVMe-oF namespace for device: %s", params.DevicePath)

	var result NVMeOFNamespace
	err := c.Call(ctx, "nvmet.namespace.create", []interface{}{params}, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to create NVMe-oF namespace: %w", err)
	}

	klog.V(4).Infof("Successfully created NVMe-oF namespace with ID: %d (NSID: %d)", result.ID, result.NSID)
	return &result, nil
}

// DeleteNVMeOFNamespace deletes an NVMe-oF namespace.
func (c *Client) DeleteNVMeOFNamespace(ctx context.Context, namespaceID int) error {
	klog.V(4).Infof("Deleting NVMe-oF namespace: %d", namespaceID)

	var result bool
	err := c.Call(ctx, "nvmet.namespace.delete", []interface{}{namespaceID}, &result)
	if err != nil {
		return fmt.Errorf("failed to delete NVMe-oF namespace: %w", err)
	}

	// TrueNAS API returns true on success, false on failure
	if !result {
		return fmt.Errorf("%w: namespace ID %d", ErrNamespaceDeletionFailed, namespaceID)
	}

	klog.V(4).Infof("Successfully deleted NVMe-oF namespace: %d", namespaceID)
	return nil
}

// QueryNVMeOFSubsystem queries NVMe-oF subsystems by NQN.
// This lists all subsystems and filters client-side by the 'name' field,
// since TrueNAS uses 'name' for the short NQN and 'subnqn' for the full UUID-prefixed NQN.
func (c *Client) QueryNVMeOFSubsystem(ctx context.Context, nqn string) ([]NVMeOFSubsystem, error) {
	klog.V(4).Infof("Querying NVMe-oF subsystems for NQN: %s", nqn)

	// List all subsystems - server-side filtering doesn't work reliably
	// because the NQN field name varies between TrueNAS versions
	allSubsystems, err := c.ListAllNVMeOFSubsystems(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list subsystems: %w", err)
	}

	// Filter client-side by matching the 'name' field (short NQN without UUID prefix)
	// The API response has both:
	// - 'name': short NQN (e.g., "nqn.2005-03.org.truenas:csi-test")
	// - 'subnqn': full NQN with UUID prefix (e.g., "nqn.2011-06.com.truenas:uuid:<uuid>:nqn.2005-03.org.truenas:csi-test")
	var result []NVMeOFSubsystem
	for _, sub := range allSubsystems {
		if sub.Name == nqn {
			result = append(result, sub)
		}
	}

	klog.V(4).Infof("Found %d subsystems matching NQN: %s", len(result), nqn)
	return result, nil
}

// NVMeOFSubsystemByNQN retrieves a single NVMe-oF subsystem by NQN.
// Returns error if subsystem is not found or if multiple subsystems match.
func (c *Client) NVMeOFSubsystemByNQN(ctx context.Context, nqn string) (*NVMeOFSubsystem, error) {
	klog.V(4).Infof("Getting NVMe-oF subsystem for NQN: %s", nqn)

	subsystems, err := c.QueryNVMeOFSubsystem(ctx, nqn)
	if err != nil {
		klog.Errorf("Failed to query NVMe-oF subsystem: %v", err)

		// Try to list all subsystems for debugging
		klog.Infof("Attempting to list all NVMe-oF subsystems for debugging...")
		allSubsystems, listErr := c.ListAllNVMeOFSubsystems(ctx)
		if listErr != nil {
			klog.Errorf("Failed to list all subsystems: %v", listErr)
		} else {
			klog.Infof("Found %d total NVMe-oF subsystems:", len(allSubsystems))
			for _, sub := range allSubsystems {
				klog.Infof("  - ID=%d, NQN=%s", sub.ID, sub.NQN)
			}
		}

		return nil, fmt.Errorf("failed to query subsystem: %w", err)
	}

	if len(subsystems) == 0 {
		// Try listing all subsystems to help with debugging
		klog.Warningf("No subsystems found with NQN %s, listing all subsystems...", nqn)
		allSubsystems, listErr := c.ListAllNVMeOFSubsystems(ctx)
		if listErr == nil {
			klog.Infof("Found %d total NVMe-oF subsystems:", len(allSubsystems))
			for _, sub := range allSubsystems {
				klog.Infof("  - ID=%d, Name=%s, FullNQN=%s", sub.ID, sub.Name, sub.NQN)
			}
		}
		return nil, fmt.Errorf("%w: NQN %s", ErrSubsystemNotFound, nqn)
	}

	if len(subsystems) > 1 {
		return nil, fmt.Errorf("%w: NQN %s (expected 1, found %d)", ErrMultipleSubsystems, nqn, len(subsystems))
	}

	klog.V(4).Infof("Found NVMe-oF subsystem: ID=%d, Name=%s, FullNQN=%s", subsystems[0].ID, subsystems[0].Name, subsystems[0].NQN)
	return &subsystems[0], nil
}

// ListAllNVMeOFSubsystems lists all NVMe-oF subsystems (no filter).
func (c *Client) ListAllNVMeOFSubsystems(ctx context.Context) ([]NVMeOFSubsystem, error) {
	klog.V(4).Infof("Listing all NVMe-oF subsystems")

	var result []NVMeOFSubsystem

	// Try different API methods to find the correct one
	apiMethods := []string{
		"sharing.nvme.query",
		"nvmet.subsystem.query",
		"nvmet.subsys.query",
		"iscsi.nvme.subsystem.query",
	}

	for _, method := range apiMethods {
		klog.V(4).Infof("Trying API method: %s (no filter)", method)
		// Empty filter to get all subsystems
		err := c.Call(ctx, method, []interface{}{}, &result)

		if err == nil {
			klog.V(4).Infof("Successfully listed using method %s, found %d subsystems", method, len(result))
			return result, nil
		}

		klog.V(4).Infof("Method %s failed: %v", method, err)
	}

	return nil, ErrListSubsystemsFailed
}

// AddSubsystemToPort associates an NVMe-oF subsystem with a port.
func (c *Client) AddSubsystemToPort(ctx context.Context, subsystemID, portID int) error {
	klog.V(4).Infof("Adding subsystem %d to port %d", subsystemID, portID)

	// Use nvmet.port_subsys.create to create port-subsystem association
	var result map[string]interface{}
	err := c.Call(ctx, "nvmet.port_subsys.create", []interface{}{
		map[string]interface{}{
			"port_id":   portID,
			"subsys_id": subsystemID,
		},
	}, &result)
	if err != nil {
		return fmt.Errorf("failed to add subsystem %d to port %d: %w", subsystemID, portID, err)
	}

	klog.V(4).Infof("Successfully added subsystem %d to port %d", subsystemID, portID)
	return nil
}

// NVMeOFPortSubsystem represents a port-subsystem association.
// TrueNAS API returns this with fields like "port", "subsys" (nested objects containing id, name, etc.)
type NVMeOFPortSubsystem struct {
	Port        json.RawMessage `json:"port"`      // Can be int or object with id field
	Subsystem   json.RawMessage `json:"subsystem"` // Alternative field name (may not be used)
	Subsys      json.RawMessage `json:"subsys"`    // Nested object: {"id": int, "name": "...", "subnqn": "..."}
	ID          int             `json:"id"`        // Binding ID
	PortID      int             `json:"port_id"`   // Direct port ID (may not be present)
	SubsystemID int             `json:"subsys_id"` // Direct subsystem ID (may not be present)
	SubsysID    int             `json:"subsysid"`  // Alternative field name
}

// GetPortID returns the port ID, trying multiple possible field names.
func (ps *NVMeOFPortSubsystem) GetPortID() int {
	if ps.PortID != 0 {
		return ps.PortID
	}
	// Try to parse Port as int
	if len(ps.Port) > 0 {
		var portInt int
		if err := json.Unmarshal(ps.Port, &portInt); err == nil {
			return portInt
		}
		// Try to parse as object with id field
		var portObj struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal(ps.Port, &portObj); err == nil && portObj.ID != 0 {
			return portObj.ID
		}
	}
	return 0
}

// GetSubsystemID returns the subsystem ID, trying multiple possible field names and formats.
// TrueNAS may return subsystem as:
// - Direct field: subsys_id, subsysid.
// - Nested object in "subsys": {"id": 338, "name": "...", ...}.
// - Nested object in "subsystem": {"id": 338, "name": "...", ...}.
func (ps *NVMeOFPortSubsystem) GetSubsystemID() int {
	// Try direct fields first
	if ps.SubsystemID != 0 {
		return ps.SubsystemID
	}
	if ps.SubsysID != 0 {
		return ps.SubsysID
	}

	// Try to parse Subsys (the actual field name TrueNAS uses) as object or int
	if len(ps.Subsys) > 0 {
		// Try as int first
		var subsysInt int
		if err := json.Unmarshal(ps.Subsys, &subsysInt); err == nil && subsysInt != 0 {
			return subsysInt
		}
		// Try to parse as object with id field
		var subsysObj struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal(ps.Subsys, &subsysObj); err == nil && subsysObj.ID != 0 {
			return subsysObj.ID
		}
	}

	// Fallback: Try Subsystem field (alternative naming)
	if len(ps.Subsystem) > 0 {
		var subsysInt int
		if err := json.Unmarshal(ps.Subsystem, &subsysInt); err == nil && subsysInt != 0 {
			return subsysInt
		}
		var subsysObj struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal(ps.Subsystem, &subsysObj); err == nil && subsysObj.ID != 0 {
			return subsysObj.ID
		}
	}
	return 0
}

// QuerySubsystemPortBindings queries all port bindings for a specific subsystem.
func (c *Client) QuerySubsystemPortBindings(ctx context.Context, subsystemID int) ([]NVMeOFPortSubsystem, error) {
	klog.V(4).Infof("Querying port bindings for subsystem %d", subsystemID)

	// First, get raw JSON to debug the actual field names
	var rawResult json.RawMessage
	err := c.Call(ctx, "nvmet.port_subsys.query", []interface{}{}, &rawResult)
	if err != nil {
		return nil, fmt.Errorf("failed to query port-subsystem bindings: %w", err)
	}

	// Log raw JSON for debugging (first 2000 chars to avoid log spam)
	rawStr := string(rawResult)
	if len(rawStr) > 2000 {
		rawStr = rawStr[:2000] + "..."
	}
	klog.Infof("QuerySubsystemPortBindings: Raw JSON response: %s", rawStr)

	// Now unmarshal into our struct
	var allBindings []NVMeOFPortSubsystem
	if err := json.Unmarshal(rawResult, &allBindings); err != nil {
		return nil, fmt.Errorf("failed to unmarshal port-subsystem bindings: %w", err)
	}

	klog.Infof("QuerySubsystemPortBindings: Found %d total port bindings", len(allBindings))

	// Filter for this specific subsystem
	var result []NVMeOFPortSubsystem
	for _, binding := range allBindings {
		subsysID := binding.GetSubsystemID()
		klog.V(5).Infof("QuerySubsystemPortBindings: Binding ID=%d, SubsystemID=%d (looking for %d)",
			binding.ID, subsysID, subsystemID)
		if subsysID == subsystemID {
			result = append(result, binding)
		}
	}

	klog.Infof("Found %d port binding(s) for subsystem %d", len(result), subsystemID)
	return result, nil
}

// RemoveSubsystemFromPort removes an NVMe-oF subsystem from a port binding.
func (c *Client) RemoveSubsystemFromPort(ctx context.Context, portSubsysID int) error {
	klog.V(4).Infof("Removing port-subsystem binding: %d", portSubsysID)

	var result bool
	err := c.Call(ctx, "nvmet.port_subsys.delete", []interface{}{portSubsysID}, &result)
	if err != nil {
		return fmt.Errorf("failed to remove port-subsystem binding %d: %w", portSubsysID, err)
	}

	klog.V(4).Infof("Successfully removed port-subsystem binding: %d", portSubsysID)
	return nil
}

// QueryNVMeOFPorts queries available NVMe-oF ports.
func (c *Client) QueryNVMeOFPorts(ctx context.Context) ([]NVMeOFPort, error) {
	klog.V(4).Info("Querying NVMe-oF ports")

	var result []NVMeOFPort
	err := c.Call(ctx, "nvmet.port.query", []interface{}{}, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to query NVMe-oF ports: %w", err)
	}

	return result, nil
}

// NVMeOFPort represents an NVMe-oF port/listener.
type NVMeOFPort struct {
	Transport string `json:"addr_trtype"`
	Address   string `json:"addr_traddr"`
	ID        int    `json:"id"`
	Port      int    `json:"addr_trsvcid"`
}

// Dataset Update API methods

// DatasetUpdateParams represents parameters for dataset update.
type DatasetUpdateParams struct {
	Quota               *int64 `json:"quota,omitempty"`                // Quota in bytes (for NFS)
	RefQuota            *int64 `json:"refquota,omitempty"`             // Reference quota in bytes
	Volsize             *int64 `json:"volsize,omitempty"`              // Volume size in bytes (for ZVOLs)
	RefreservPercentage *int   `json:"refreserv_percentage,omitempty"` // Reference reservation percentage
	Comments            string `json:"comments,omitempty"`             // Comments
}

// UpdateDataset updates a ZFS dataset or ZVOL.
func (c *Client) UpdateDataset(ctx context.Context, datasetID string, params DatasetUpdateParams) (*Dataset, error) {
	klog.V(4).Infof("Updating dataset: %s with params: %+v", datasetID, params)

	var result Dataset
	err := c.Call(ctx, "pool.dataset.update", []interface{}{datasetID, params}, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to update dataset: %w", err)
	}

	klog.V(4).Infof("Successfully updated dataset: %s", result.Name)
	return &result, nil
}

// Snapshot API methods

// SnapshotCreateParams represents parameters for snapshot creation.
type SnapshotCreateParams struct {
	Dataset   string `json:"dataset"`             // Dataset name (e.g., "pool/dataset")
	Name      string `json:"name"`                // Snapshot name (will be appended to dataset as dataset@name)
	Recursive bool   `json:"recursive,omitempty"` // Create recursive snapshot
}

// Snapshot represents a ZFS snapshot.
type Snapshot struct {
	Properties map[string]interface{} `json:"properties"` // ZFS properties
	ID         string                 `json:"id"`         // Full snapshot name (dataset@snapshot)
	Name       string                 `json:"name"`       // Snapshot name portion
	Dataset    string                 `json:"dataset"`    // Parent dataset name
	CreateTXG  string                 `json:"createtxg"`  // Creation transaction group
}

// CreateSnapshot creates a new ZFS snapshot.
func (c *Client) CreateSnapshot(ctx context.Context, params SnapshotCreateParams) (*Snapshot, error) {
	klog.V(4).Infof("Creating snapshot %s for dataset %s", params.Name, params.Dataset)

	var result Snapshot
	err := c.Call(ctx, "zfs.snapshot.create", []interface{}{params}, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to create snapshot: %w", err)
	}

	klog.V(4).Infof("Successfully created snapshot: %s", result.ID)
	return &result, nil
}

// DeleteSnapshot deletes a ZFS snapshot.
// Uses defer=true to handle snapshots with dependent clones (ZFS clones from snapshot restore).
// With defer=true, the snapshot will be marked for deletion and automatically removed
// when all dependent clones are destroyed.
func (c *Client) DeleteSnapshot(ctx context.Context, snapshotID string) error {
	klog.V(4).Infof("Deleting snapshot: %s", snapshotID)

	// TrueNAS API expects snapshot deletion parameters
	// Use defer=true to handle snapshots with dependent clones (restored volumes)
	// The snapshot will be automatically deleted when all clones are destroyed
	params := map[string]interface{}{
		"defer": true,
	}

	var result bool
	err := c.Call(ctx, "zfs.snapshot.delete", []interface{}{snapshotID, params}, &result)
	if err != nil {
		return fmt.Errorf("failed to delete snapshot: %w", err)
	}

	// TrueNAS API returns true on success, false on failure
	if !result {
		return fmt.Errorf("%w: %s", ErrSnapshotDeletionFailed, snapshotID)
	}

	klog.V(4).Infof("Successfully deleted snapshot: %s (defer=true)", snapshotID)
	return nil
}

// QuerySnapshots queries ZFS snapshots with optional filters.
func (c *Client) QuerySnapshots(ctx context.Context, filters []interface{}) ([]Snapshot, error) {
	klog.V(4).Infof("Querying snapshots with filters: %+v", filters)

	var result []Snapshot
	err := c.Call(ctx, "zfs.snapshot.query", []interface{}{filters}, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to query snapshots: %w", err)
	}

	klog.V(4).Infof("Found %d snapshots", len(result))
	return result, nil
}

// CloneSnapshotParams represents parameters for cloning a snapshot.
type CloneSnapshotParams struct {
	Snapshot string `json:"snapshot"`    // Source snapshot ID (dataset@snapshot)
	Dataset  string `json:"dataset_dst"` // Destination dataset name
}

// CloneSnapshot clones a ZFS snapshot to a new dataset.
func (c *Client) CloneSnapshot(ctx context.Context, params CloneSnapshotParams) (*Dataset, error) {
	klog.V(4).Infof("Cloning snapshot %s to dataset %s", params.Snapshot, params.Dataset)

	// TrueNAS zfs.snapshot.clone returns a boolean indicating success, not the Dataset object
	var result bool
	err := c.Call(ctx, "zfs.snapshot.clone", []interface{}{params}, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to clone snapshot: %w", err)
	}

	if !result {
		return nil, ErrCloneFailed
	}

	klog.V(4).Infof("Clone operation successful, querying for cloned dataset: %s", params.Dataset)

	// Query the newly cloned dataset to get its full information
	datasets, err := c.queryDatasets(ctx, params.Dataset)
	if err != nil {
		return nil, fmt.Errorf("failed to query cloned dataset: %w", err)
	}

	if len(datasets) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrClonedDatasetNotFound, params.Dataset)
	}

	klog.V(4).Infof("Successfully cloned snapshot to dataset: %s", datasets[0].Name)
	return &datasets[0], nil
}

// PromoteDataset promotes a cloned dataset to become independent from its origin snapshot.
// After promotion, the clone becomes a standalone dataset with no dependency on the parent.
// This is essential for "detached snapshots" where you want an independent copy of data.
//
// The promotion operation:
// 1. Reverses the parent-child relationship between clone and origin
// 2. Makes the clone independent (it no longer depends on the snapshot)
// 3. Allows the original snapshot to be deleted (if no other clones depend on it)
//
// Note: This uses the TrueNAS pool.dataset.promote API which wraps ZFS promote.
func (c *Client) PromoteDataset(ctx context.Context, datasetID string) error {
	klog.V(4).Infof("Promoting dataset: %s", datasetID)

	// TrueNAS pool.dataset.promote takes the dataset ID and returns success/failure
	// The API expects just the dataset ID as a string parameter
	// Note: TrueNAS API returns null on success, which Go unmarshals as false for bool.
	// We use json.RawMessage to capture the raw response and check for errors properly.
	var result json.RawMessage
	err := c.Call(ctx, "pool.dataset.promote", []interface{}{datasetID}, &result)
	if err != nil {
		return fmt.Errorf("failed to promote dataset %s: %w", datasetID, err)
	}

	// If no error was returned, the promote operation succeeded.
	// TrueNAS returns null on success, which is valid.
	klog.V(4).Infof("Successfully promoted dataset: %s (raw response: %s)", datasetID, string(result))
	return nil
}

// queryWithOptionalFilter is a helper function to reduce duplication in query methods.
// The operator parameter specifies the filter operator:
// - "^" for starts-with (prefix match).
// - "~" for regex/contains match.
// - "$" for ends-with (suffix match).
func (c *Client) queryWithOptionalFilter(ctx context.Context, method, filterField, filterValue, operator, resourceType string, result interface{}) error {
	klog.V(5).Infof("Querying all %s with filter: %s (operator: %s)", resourceType, filterValue, operator)

	var filters []interface{}

	// If filter value is specified, apply the filter
	if filterValue != "" {
		filters = []interface{}{
			[]interface{}{filterField, operator, filterValue},
		}
	}

	err := c.Call(ctx, method, []interface{}{filters}, result)
	if err != nil {
		return fmt.Errorf("failed to query %s: %w", resourceType, err)
	}

	return nil
}

// QueryAllDatasets queries all datasets with optional prefix filter.
func (c *Client) QueryAllDatasets(ctx context.Context, prefix string) ([]Dataset, error) {
	var result []Dataset
	if err := c.queryWithOptionalFilter(ctx, "pool.dataset.query", "id", prefix, "^", "datasets", &result); err != nil {
		return nil, err
	}

	klog.V(5).Infof("Found %d datasets", len(result))
	return result, nil
}

// QueryAllNFSShares queries all NFS shares.
// The pathFilter parameter is ignored - all shares are returned and callers
// should filter client-side. This is more reliable than server-side regex
// filtering which may have inconsistent behavior across TrueNAS versions.
func (c *Client) QueryAllNFSShares(ctx context.Context, pathFilter string) ([]NFSShare, error) {
	// Always query all shares - ignore pathFilter parameter
	// Callers filter client-side using strings.HasSuffix or similar
	_ = pathFilter // Explicitly ignore - kept for API compatibility

	klog.V(5).Info("Querying all NFS shares")

	var result []NFSShare
	// Pass empty params to get all shares - TrueNAS API expects either no filter or a valid filter array
	err := c.Call(ctx, "sharing.nfs.query", []interface{}{}, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to query NFS shares: %w", err)
	}

	klog.V(5).Infof("Found %d NFS shares", len(result))
	return result, nil
}

// QueryAllNVMeOFNamespaces queries all NVMe-oF namespaces.
func (c *Client) QueryAllNVMeOFNamespaces(ctx context.Context) ([]NVMeOFNamespace, error) {
	klog.V(5).Info("Querying all NVMe-oF namespaces")

	// First, get raw JSON to debug the actual field names
	var rawResult json.RawMessage
	err := c.Call(ctx, "nvmet.namespace.query", []interface{}{}, &rawResult)
	if err != nil {
		return nil, fmt.Errorf("failed to query NVMe-oF namespaces: %w", err)
	}

	// Log raw JSON for debugging (first 2000 chars to avoid log spam)
	rawStr := string(rawResult)
	if len(rawStr) > 2000 {
		rawStr = rawStr[:2000] + "..."
	}
	klog.Infof("QueryAllNVMeOFNamespaces: Raw JSON response: %s", rawStr)

	// Now unmarshal into our struct
	var result []NVMeOFNamespace
	if err := json.Unmarshal(rawResult, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal NVMe-oF namespaces: %w", err)
	}

	klog.Infof("QueryAllNVMeOFNamespaces: Found %d NVMe-oF namespaces", len(result))
	// Log first 3 namespaces for debugging
	for i, ns := range result {
		if i >= 3 {
			break
		}
		klog.Infof("QueryAllNVMeOFNamespaces: Sample namespace %d: ID=%d, Device='%s', DevicePath='%s', SubsystemID=%d, SubsystemNQN='%s', NSID=%d", i, ns.ID, ns.Device, ns.DevicePath, ns.GetSubsystemID(), ns.GetSubsystemNQN(), ns.NSID)
	}
	return result, nil
}

// queryDatasets queries datasets by name (internal helper).
func (c *Client) queryDatasets(ctx context.Context, datasetName string) ([]Dataset, error) {
	klog.V(5).Infof("Querying datasets with name: %s", datasetName)

	var result []Dataset
	err := c.Call(ctx, "pool.dataset.query", []interface{}{
		[]interface{}{
			[]interface{}{"id", "=", datasetName},
		},
	}, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to query datasets: %w", err)
	}

	return result, nil
}

// ZFS User Property API methods
//
// These methods manage ZFS user properties on datasets, which are used to store
// CSI metadata for reliable tracking and safe deletion verification.

// SetDatasetProperties sets ZFS user properties on a dataset.
// Properties are stored in the ZFS dataset's user_properties field.
// This is used to track CSI metadata like NFS share IDs, NVMe-oF subsystem IDs, etc.
func (c *Client) SetDatasetProperties(ctx context.Context, datasetID string, properties map[string]string) error {
	klog.V(4).Infof("Setting %d user properties on dataset: %s", len(properties), datasetID)

	if len(properties) == 0 {
		return nil
	}

	// TrueNAS pool.dataset.update accepts user_properties as a list of objects
	// The API expects: {"user_properties": [{"key": "property_name", "value": "property_value"}, ...]}
	// Convert our simple map to the list format expected by TrueNAS
	userProps := make([]map[string]string, 0, len(properties))
	for key, value := range properties {
		userProps = append(userProps, map[string]string{
			"key":   key,
			"value": value,
		})
	}

	params := map[string]interface{}{
		"user_properties": userProps,
	}

	var result Dataset
	err := c.Call(ctx, "pool.dataset.update", []interface{}{datasetID, params}, &result)
	if err != nil {
		errStr := err.Error()
		// Ignore 'comments' property errors - this property doesn't exist on ZVOLs
		// but TrueNAS may complain about it when the parent dataset has comments set
		if strings.Contains(errStr, "properties.comments") && strings.Contains(errStr, "does not exist") {
			klog.V(4).Infof("Ignoring 'comments' property error for ZVOL %s (expected for block devices)", datasetID)
			return nil
		}
		return fmt.Errorf("failed to set user properties on dataset %s: %w", datasetID, err)
	}

	klog.V(4).Infof("Successfully set %d user properties on dataset: %s", len(properties), datasetID)
	return nil
}

// DatasetWithProperties represents a dataset with its user properties.
// This struct is used when querying datasets with extra properties included.
//
//nolint:govet // fieldalignment: struct embeds Dataset for readability.
type DatasetWithProperties struct {
	Dataset
	UserProperties map[string]UserProperty `json:"user_properties,omitempty"`
}

// UserProperty represents a ZFS user property value.
type UserProperty struct {
	Value  string `json:"value"`
	Source string `json:"source,omitempty"`
}

// GetDatasetProperties retrieves ZFS user properties from a dataset.
// Returns a map of property name to value for the requested properties.
// Properties that don't exist will not be included in the returned map.
func (c *Client) GetDatasetProperties(ctx context.Context, datasetID string, propertyNames []string) (map[string]string, error) {
	klog.V(4).Infof("Getting %d user properties from dataset: %s", len(propertyNames), datasetID)

	// Query the dataset with extra properties
	// TrueNAS pool.dataset.query supports an "extra" option to include user_properties
	var result []DatasetWithProperties
	queryOpts := map[string]interface{}{
		"extra": map[string]interface{}{
			"properties": true,
		},
	}
	err := c.Call(ctx, "pool.dataset.query", []interface{}{
		[]interface{}{
			[]interface{}{"id", "=", datasetID},
		},
		queryOpts,
	}, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to query dataset properties for %s: %w", datasetID, err)
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("dataset not found: %s: %w", datasetID, ErrDatasetNotFound)
	}

	// Extract requested properties from user_properties
	props := make(map[string]string)
	dataset := result[0]

	if dataset.UserProperties == nil {
		klog.V(4).Infof("Dataset %s has no user properties", datasetID)
		return props, nil
	}

	for _, name := range propertyNames {
		if prop, ok := dataset.UserProperties[name]; ok {
			props[name] = prop.Value
		}
	}

	klog.V(4).Infof("Retrieved %d user properties from dataset: %s", len(props), datasetID)
	return props, nil
}

// GetAllDatasetProperties retrieves all ZFS user properties from a dataset.
// Returns a map of all property names to values.
func (c *Client) GetAllDatasetProperties(ctx context.Context, datasetID string) (map[string]string, error) {
	klog.V(4).Infof("Getting all user properties from dataset: %s", datasetID)

	// Query the dataset with extra properties
	var result []DatasetWithProperties
	queryOpts := map[string]interface{}{
		"extra": map[string]interface{}{
			"properties": true,
		},
	}
	err := c.Call(ctx, "pool.dataset.query", []interface{}{
		[]interface{}{
			[]interface{}{"id", "=", datasetID},
		},
		queryOpts,
	}, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to query dataset properties for %s: %w", datasetID, err)
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("dataset not found: %s: %w", datasetID, ErrDatasetNotFound)
	}

	// Extract all user properties
	props := make(map[string]string)
	dataset := result[0]

	if dataset.UserProperties == nil {
		klog.V(4).Infof("Dataset %s has no user properties", datasetID)
		return props, nil
	}

	for name, prop := range dataset.UserProperties {
		props[name] = prop.Value
	}

	klog.V(4).Infof("Retrieved %d user properties from dataset: %s", len(props), datasetID)
	return props, nil
}

// InheritDatasetProperty removes (inherits) a ZFS user property from a dataset.
// This effectively deletes the property by setting it to inherit from the parent.
func (c *Client) InheritDatasetProperty(ctx context.Context, datasetID, propertyName string) error {
	klog.V(4).Infof("Inheriting (removing) user property %s from dataset: %s", propertyName, datasetID)

	// TrueNAS doesn't have a direct "inherit" API for user properties.
	// To remove a user property, we set it with source="INHERIT" or use pool.dataset.inherit
	// Try the inherit method first
	err := c.Call(ctx, "pool.dataset.inherit", []interface{}{
		datasetID,
		propertyName,
		false, // recursive
	}, nil)
	if err != nil {
		// If inherit fails, try setting to empty value (which effectively clears it)
		klog.V(4).Infof("Inherit method failed, trying to clear property: %v", err)
		clearProps := map[string]interface{}{
			"user_properties": map[string]interface{}{
				propertyName: map[string]string{"value": ""},
			},
		}
		var result Dataset
		err2 := c.Call(ctx, "pool.dataset.update", []interface{}{datasetID, clearProps}, &result)
		if err2 != nil {
			return fmt.Errorf("failed to clear user property %s on dataset %s: %w (inherit also failed: %w)", propertyName, datasetID, err2, err)
		}
	}

	klog.V(4).Infof("Successfully inherited (removed) user property %s from dataset: %s", propertyName, datasetID)
	return nil
}

// ClearDatasetProperties removes multiple ZFS user properties from a dataset.
// This is a convenience method that calls InheritDatasetProperty for each property.
func (c *Client) ClearDatasetProperties(ctx context.Context, datasetID string, propertyNames []string) error {
	klog.V(4).Infof("Clearing %d user properties from dataset: %s", len(propertyNames), datasetID)

	for _, name := range propertyNames {
		if err := c.InheritDatasetProperty(ctx, datasetID, name); err != nil {
			return fmt.Errorf("failed to clear property %s: %w", name, err)
		}
	}

	klog.V(4).Infof("Successfully cleared %d user properties from dataset: %s", len(propertyNames), datasetID)
	return nil
}

// ReplicationRunOnetimeParams contains parameters for running a one-time replication task.
// This is used for creating detached snapshots via zfs send/receive.
//
//nolint:govet // fieldalignment: prefer readability over memory alignment for config structs
type ReplicationRunOnetimeParams struct {
	Direction               string   `json:"direction"`                  // "PUSH" or "PULL"
	Transport               string   `json:"transport"`                  // "LOCAL", "SSH", or "SSH+NETCAT"
	SourceDatasets          []string `json:"source_datasets"`            // Source dataset paths
	TargetDataset           string   `json:"target_dataset"`             // Target dataset path
	Recursive               bool     `json:"recursive"`                  // Recursive replication
	Properties              bool     `json:"properties"`                 // Include ZFS properties
	PropertiesExclude       []string `json:"properties_exclude"`         // Properties to exclude
	Replicate               bool     `json:"replicate"`                  // Full filesystem replication
	Encryption              bool     `json:"encryption"`                 // Enable encryption
	NamingSchema            []string `json:"naming_schema"`              // Naming schema for snapshots
	AlsoIncludeNamingSchema []string `json:"also_include_naming_schema"` // Additional naming schemas
	RetentionPolicy         string   `json:"retention_policy"`           // "SOURCE", "CUSTOM", or "NONE"
	Readonly                string   `json:"readonly"`                   // "SET", "REQUIRE", "IGNORE"
	AllowFromScratch        bool     `json:"allow_from_scratch"`         // Allow initial full send
}

// ReplicationJobState represents the state of a replication job.
//
//nolint:govet // fieldalignment: prefer readability over memory alignment for API response structs
type ReplicationJobState struct {
	ID          int                    `json:"id"`
	Method      string                 `json:"method"`
	State       string                 `json:"state"` // "WAITING", "RUNNING", "SUCCESS", "FAILED"
	Progress    map[string]interface{} `json:"progress"`
	Error       string                 `json:"error"`
	Result      interface{}            `json:"result"`
	TimeStarted *string                `json:"time_started"`
	TimeEnded   *string                `json:"time_finished"`
}

// RunOnetimeReplication runs a one-time replication task using zfs send/receive.
// This is the core method for creating detached snapshots - it performs a full
// data copy from source to destination without maintaining ZFS clone dependencies.
//
// The replication uses LOCAL transport for same-system operations (detached snapshots),
// which means the data is copied using zfs send | zfs receive within the same TrueNAS system.
//
// Returns the job ID which can be used to poll for completion status.
func (c *Client) RunOnetimeReplication(ctx context.Context, params ReplicationRunOnetimeParams) (int, error) {
	klog.V(4).Infof("Running one-time replication: %s -> %s", params.SourceDatasets, params.TargetDataset)

	var jobID int
	err := c.Call(ctx, "replication.run_onetime", []interface{}{params}, &jobID)
	if err != nil {
		return 0, fmt.Errorf("failed to start one-time replication: %w", err)
	}

	klog.V(4).Infof("Started one-time replication job: %d", jobID)
	return jobID, nil
}

// GetJobStatus retrieves the status of a job by its ID.
// Used to poll for completion of long-running operations like replication.
func (c *Client) GetJobStatus(ctx context.Context, jobID int) (*ReplicationJobState, error) {
	klog.V(5).Infof("Getting job status for job %d", jobID)

	var result ReplicationJobState
	err := c.Call(ctx, "core.get_jobs", []interface{}{
		[]interface{}{
			[]interface{}{"id", "=", jobID},
		},
	}, &[]ReplicationJobState{result})
	if err != nil {
		return nil, fmt.Errorf("failed to get job status: %w", err)
	}

	// Query returns an array, we need to get the first element
	var jobs []ReplicationJobState
	err = c.Call(ctx, "core.get_jobs", []interface{}{
		[]interface{}{
			[]interface{}{"id", "=", jobID},
		},
	}, &jobs)
	if err != nil {
		return nil, fmt.Errorf("failed to get job status: %w", err)
	}

	if len(jobs) == 0 {
		return nil, fmt.Errorf("job %d: %w", jobID, ErrJobNotFound)
	}

	return &jobs[0], nil
}

// WaitForJob waits for a job to complete, polling at the specified interval.
// Returns nil if the job succeeds, or an error if it fails or times out.
func (c *Client) WaitForJob(ctx context.Context, jobID int, pollInterval time.Duration) error {
	klog.V(4).Infof("Waiting for job %d to complete", jobID)

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("context canceled while waiting for job %d: %w", jobID, ctx.Err())
		case <-ticker.C:
			status, err := c.GetJobStatus(ctx, jobID)
			if err != nil {
				klog.Warningf("Failed to get job %d status: %v", jobID, err)
				continue
			}

			klog.V(5).Infof("Job %d state: %s", jobID, status.State)

			switch status.State {
			case "SUCCESS":
				klog.V(4).Infof("Job %d completed successfully", jobID)
				return nil
			case "FAILED":
				return fmt.Errorf("job %d: %w: %s", jobID, ErrJobFailed, status.Error)
			case "ABORTED":
				return fmt.Errorf("job %d: %w", jobID, ErrJobAborted)
			case "WAITING", "RUNNING":
				// Still in progress, continue polling
				continue
			default:
				klog.Warningf("Unknown job state: %s", status.State)
			}
		}
	}
}

// RunOnetimeReplicationAndWait runs a one-time replication and waits for completion.
// This is a convenience method that combines RunOnetimeReplication and WaitForJob.
func (c *Client) RunOnetimeReplicationAndWait(ctx context.Context, params ReplicationRunOnetimeParams, pollInterval time.Duration) error {
	jobID, err := c.RunOnetimeReplication(ctx, params)
	if err != nil {
		return err
	}

	return c.WaitForJob(ctx, jobID, pollInterval)
}

// FindDatasetsByProperty searches for datasets that have a specific ZFS user property value.
// This is useful for:
// - Finding all volumes managed by tns-csi (property: tns-csi:managed_by, value: tns-csi)
// - Finding a volume by its CSI volume name
// - Orphan detection and volume recovery
//
// The search is performed under the specified prefix (e.g., "tank/k8s").
// Returns a list of DatasetWithProperties that match the property filter.
func (c *Client) FindDatasetsByProperty(ctx context.Context, prefix, propertyName, propertyValue string) ([]DatasetWithProperties, error) {
	klog.V(4).Infof("Finding datasets with property %s=%s under prefix: %s", propertyName, propertyValue, prefix)

	// Query all datasets under the prefix with user properties included
	var result []DatasetWithProperties
	queryOpts := map[string]interface{}{
		"extra": map[string]interface{}{
			"properties": true,
		},
	}

	// Use "id" with "^" (starts with) filter to get all datasets under the prefix
	err := c.Call(ctx, "pool.dataset.query", []interface{}{
		[]interface{}{
			[]interface{}{"id", "^", prefix},
		},
		queryOpts,
	}, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to query datasets with properties: %w", err)
	}

	// Filter datasets that have the matching property value
	var matched []DatasetWithProperties
	for _, ds := range result {
		if ds.UserProperties == nil {
			continue
		}
		if prop, ok := ds.UserProperties[propertyName]; ok && prop.Value == propertyValue {
			matched = append(matched, ds)
		}
	}

	klog.V(4).Infof("Found %d datasets with property %s=%s (out of %d total)", len(matched), propertyName, propertyValue, len(result))
	return matched, nil
}

// FindManagedDatasets finds all datasets managed by tns-csi under the given prefix.
// This is a convenience method that searches for datasets with PropertyManagedBy=ManagedByValue.
// Useful for listing all CSI-provisioned volumes and orphan detection.
func (c *Client) FindManagedDatasets(ctx context.Context, prefix string) ([]DatasetWithProperties, error) {
	return c.FindDatasetsByProperty(ctx, prefix, PropertyManagedBy, ManagedByValue)
}

// FindDatasetByCSIVolumeName finds a dataset by its CSI volume name (PVC name).
// Returns the dataset if found, or nil if not found.
// This is useful for volume recovery when the controller restarts.
func (c *Client) FindDatasetByCSIVolumeName(ctx context.Context, prefix, csiVolumeName string) (*DatasetWithProperties, error) {
	datasets, err := c.FindDatasetsByProperty(ctx, prefix, PropertyCSIVolumeName, csiVolumeName)
	if err != nil {
		return nil, err
	}

	if len(datasets) == 0 {
		return nil, nil //nolint:nilnil // nil, nil indicates "not found" - callers check for nil dataset
	}

	if len(datasets) > 1 {
		klog.Warningf("Found multiple datasets with CSI volume name %s (returning first): %d datasets", csiVolumeName, len(datasets))
	}

	return &datasets[0], nil
}
