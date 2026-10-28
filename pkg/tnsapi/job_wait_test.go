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

// A retried detached clone must find the replication an earlier attempt started into its
// target, so the query asks for one-time replication jobs and keeps only those whose
// target_dataset argument is that dataset, oldest first.
func TestFindReplicationJobsForTarget(t *testing.T) {
	var mu sync.Mutex
	var params []interface{}
	server := newMockWSServer()
	server.handler = func(conn *websocket.Conn) {
		serveMockRequests(conn, func(req Request) []Response {
			if req.Method == methodAuthLoginWithAPIKey {
				return []Response{{ID: req.ID, Result: json.RawMessage(`true`)}}
			}
			mu.Lock()
			params = req.Params
			mu.Unlock()
			return []Response{{ID: req.ID, Result: json.RawMessage(`[
				{"id": 9, "method": "replication.run_onetime", "state": "RUNNING", "arguments": [{"target_dataset": "tank/csi/pvc-new"}]},
				{"id": 5, "method": "replication.run_onetime", "state": "FAILED", "error": "boom", "arguments": [{"target_dataset": "tank/csi/pvc-new"}]},
				{"id": 7, "method": "replication.run_onetime", "state": "SUCCESS", "arguments": [{"target_dataset": "tank/csi/pvc-other"}]}
			]`)}}
		})
	}
	t.Cleanup(server.Close)
	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer cleanupClient(client)

	jobs, err := client.FindReplicationJobsForTarget(context.Background(), "tank/csi/pvc-new")
	if err != nil {
		t.Fatalf("FindReplicationJobsForTarget() error = %v", err)
	}
	if len(jobs) != 2 || jobs[0].ID != 5 || jobs[0].State != "FAILED" || jobs[1].ID != 9 || jobs[1].State != "RUNNING" {
		t.Fatalf("jobs = %+v, want jobs 5 (FAILED) then 9 (RUNNING)", jobs)
	}
	mu.Lock()
	defer mu.Unlock()
	got, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if want := `[[["method","=","replication.run_onetime"]]]`; string(got) != want {
		t.Errorf("core.get_jobs params = %s, want %s", got, want)
	}
}

// An interrupted resumable receive leaves receive_resume_token set on the dataset; an unset
// token reads "-" (or empty), and a query that did not return the property says nothing.
func TestDatasetReceiveIncomplete(t *testing.T) {
	tests := []struct {
		token map[string]interface{}
		want  bool
	}{
		{token: nil, want: false},
		{token: map[string]interface{}{"value": "-"}, want: false},
		{token: map[string]interface{}{"value": ""}, want: false},
		{token: map[string]interface{}{"value": nil}, want: false},
		{token: map[string]interface{}{"value": "1-e604ea4bf-e0-789c63a2"}, want: true},
	}
	for _, tt := range tests {
		ds := Dataset{ReceiveResumeToken: tt.token}
		if got := ds.ReceiveIncomplete(); got != tt.want {
			t.Errorf("ReceiveIncomplete() with token %v = %v, want %v", tt.token, got, tt.want)
		}
	}
}
