package tnsapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// bigResponseHandler authenticates and answers every other call with a JSON string
// result of payloadSize bytes.
func bigResponseHandler(payloadSize int) func(*websocket.Conn) {
	return func(conn *websocket.Conn) {
		ctx := context.Background()
		for {
			_, message, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var req Request
			if json.Unmarshal(message, &req) != nil {
				continue
			}
			resp := Response{ID: req.ID, Result: json.RawMessage(`true`)}
			if req.Method != methodAuthLoginWithAPIKey {
				result, errMarshal := json.Marshal(strings.Repeat("x", payloadSize))
				if errMarshal != nil {
					return
				}
				resp.Result = result
			}
			respBytes, err := json.Marshal(resp)
			if err != nil {
				return
			}
			if conn.Write(ctx, websocket.MessageText, respBytes) != nil {
				return
			}
		}
	}
}

func TestWithReadLimit(t *testing.T) {
	tests := []struct {
		name string
		opts []ClientOption
		want int64
	}{
		{name: "default", want: DefaultReadLimit},
		{name: "explicit", opts: []ClientOption{WithReadLimit(64 << 20)}, want: 64 << 20},
		{name: "zero keeps default", opts: []ClientOption{WithReadLimit(0)}, want: DefaultReadLimit},
		{name: "negative keeps default", opts: []ClientOption{WithReadLimit(-1)}, want: DefaultReadLimit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newMockWSServer()
			defer server.Close()

			client, err := NewClient(server.URL(), "test-api-key", false, tt.opts...)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			defer cleanupClient(client)

			if client.readLimit != tt.want {
				t.Errorf("readLimit = %d, want %d", client.readLimit, tt.want)
			}
		})
	}
}

// TestReadLimitEnforced verifies the configured limit is what the connection enforces:
// a response over the limit fails the call, the same response under it succeeds.
func TestReadLimitEnforced(t *testing.T) {
	const payloadSize = 64 * 1024

	t.Run("response over limit fails", func(t *testing.T) {
		server := newMockWSServer()
		server.handler = bigResponseHandler(payloadSize)
		defer server.Close()

		client, err := NewClient(server.URL(), "test-api-key", false, WithReadLimit(16*1024))
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		defer cleanupClient(client)

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var result string
		if err := client.Call(ctx, "test.method", []interface{}{}, &result); err == nil {
			t.Fatal("Call() succeeded, want error for response over the read limit")
		}
	})

	t.Run("response under limit succeeds", func(t *testing.T) {
		server := newMockWSServer()
		server.handler = bigResponseHandler(payloadSize)
		defer server.Close()

		client, err := NewClient(server.URL(), "test-api-key", false, WithReadLimit(1<<20))
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		defer cleanupClient(client)

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var result string
		if err := client.Call(ctx, "test.method", []interface{}{}, &result); err != nil {
			t.Fatalf("Call() error = %v", err)
		}
		if len(result) != payloadSize {
			t.Errorf("result length = %d, want %d", len(result), payloadSize)
		}
	})
}
