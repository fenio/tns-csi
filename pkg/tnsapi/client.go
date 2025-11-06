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

// NewClient creates a new storage API client.
func NewClient(url, apiKey string) (*Client, error) {
	klog.V(4).Infof("Creating new storage API client for %s", url)

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
	}

	// Connect to WebSocket
	if err := c.connect(); err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}

	// Start response handler
	go c.readLoop()

	// Start ping handler for connection health monitoring
	go c.pingLoop()

	// Authenticate
	if err := c.authenticate(); err != nil {
		c.Close()
		return nil, fmt.Errorf("failed to authenticate: %w", err)
	}

	return c, nil
}

// connect establishes WebSocket connection.
func (c *Client) connect() error {
	klog.V(4).Infof("Connecting to storage WebSocket at %s", c.url)

	dialer := &websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	// For wss:// connections, skip TLS verification (common for self-signed certs)
	// TODO: Add option to provide custom CA certificate
	if strings.HasPrefix(c.url, "wss://") {
		//nolint:gosec // TLS skip verify is intentional for self-signed TrueNAS certificates
		dialer.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true,
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
		klog.Errorf("Storage system rejected API key (length: %d, prefix: %s...)", len(c.apiKey), c.apiKey[:min(10, len(c.apiKey))])
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

	// Send request
	klog.V(5).Infof("Sending request: %+v", req)
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
		klog.Errorf("Storage system rejected API key (length: %d, prefix: %s...)", len(c.apiKey), c.apiKey[:min(10, len(c.apiKey))])
		return ErrAuthenticationRejected
	}

	klog.V(4).Info("Successfully authenticated with storage system (direct mode)")
	return nil
}

// Call makes a JSON-RPC 2.0 call.
func (c *Client) Call(ctx context.Context, method string, params []interface{}, result interface{}) error {
	// Start timing for metrics
	timer := metrics.NewWSMessageTimer(method)
	defer timer.Observe()

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

	// Send request
	klog.V(5).Infof("Sending request: %+v", req)
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
//
//nolint:gocognit // Complex WebSocket handling with reconnection - refactoring would risk stability
func (c *Client) readLoop() {
	defer func() {
		c.mu.Lock()
		c.closed = true
		for _, ch := range c.pending {
			close(ch)
		}
		c.pending = make(map[string]chan *Response)
		c.mu.Unlock()
		close(c.closeCh)
	}()

	for {
		// Set read deadline to detect dead connections
		// Since we send pings every 20s, use 40s timeout (2x ping interval)
		// This gets reset every time we receive a message (response to our requests)
		c.mu.Lock()
		if c.conn != nil {
			if err := c.conn.SetReadDeadline(time.Now().Add(40 * time.Second)); err != nil {
				klog.Warningf("Failed to set read deadline: %v", err)
			}
		}
		c.mu.Unlock()

		// Read raw message first for logging
		_, rawMsg, err := c.conn.ReadMessage()
		//nolint:nestif // Complex WebSocket error handling with reconnection - necessary for stability
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				klog.Errorf("WebSocket read error: %v", err)
			}

			// Check if client was intentionally closed
			c.mu.Lock()
			if c.closed {
				c.mu.Unlock()
				return
			}
			c.mu.Unlock()

			// Attempt to reconnect
			if c.reconnect() {
				klog.Info("Successfully reconnected to storage WebSocket")
				continue
			}

			// Reconnection failed after 5 attempts, reinitialize connection from scratch
			klog.Warning("Failed to reconnect after 5 attempts, will reinitialize connection in 30 seconds...")
			time.Sleep(30 * time.Second)

			klog.Info("Reinitializing WebSocket connection from scratch...")
			if err := c.connect(); err != nil {
				klog.Errorf("Connection reinitialization failed: %v, will retry", err)
				continue // Go back to top of loop, will retry reinitialization
			}

			// Use direct authentication since readLoop is still blocked here
			if err := c.authenticateDirect(); err != nil {
				klog.Errorf("Re-authentication after reinitialization failed: %v, will retry", err)
				continue // Go back to top of loop, will retry reinitialization
			}

			klog.Info("Successfully reinitialized WebSocket connection")
			continue
		}

		klog.V(5).Infof("Received raw response: %s", string(rawMsg))

		// Unmarshal response
		var resp Response
		if err := json.Unmarshal(rawMsg, &resp); err != nil {
			klog.Errorf("Failed to unmarshal response: %v", err)
			continue
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
		//nolint:gosec // Integer conversion is safe here - attempt is small positive int
		backoff := time.Duration(1<<uint(attempt-1)) * c.retryInterval
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

// Dataset API methods

// DatasetCreateParams represents parameters for dataset creation.
type DatasetCreateParams struct {
	Name string `json:"name"`
	Type string `json:"type"` // FILESYSTEM, VOLUME
}

// Dataset represents a ZFS dataset.
type Dataset struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	Type       string                 `json:"type"`
	Available  map[string]interface{} `json:"available,omitempty"`
	Used       map[string]interface{} `json:"used,omitempty"`
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
	klog.V(4).Infof("Deleting dataset: %s", datasetID)

	var result bool
	err := c.Call(ctx, "pool.dataset.delete", []interface{}{datasetID}, &result)
	if err != nil {
		return fmt.Errorf("failed to delete dataset: %w", err)
	}

	klog.V(4).Infof("Successfully deleted dataset: %s", datasetID)
	return nil
}

// GetDataset retrieves dataset information.
func (c *Client) GetDataset(ctx context.Context, datasetID string) (*Dataset, error) {
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
type ZvolCreateParams struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Volblocksize string `json:"volblocksize,omitempty"`
	Volsize      int64  `json:"volsize"`
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

// NVMeOFNamespace represents an NVMe-oF namespace.
type NVMeOFNamespace struct {
	Device    string `json:"device"`
	ID        int    `json:"id"`
	Subsystem int    `json:"subsystem"`
	NSID      int    `json:"nsid"`
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

// GetNVMeOFSubsystemByNQN retrieves a single NVMe-oF subsystem by NQN.
// Returns error if subsystem is not found or if multiple subsystems match.
func (c *Client) GetNVMeOFSubsystemByNQN(ctx context.Context, nqn string) (*NVMeOFSubsystem, error) {
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
func (c *Client) DeleteSnapshot(ctx context.Context, snapshotID string) error {
	klog.V(4).Infof("Deleting snapshot: %s", snapshotID)

	// TrueNAS API expects snapshot deletion parameters
	params := map[string]interface{}{
		"defer": false, // Don't defer deletion
	}

	var result bool
	err := c.Call(ctx, "zfs.snapshot.delete", []interface{}{snapshotID, params}, &result)
	if err != nil {
		return fmt.Errorf("failed to delete snapshot: %w", err)
	}

	klog.V(4).Infof("Successfully deleted snapshot: %s", snapshotID)
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

// QueryAllDatasets queries all datasets with optional prefix filter.
func (c *Client) QueryAllDatasets(ctx context.Context, prefix string) ([]Dataset, error) {
	klog.V(5).Infof("Querying all datasets with prefix: %s", prefix)

	var result []Dataset
	var filters []interface{}

	// If prefix is specified, filter by it
	if prefix != "" {
		filters = []interface{}{
			[]interface{}{"id", "^", prefix},
		}
	}

	err := c.Call(ctx, "pool.dataset.query", []interface{}{filters}, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to query datasets: %w", err)
	}

	klog.V(5).Infof("Found %d datasets", len(result))
	return result, nil
}

// QueryAllNFSShares queries all NFS shares with optional path prefix filter.
func (c *Client) QueryAllNFSShares(ctx context.Context, pathPrefix string) ([]NFSShare, error) {
	klog.V(5).Infof("Querying all NFS shares with path prefix: %s", pathPrefix)

	var result []NFSShare
	var filters []interface{}

	// If path prefix is specified, filter by it
	if pathPrefix != "" {
		filters = []interface{}{
			[]interface{}{"path", "^", pathPrefix},
		}
	}

	err := c.Call(ctx, "sharing.nfs.query", []interface{}{filters}, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to query NFS shares: %w", err)
	}

	klog.V(5).Infof("Found %d NFS shares", len(result))
	return result, nil
}

// QueryAllNVMeOFNamespaces queries all NVMe-oF namespaces.
func (c *Client) QueryAllNVMeOFNamespaces(ctx context.Context) ([]NVMeOFNamespace, error) {
	klog.V(5).Info("Querying all NVMe-oF namespaces")

	var result []NVMeOFNamespace
	err := c.Call(ctx, "nvmeof.namespace.query", []interface{}{[]interface{}{}}, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to query NVMe-oF namespaces: %w", err)
	}

	klog.V(5).Infof("Found %d NVMe-oF namespaces", len(result))
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
