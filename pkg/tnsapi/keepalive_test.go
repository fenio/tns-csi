package tnsapi

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// A TrueNAS that stays TCP-reachable but stops answering (wedged middlewared, or a
// flow a firewall silently dropped) never produces a read error, so readLoop would
// block forever and every RPC would time out until the pod restarted. Missed pongs
// have to force the reconnect.
func TestMissedPongsForceReconnect(t *testing.T) {
	var accepts atomic.Int32
	silent := make(chan struct{})
	server := newMockWSServer()
	t.Cleanup(server.Close)             // runs last: the silent handler must have returned
	t.Cleanup(func() { close(silent) }) // runs first (LIFO)
	server.handler = func(conn *websocket.Conn) {
		n := accepts.Add(1)
		ctx := context.Background()
		// Answer the login on every connection.
		var req Request
		if err := wsjson.Read(ctx, conn, &req); err != nil {
			return
		}
		if err := wsjson.Write(ctx, conn, Response{ID: req.ID, Result: json.RawMessage(`true`)}); err != nil {
			return
		}
		if n == 1 {
			// First connection: go silent. Not reading means no pong is ever sent.
			<-silent
			return
		}
		serveMockRequests(conn, func(req Request) []Response {
			return []Response{{ID: req.ID, Result: json.RawMessage(`true`)}}
		})
	}

	// Options run before any goroutine starts, so the reconnect backoff is set race-free.
	shortBackoff := func(c *Client) { c.retryInterval = 20 * time.Millisecond }
	client, err := NewClient(server.URL(), "test-api-key", false, WithKeepalive(30*time.Millisecond, 60*time.Millisecond), shortBackoff)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer cleanupClient(client)

	deadline := time.Now().Add(5 * time.Second)
	for accepts.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if accepts.Load() < 2 {
		t.Fatalf("client never reconnected after missed pongs (accepts=%d)", accepts.Load())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var result bool
	if callErr := client.Call(ctx, "test.method", nil, &result); callErr != nil || !result {
		t.Fatalf("Call() after keepalive reconnect = %v, %v; want true, nil", result, callErr)
	}
}
