// Package tnsapi provides a WebSocket client for TrueNAS Scale API.
package tnsapi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/klog/v2"
)

// Client is a storage API client using JSON-RPC 2.0 over WebSocket.
type Client struct {
	url           string
	apiKey        string
	conn          *websocket.Conn
	mu            sync.Mutex
	reqID         uint64
	pending       map[string]chan *Response
	closeCh       chan struct{}
	closed        bool
	reconnecting  bool
	maxRetries    int
	retryInterval time.Duration
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
	ID     string          `json:"id"`
	Msg    string          `json:"msg,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error represents a storage API error.
type Error struct {
	// Storage API error format
	ErrorCode int    `json:"error"`
	ErrorName string `json:"errname"`
	Reason    string `json:"reason"`
	Type      string `json:"type"`

	// Fallback to JSON-RPC 2.0 format
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
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

	dialer := websocket.DefaultDialer
	dialer.HandshakeTimeout = 10 * time.Second

	// For wss:// connections, skip TLS verification (common for self-signed certs)
	// TODO: Add option to provide custom CA certificate
	if strings.HasPrefix(c.url, "wss://") {
		//nolint:gosec // TLS skip verify is intentional for self-signed TrueNAS certificates
		dialer.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
	}

	conn, _, err := dialer.Dial(c.url, nil)
	if err != nil {
		return fmt.Errorf("failed to dial: %w", err)
	}

	// Set up pong handler to respond to server pings
	// Note: TrueNAS does not send pings, so this is just for completeness
	conn.SetPongHandler(func(_ string) error {
		klog.V(6).Info("Received WebSocket pong")
		return nil
	})

	c.conn = conn
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
		return fmt.Errorf("authentication failed: Storage system rejected API key - verify key is correct and not revoked in System Settings -> API Keys")
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
	id := fmt.Sprintf("%d", atomic.AddUint64(&c.reqID, 1))

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
		return fmt.Errorf("authentication response ID mismatch: expected %s, got %s", id, resp.ID)
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
		return fmt.Errorf("authentication failed: Storage system rejected API key - verify key is correct and not revoked in System Settings -> API Keys")
	}

	klog.V(4).Info("Successfully authenticated with storage system (direct mode)")
	return nil
}

// Call makes a JSON-RPC 2.0 call.
func (c *Client) Call(ctx context.Context, method string, params []interface{}, result interface{}) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("client is closed")
	}

	// Generate request ID
	id := fmt.Sprintf("%d", atomic.AddUint64(&c.reqID, 1))

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
	c.mu.Unlock()

	// Wait for response
	select {
	case resp, ok := <-respCh:
		if !ok {
			// Channel was closed, connection error occurred
			return fmt.Errorf("connection closed while waiting for response")
		}
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
		return fmt.Errorf("client closed")
	}
}

// readLoop reads responses from WebSocket.
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

	for attempt := 1; attempt <= c.maxRetries; attempt++ {
		// Exponential backoff: 2^(attempt-1) * retryInterval, max 60s
		//nolint:gosec // Integer conversion is safe here - attempt is small positive int
		backoff := time.Duration(1<<uint(attempt-1)) * c.retryInterval
		if backoff > 60*time.Second {
			backoff = 60 * time.Second
		}

		klog.Infof("Reconnection attempt %d/%d (waiting %v)...", attempt, c.maxRetries, backoff)
		time.Sleep(backoff)

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
	Hosts        []string `json:"hosts,omitempty"`
	Networks     []string `json:"networks,omitempty"`
	MaprootUser  string   `json:"maproot_user,omitempty"`
	MaprootGroup string   `json:"maproot_group,omitempty"`
	Enabled      bool     `json:"enabled"`
}

// NFSShare represents an NFS share.
type NFSShare struct {
	ID      int      `json:"id"`
	Path    string   `json:"path"`
	Comment string   `json:"comment"`
	Hosts   []string `json:"hosts"`
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
	Type         string `json:"type"` // VOLUME
	Volsize      int64  `json:"volsize"`
	Volblocksize string `json:"volblocksize,omitempty"` // e.g., "16K"
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
	Name string `json:"name"`
}

// NVMeOFSubsystem represents an NVMe-oF subsystem.
type NVMeOFSubsystem struct {
	ID      int    `json:"id"`
	NQN     string `json:"subnqn"` // Storage system uses "subnqn" field name
	Serial  string `json:"serial"`
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
	SubsysID   int    `json:"subsys_id"`
	DevicePath string `json:"device_path"`    // Path to ZVOL, e.g., "/dev/zvol/pool/dataset"
	DeviceType string `json:"device_type"`    // Device type, e.g., "DEVICE"
	NSID       int    `json:"nsid,omitempty"` // Namespace ID (optional, auto-assigned if 0)
}

// NVMeOFNamespace represents an NVMe-oF namespace.
type NVMeOFNamespace struct {
	ID        int    `json:"id"`
	Subsystem int    `json:"subsystem"`
	Device    string `json:"device"`
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
func (c *Client) QueryNVMeOFSubsystem(ctx context.Context, nqn string) ([]NVMeOFSubsystem, error) {
	klog.V(4).Infof("Querying NVMe-oF subsystems for NQN: %s", nqn)

	var result []NVMeOFSubsystem
	err := c.Call(ctx, "nvmet.subsys.query", []interface{}{
		[]interface{}{
			[]interface{}{"nqn", "=", nqn},
		},
	}, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to query NVMe-oF subsystems: %w", err)
	}

	return result, nil
}
