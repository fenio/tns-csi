package tnsapi

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestIsIdempotentMethod(t *testing.T) {
	idempotent := []string{
		"pool.dataset.query", "sharing.nfs.query", "iscsi.global.config", "core.get_jobs",
		"filesystem.stat", "filesystem.getacl", "filesystem.setacl",
		"pool.dataset.update", "sharing.smb.update", "pool.snapshot.update",
		"pool.dataset.delete", "nvmet.subsys.delete", "iscsi.targetextent.delete",
	}
	nonIdempotent := []string{
		"pool.dataset.create", "sharing.nfs.create", "iscsi.target.create", "nvmet.namespace.create",
		"pool.snapshot.create", "pool.snapshot.clone", "pool.dataset.promote",
		"replication.run_onetime", "service.control", "some.future.method",
	}
	for _, m := range idempotent {
		if !isIdempotentMethod(m) {
			t.Errorf("isIdempotentMethod(%q) = false, want true", m)
		}
	}
	for _, m := range nonIdempotent {
		if isIdempotentMethod(m) {
			t.Errorf("isIdempotentMethod(%q) = true, want false", m)
		}
	}
}

// dropPendingResponses simulates what reconnect() does to in-flight requests when the
// connection is lost: their response channels are closed without a response.
func dropPendingResponses(c *Client) {
	c.mu.Lock()
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
}

// lossyServer answers auth, counts requests per method, and never answers the first
// request of each method, so the test can drop it mid-flight. Later requests are
// answered with an empty result.
type lossyServer struct {
	counts   map[string]int
	received chan string
	mu       sync.Mutex
}

func newLossyServer(t *testing.T) (*mockWSServer, *lossyServer) {
	t.Helper()
	ls := &lossyServer{counts: map[string]int{}, received: make(chan string, 16)}
	server := newMockWSServer()
	server.handler = func(conn *websocket.Conn) {
		serveMockRequests(conn, func(req Request) []Response {
			if req.Method == methodAuthLoginWithAPIKey {
				return []Response{{ID: req.ID, Result: json.RawMessage(`true`)}}
			}
			ls.mu.Lock()
			ls.counts[req.Method]++
			n := ls.counts[req.Method]
			ls.mu.Unlock()
			ls.received <- req.Method
			if n == 1 {
				return nil // swallow: the test drops it as if the connection died
			}
			return []Response{{ID: req.ID, Result: json.RawMessage(`[]`)}}
		})
	}
	t.Cleanup(server.Close)
	return server, ls
}

func (ls *lossyServer) count(method string) int {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return ls.counts[method]
}

// A create whose connection drops after the request was sent may already have been
// applied by TrueNAS. Re-sending it can create a duplicate (dataset, share, target,
// namespace). The client must not retry it and must say the outcome is unknown, so the
// caller can look the resource up (CSI CreateVolume is idempotent by name).
func TestCallDoesNotResendNonIdempotentRequestAfterResponseLost(t *testing.T) {
	server, ls := newLossyServer(t)
	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer cleanupClient(client)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- client.Call(ctx, "pool.dataset.create", []interface{}{map[string]string{"name": "tank/x"}}, nil)
	}()

	<-ls.received
	dropPendingResponses(client)

	callErr := <-done
	if !errors.Is(callErr, ErrUncertainOutcome) {
		t.Errorf("Call() error = %v, want ErrUncertainOutcome", callErr)
	}
	if n := ls.count("pool.dataset.create"); n != 1 {
		t.Errorf("server received pool.dataset.create %d times, want exactly 1", n)
	}
}

// Queries are safe to repeat, so a lost response is still retried transparently.
func TestCallRetriesIdempotentRequestAfterResponseLost(t *testing.T) {
	server, ls := newLossyServer(t)
	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer cleanupClient(client)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Call(ctx, "pool.dataset.query", []interface{}{}, nil) }()

	<-ls.received
	dropPendingResponses(client)

	if callErr := <-done; callErr != nil {
		t.Errorf("Call() error = %v, want success after one transparent retry", callErr)
	}
	if n := ls.count("pool.dataset.query"); n != 2 {
		t.Errorf("server received pool.dataset.query %d times, want 2", n)
	}
}
