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

// serviceControlServer answers auth and records every service.control verb. The
// first RELOAD is handled by reloadResponse; RESTART always succeeds.
type serviceControlServer struct {
	received       chan string
	reloadResponse func(req Request) []Response
	verbs          []string
	mu             sync.Mutex
}

func newServiceControlServer(t *testing.T, reloadResponse func(req Request) []Response) (*mockWSServer, *serviceControlServer) {
	t.Helper()
	sc := &serviceControlServer{received: make(chan string, 16), reloadResponse: reloadResponse}
	server := newMockWSServer()
	server.handler = func(conn *websocket.Conn) {
		serveMockRequests(conn, func(req Request) []Response {
			if req.Method == methodAuthLoginWithAPIKey {
				return []Response{{ID: req.ID, Result: json.RawMessage(`true`)}}
			}
			if req.Method != "service.control" || len(req.Params) == 0 {
				return []Response{{ID: req.ID, Result: json.RawMessage(`null`)}}
			}
			verb, _ := req.Params[0].(string)
			sc.mu.Lock()
			sc.verbs = append(sc.verbs, verb)
			sc.mu.Unlock()
			sc.received <- verb
			if verb == "RELOAD" {
				return sc.reloadResponse(req)
			}
			return []Response{{ID: req.ID, Result: json.RawMessage(`42`)}}
		})
	}
	t.Cleanup(server.Close)
	return server, sc
}

func (sc *serviceControlServer) seen() []string {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return append([]string(nil), sc.verbs...)
}

// A RELOAD whose response is lost may already have run. Escalating to RESTART drops
// every active iSCSI session on the TrueNAS, so the uncertain outcome must be reported,
// not papered over with a restart.
func TestReloadISCSIServiceDoesNotRestartAfterLostReloadResponse(t *testing.T) {
	server, sc := newServiceControlServer(t, func(Request) []Response { return nil })
	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer cleanupClient(client)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.ReloadISCSIService(ctx) }()

	<-sc.received
	dropPendingResponses(client)

	reloadErr := <-done
	if !errors.Is(reloadErr, ErrUncertainOutcome) {
		t.Errorf("ReloadISCSIService() error = %v, want ErrUncertainOutcome", reloadErr)
	}
	time.Sleep(50 * time.Millisecond) // give a wrong RESTART time to show up
	if verbs := sc.seen(); len(verbs) != 1 || verbs[0] != "RELOAD" {
		t.Errorf("server saw %v, want [RELOAD] only", verbs)
	}
}

// A definite API error on RELOAD (the verb is not supported by this service or
// release) still falls back to RESTART.
func TestReloadISCSIServiceFallsBackToRestartOnAPIError(t *testing.T) {
	server, sc := newServiceControlServer(t, func(req Request) []Response {
		return []Response{{ID: req.ID, Error: &Error{Code: 22, Message: "Invalid verb RELOAD", ErrorName: "EINVAL"}}}
	})
	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer cleanupClient(client)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if reloadErr := client.ReloadISCSIService(ctx); reloadErr != nil {
		t.Fatalf("ReloadISCSIService() error = %v, want fallback success", reloadErr)
	}
	if verbs := sc.seen(); len(verbs) != 2 || verbs[0] != "RELOAD" || verbs[1] != "RESTART" {
		t.Errorf("server saw %v, want [RELOAD RESTART]", verbs)
	}
}

// A canceled or expired request context is not a reason to restart the service.
func TestReloadSMBServiceDoesNotRestartAfterContextExpiry(t *testing.T) {
	server, sc := newServiceControlServer(t, func(Request) []Response { return nil })
	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer cleanupClient(client)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	reloadErr := client.ReloadSMBService(ctx)
	if !errors.Is(reloadErr, context.DeadlineExceeded) {
		t.Errorf("ReloadSMBService() error = %v, want context deadline", reloadErr)
	}
	time.Sleep(50 * time.Millisecond)
	if verbs := sc.seen(); len(verbs) != 1 || verbs[0] != "RELOAD" {
		t.Errorf("server saw %v, want [RELOAD] only", verbs)
	}
}
