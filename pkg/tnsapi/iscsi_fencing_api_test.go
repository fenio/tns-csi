package tnsapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// The fencing calls send the IDs and fields TrueNAS expects and decode its replies.
func TestISCSIFencingAPICalls(t *testing.T) {
	sent := make(chan Request, 8)
	server := newMockWSServer()
	server.handler = func(conn *websocket.Conn) {
		serveMockRequests(conn, func(req Request) []Response {
			if req.Method == methodAuthLoginWithAPIKey {
				return []Response{{ID: req.ID, Result: json.RawMessage(`true`)}}
			}
			sent <- req
			if req.Method == "iscsi.target.update" {
				return []Response{{ID: req.ID, Result: json.RawMessage(`{"id":7,"name":"pvc-1","groups":[{"portal":1,"initiator":3}]}`)}}
			}
			return []Response{{ID: req.ID, Result: json.RawMessage(`{"id":3,"comment":"c","initiators":["iqn.a"]}`)}}
		})
	}
	t.Cleanup(server.Close)
	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer cleanupClient(client)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	params := func(want string) string {
		t.Helper()
		req := <-sent
		if req.Method != want {
			t.Fatalf("method = %s, want %s", req.Method, want)
		}
		b, marshalErr := json.Marshal(req.Params)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return string(b)
	}

	g, err := client.CreateISCSIInitiator(ctx, []string{"iqn.a"}, "c")
	if err != nil || g.ID != 3 {
		t.Fatalf("CreateISCSIInitiator() = %+v, %v", g, err)
	}
	if got := params("iscsi.initiator.create"); got != `[{"comment":"c","initiators":["iqn.a"]}]` {
		t.Errorf("create params = %s", got)
	}

	if _, err = client.UpdateISCSIInitiator(ctx, 3, []string{"iqn.b"}); err != nil {
		t.Fatalf("UpdateISCSIInitiator() error = %v", err)
	}
	if got := params("iscsi.initiator.update"); got != `[3,{"initiators":["iqn.b"]}]` {
		t.Errorf("initiator update params = %s", got)
	}

	target, err := client.UpdateISCSITarget(ctx, 7, []ISCSITargetGroup{{Portal: 1, Initiator: 3}})
	if err != nil || len(target.Groups) != 1 || target.Groups[0].Initiator != 3 {
		t.Fatalf("UpdateISCSITarget() = %+v, %v", target, err)
	}
	if got := params("iscsi.target.update"); got != `[7,{"groups":[{"initiator":3,"portal":1}]}]` {
		t.Errorf("target update params = %s", got)
	}
}
