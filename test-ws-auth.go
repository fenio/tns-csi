// Package main provides a test utility for TrueNAS WebSocket authentication.
package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type Request struct {
	ID      string        `json:"id"`
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params,omitempty"`
}

type Response struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

//nolint:gocognit // Test utility with multiple operations - complexity is acceptable
func main() {
	// Read credentials
	creds, err := os.ReadFile(".tns-credentials")
	if err != nil {
		fmt.Printf("ERROR: Failed to read .tns-credentials: %v\n", err)
		return
	}

	// Parse environment variable format
	var url, apiKey string
	lines := strings.Split(string(creds), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "TRUENAS_URL=") {
			url = strings.TrimPrefix(line, "TRUENAS_URL=")
		} else if strings.HasPrefix(line, "TRUENAS_API_KEY=") {
			apiKey = strings.TrimPrefix(line, "TRUENAS_API_KEY=")
		}
	}

	if url == "" || apiKey == "" {
		fmt.Printf("ERROR: Missing TRUENAS_URL or TRUENAS_API_KEY in .tns-credentials\n")
		return
	}
	fmt.Printf("Connecting to: %s\n", url)
	fmt.Printf("API Key length: %d\n\n", len(apiKey))

	// Connect with TLS skip verify
	dialer := websocket.DefaultDialer
	dialer.HandshakeTimeout = 10 * time.Second
	//nolint:gosec // TLS skip verify is intentional for testing with self-signed certificates
	dialer.TLSClientConfig = &tls.Config{
		InsecureSkipVerify: true,
	}

	conn, resp, err := dialer.Dial(url, nil)
	if err != nil {
		fmt.Printf("ERROR: Failed to dial: %v\n", err)
		if resp != nil {
			fmt.Printf("HTTP Status: %s\n", resp.Status)
		}
		return
	}
	defer func() {
		if err := conn.Close(); err != nil {
			fmt.Printf("Error closing connection: %v\n", err)
		}
	}()

	fmt.Printf("✓ WebSocket connected (HTTP %s)\n\n", resp.Status)

	// Set up message handler
	responseCh := make(chan []byte, 1)
	errorCh := make(chan error, 1)

	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				errorCh <- err
				return
			}
			responseCh <- msg
		}
	}()

	// Send authentication request - EXACT format from client.go:148-158
	authReq := Request{
		ID:      "1",
		JSONRPC: "2.0",
		Method:  "auth.login_with_api_key",
		Params:  []interface{}{apiKey},
	}

	authJSON, _ := json.MarshalIndent(authReq, "", "  ")
	fmt.Printf("Sending authentication request:\n%s\n\n", string(authJSON))

	if err := conn.WriteJSON(authReq); err != nil {
		fmt.Printf("ERROR: Failed to send auth request: %v\n", err)
		return
	}

	fmt.Printf("Waiting for response (10 second timeout)...\n\n")

	// Wait for response with timeout
	select {
	case msg := <-responseCh:
		fmt.Printf("✓ RECEIVED RESPONSE!\n")
		fmt.Printf("Raw response:\n%s\n\n", string(msg))

		var resp Response
		if err := json.Unmarshal(msg, &resp); err != nil {
			fmt.Printf("ERROR: Failed to parse response: %v\n", err)
			return
		}

		fmt.Printf("Parsed response:\n")
		fmt.Printf("  ID: %s\n", resp.ID)
		if resp.Result != nil {
			fmt.Printf("  Result: %s\n", string(resp.Result))
			fmt.Printf("\n✓ AUTHENTICATION SUCCESSFUL!\n\n")

			// Now test a real API call
			fmt.Printf("Testing API call: pool.dataset.query\n")
			queryReq := Request{
				ID:      "2",
				JSONRPC: "2.0",
				Method:  "pool.dataset.query",
				Params:  []interface{}{},
			}

			queryJSON, _ := json.MarshalIndent(queryReq, "", "  ")
			fmt.Printf("Sending query request:\n%s\n\n", string(queryJSON))

			if err := conn.WriteJSON(queryReq); err != nil {
				fmt.Printf("ERROR: Failed to send query request: %v\n", err)
				return
			}

			fmt.Printf("Waiting for query response (10 second timeout)...\n\n")

			// Wait for second response
			select {
			case msg := <-responseCh:
				fmt.Printf("✓ RECEIVED QUERY RESPONSE!\n")
				fmt.Printf("Raw response (first 500 chars):\n%s\n\n", string(msg[:minInt(500, len(msg))]))
				fmt.Printf("\n✓ WebSocket API is fully functional!\n")

			case err := <-errorCh:
				fmt.Printf("✗ WebSocket read error on query: %v\n", err)

			case <-time.After(10 * time.Second):
				fmt.Printf("✗ TIMEOUT on query response\n")
			}
		}
		if resp.Error != nil {
			fmt.Printf("  Error: %s\n", string(resp.Error))
		}

	case err := <-errorCh:
		fmt.Printf("✗ WebSocket read error: %v\n", err)

	case <-time.After(10 * time.Second):
		fmt.Printf("✗ TIMEOUT - No response received after 10 seconds\n")
		fmt.Printf("\nThis confirms the WebSocket accepts connections but does not respond to messages.\n")
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
