package tnsapi

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// loginRecorder answers login requests and records what the client sent.
type loginRecorder struct {
	responseType string // auth.login_ex result; "" means SUCCESS
	methods      []string
	params       [][]interface{}
	mu           sync.Mutex
	legacyResult bool // auth.login_with_api_key result
}

func (r *loginRecorder) handle(req Request) []Response {
	resp := Response{ID: req.ID, Result: json.RawMessage(`true`)}
	if !isLoginMethod(req.Method) {
		return []Response{resp}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.methods = append(r.methods, req.Method)
	r.params = append(r.params, req.Params)

	if req.Method == methodAuthLoginEx {
		responseType := r.responseType
		if responseType == "" {
			responseType = loginResponseSuccess
		}
		resp.Result = json.RawMessage(`{"response_type": "` + responseType + `"}`)
		return []Response{resp}
	}
	resp.Result = json.RawMessage(strconv.FormatBool(r.legacyResult))
	return []Response{resp}
}

func (r *loginRecorder) calls() (methods []string, params [][]interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.methods...), append([][]interface{}(nil), r.params...)
}

func newLoginServer(t *testing.T, rec *loginRecorder) *mockWSServer {
	t.Helper()
	server := newMockWSServer()
	server.handler = func(conn *websocket.Conn) { serveMockRequests(conn, rec.handle) }
	t.Cleanup(server.Close)
	return server
}

func TestNewClientLogsInWithLoginExWhenUsernameSet(t *testing.T) {
	rec := &loginRecorder{}
	server := newLoginServer(t, rec)

	client, err := NewClient(server.URL(), "test-api-key", false, WithAPIUsername(" csi-user\n"))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer cleanupClient(client)

	methods, params := rec.calls()
	if !reflect.DeepEqual(methods, []string{methodAuthLoginEx}) {
		t.Fatalf("login methods = %v, want only %s", methods, methodAuthLoginEx)
	}
	want := []interface{}{map[string]interface{}{
		"mechanism": "API_KEY_PLAIN",
		"username":  "csi-user",
		"api_key":   "test-api-key",
	}}
	if !reflect.DeepEqual(params[0], want) {
		t.Errorf("auth.login_ex params = %#v, want %#v", params[0], want)
	}
}

func TestNewClientKeepsLegacyLoginWithoutUsername(t *testing.T) {
	rec := &loginRecorder{legacyResult: true}
	server := newLoginServer(t, rec)

	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer cleanupClient(client)

	methods, params := rec.calls()
	if !reflect.DeepEqual(methods, []string{methodAuthLoginWithAPIKey}) {
		t.Fatalf("login methods = %v, want only %s", methods, methodAuthLoginWithAPIKey)
	}
	if !reflect.DeepEqual(params[0], []interface{}{"test-api-key"}) {
		t.Errorf("auth.login_with_api_key params = %#v, want the bare key", params[0])
	}
}

func TestLoginExRejectionIsPermanent(t *testing.T) {
	for _, responseType := range []string{"AUTH_ERR", "EXPIRED"} {
		t.Run(responseType, func(t *testing.T) {
			rec := &loginRecorder{responseType: responseType}
			server := newLoginServer(t, rec)

			client, err := NewClient(server.URL(), "test-api-key", false,
				WithAPIUsername("someone-else"),
				WithConnectRetryDelays(0, 10*time.Millisecond, 10*time.Millisecond))
			if err == nil {
				cleanupClient(client)
				t.Fatal("NewClient() succeeded, want a rejected login")
			}
			if !errors.Is(err, ErrAuthenticationRejected) {
				t.Errorf("NewClient() error = %v, want ErrAuthenticationRejected", err)
			}
			if methods, _ := rec.calls(); len(methods) != 1 {
				t.Errorf("login attempts = %d, want 1 (a rejected key is not retried)", len(methods))
			}
		})
	}
}

// REDIRECT (an HA standby) and OTP_REQUIRED say nothing about the key, so they must not be
// reported as a rejected key.
func TestLoginExNonVerdictIsNotRejection(t *testing.T) {
	for _, responseType := range []string{"REDIRECT", "OTP_REQUIRED"} {
		t.Run(responseType, func(t *testing.T) {
			rec := &loginRecorder{responseType: responseType}
			server := newLoginServer(t, rec)

			client, err := NewClient(server.URL(), "test-api-key", false,
				WithAPIUsername("someone-else"),
				WithConnectRetryDelays(0, 10*time.Millisecond, 10*time.Millisecond))
			if err == nil {
				cleanupClient(client)
				t.Fatal("NewClient() succeeded, want a failed login")
			}
			if errors.Is(err, ErrAuthenticationRejected) {
				t.Errorf("NewClient() error = %v, must not be ErrAuthenticationRejected", err)
			}
		})
	}
}

func TestAuthenticateDirectUsesLoginEx(t *testing.T) {
	tests := []struct {
		wantErr      error
		name         string
		responseType string
	}{
		{name: "success", responseType: loginResponseSuccess},
		{name: "rejected", responseType: "AUTH_ERR", wantErr: ErrAuthenticationRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &loginRecorder{responseType: tt.responseType}
			server := newLoginServer(t, rec)

			// authenticateDirect runs during reconnection, before readLoop is serving responses.
			client := &Client{
				url:      server.URL(),
				apiKey:   "test-api-key",
				username: "csi-user",
				pending:  make(map[string]chan *Response),
				closeCh:  make(chan struct{}),
				authGate: make(chan struct{}, 1),
			}
			if err := client.connect(); err != nil {
				t.Fatalf("connect() error = %v", err)
			}
			defer cleanupClient(client)

			err := client.authenticateDirect()
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Errorf("authenticateDirect() error = %v, want %v", err, tt.wantErr)
			}
			if methods, _ := rec.calls(); !reflect.DeepEqual(methods, []string{methodAuthLoginEx}) {
				t.Errorf("login methods = %v, want only %s", methods, methodAuthLoginEx)
			}
		})
	}
}

func TestLoginExDoesNotRecurseOnSessionAuthLoss(t *testing.T) {
	server := newMockWSServer()
	defer server.Close()

	var mu sync.Mutex
	logins := 0
	server.handler = func(conn *websocket.Conn) {
		serveMockRequests(conn, func(req Request) []Response {
			resp := Response{ID: req.ID}
			mu.Lock()
			defer mu.Unlock()
			if req.Method == methodAuthLoginEx {
				logins++
				if logins == 1 {
					resp.Result = json.RawMessage(`{"response_type": "SUCCESS"}`)
					return []Response{resp}
				}
			}
			resp.Error = enotauthenticatedError()
			return []Response{resp}
		})
	}

	client, err := NewClient(server.URL(), "test-api-key", false, WithAPIUsername("csi-user"))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer cleanupClient(client)

	if err := client.reauthenticate(context.Background(), client.currentReauthEpoch()); err == nil {
		t.Fatal("reauthenticate() succeeded, want the login's ENOTAUTHENTICATED error")
	}
	mu.Lock()
	defer mu.Unlock()
	if logins != 2 {
		t.Errorf("auth.login_ex calls = %d, want 2 (initial login plus one re-login, no recursion)", logins)
	}
}
