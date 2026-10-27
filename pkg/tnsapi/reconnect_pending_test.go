package tnsapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// When the connection dies with a create in flight, the caller must learn that the
// outcome is unknown right away, not after the first reconnect backoff (5s by
// default). CSI sidecars give a request 10-15s; spending 5s of it waiting for a
// verdict that was already known leaves little for the lookup the caller does next.
func TestInFlightCallerFailsBeforeReconnectBackoff(t *testing.T) {
	server := newMockWSServer()
	t.Cleanup(server.Close)
	server.handler = func(conn *websocket.Conn) {
		ctx := context.Background()
		for {
			var req Request
			if err := wsjson.Read(ctx, conn, &req); err != nil {
				return
			}
			if req.Method == methodAuthLoginWithAPIKey {
				if err := wsjson.Write(ctx, conn, Response{ID: req.ID, Result: json.RawMessage(`true`)}); err != nil {
					return
				}
				continue
			}
			// A create arrived: drop the connection without answering.
			_ = conn.Close(websocket.StatusGoingAway, "simulated outage")
			return
		}
	}

	const backoff = 2 * time.Second
	client, err := NewClient(server.URL(), "test-api-key", false, func(c *Client) { c.retryInterval = backoff })
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer cleanupClient(client)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	callErr := client.Call(ctx, "pool.dataset.create", []interface{}{map[string]string{"name": "tank/x"}}, nil)
	elapsed := time.Since(start)
	if !errors.Is(callErr, ErrUncertainOutcome) {
		t.Fatalf("Call() error = %v, want ErrUncertainOutcome", callErr)
	}
	if elapsed >= backoff {
		t.Fatalf("caller learned of the lost response after %v, i.e. only after the %v reconnect backoff", elapsed, backoff)
	}
}
