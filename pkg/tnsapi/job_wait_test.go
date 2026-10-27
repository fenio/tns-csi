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

// jobServer answers auth and serves core.get_jobs from a canned result, counting calls.
func newJobServer(t *testing.T, jobsResult string) (server *mockWSServer, calls func() int) {
	t.Helper()
	var mu sync.Mutex
	count := 0
	server = newMockWSServer()
	server.handler = func(conn *websocket.Conn) {
		serveMockRequests(conn, func(req Request) []Response {
			if req.Method == methodAuthLoginWithAPIKey {
				return []Response{{ID: req.ID, Result: json.RawMessage(`true`)}}
			}
			if req.Method == "core.get_jobs" {
				mu.Lock()
				count++
				mu.Unlock()
				return []Response{{ID: req.ID, Result: json.RawMessage(jobsResult)}}
			}
			return []Response{{ID: req.ID, Result: json.RawMessage(`null`)}}
		})
	}
	t.Cleanup(server.Close)
	return server, func() int { mu.Lock(); defer mu.Unlock(); return count }
}

// Each poll must cost one core.get_jobs round trip. Replication waits poll every
// second for as long as the copy takes.
func TestGetJobStatusSendsOneQuery(t *testing.T) {
	server, calls := newJobServer(t, `[{"id": 7, "state": "RUNNING"}]`)
	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer cleanupClient(client)

	state, err := client.GetJobStatus(context.Background(), 7)
	if err != nil || state == nil || state.State != "RUNNING" {
		t.Fatalf("GetJobStatus() = %+v, %v; want RUNNING", state, err)
	}
	if n := calls(); n != 1 {
		t.Errorf("core.get_jobs sent %d times for one status check, want 1", n)
	}
}

// core.get_jobs only knows jobs middlewared still holds in memory. After a middlewared
// restart the job is gone for good, so polling until the caller's deadline (hours for
// a replication) only hides the failure. Report it right away.
func TestWaitForJobReturnsWhenJobIsGone(t *testing.T) {
	server, calls := newJobServer(t, `[]`)
	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer cleanupClient(client)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	waitErr := client.WaitForJob(ctx, 7, 20*time.Millisecond)
	if !errors.Is(waitErr, ErrJobNotFound) {
		t.Fatalf("WaitForJob() error = %v, want ErrJobNotFound", waitErr)
	}
	if time.Since(start) > time.Second {
		t.Errorf("WaitForJob() took %v before giving up on a missing job", time.Since(start))
	}
	if n := calls(); n != 1 {
		t.Errorf("core.get_jobs sent %d times, want 1", n)
	}
}

// Once the client is closed nothing can answer; the wait must not spin on the ticker.
func TestWaitForJobReturnsAfterClientClose(t *testing.T) {
	server, _ := newJobServer(t, `[{"id": 7, "state": "RUNNING"}]`)
	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	waitErr := client.WaitForJob(ctx, 7, 20*time.Millisecond)
	if !errors.Is(waitErr, ErrClientClosed) {
		t.Fatalf("WaitForJob() error = %v, want ErrClientClosed", waitErr)
	}
	if time.Since(start) > time.Second {
		t.Errorf("WaitForJob() took %v after Close()", time.Since(start))
	}
}
