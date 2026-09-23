package tnsapi

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"

	"github.com/coder/websocket"
)

func TestPropertyQueryFilters(t *testing.T) {
	tests := []struct {
		name          string
		prefix        string
		propertyName  string
		propertyValue string
		want          []interface{}
	}{
		{
			name:          "value match, no prefix",
			propertyName:  PropertyCSIVolumeName,
			propertyValue: "pvc-123",
			want: []interface{}{
				[]interface{}{"user_properties.tns-csi:csi_volume_name.value", "=", "pvc-123"},
			},
		},
		{
			name:          "value match under prefix",
			prefix:        "tank/k8s",
			propertyName:  PropertyManagedBy,
			propertyValue: ManagedByValue,
			want: []interface{}{
				[]interface{}{"id", "^", "tank/k8s"},
				[]interface{}{"user_properties.tns-csi:managed_by.value", "=", "tns-csi"},
			},
		},
		{
			name:         "empty value means property exists",
			prefix:       "tank",
			propertyName: "democratic-csi:csi_share_volume_context",
			want: []interface{}{
				[]interface{}{"id", "^", "tank"},
				[]interface{}{"user_properties.democratic-csi:csi_share_volume_context", "!=", nil},
			},
		},
		{
			name:          "dotted property name falls back to prefix-only",
			prefix:        "tank",
			propertyName:  "org.example:prop",
			propertyValue: "x",
			want: []interface{}{
				[]interface{}{"id", "^", "tank"},
			},
		},
		{
			name:          "dotted property name without prefix sends no filters",
			propertyName:  "org.example:prop",
			propertyValue: "x",
			want:          []interface{}{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := propertyQueryFilters(tt.prefix, tt.propertyName, tt.propertyValue)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("propertyQueryFilters() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// TestFindDatasetsByPropertyFiltersServerSide verifies the property match is sent to
// TrueNAS as a query filter (so the server does not return every dataset on the
// system) and that results are still re-checked client-side.
func TestFindDatasetsByPropertyFiltersServerSide(t *testing.T) {
	server := newMockWSServer()
	defer server.Close()

	var (
		mu          sync.Mutex
		gotFilters  interface{}
		queryCalled bool
	)

	server.handler = func(conn *websocket.Conn) {
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
			if req.Method == "pool.dataset.query" {
				mu.Lock()
				queryCalled = true
				if len(req.Params) > 0 {
					gotFilters = req.Params[0]
				}
				mu.Unlock()
				// The second dataset does not match; a server that ignored the filter
				// would return it, and the client-side re-check must drop it.
				resp.Result = json.RawMessage(`[
					{"id": "tank/k8s/pvc-123", "name": "tank/k8s/pvc-123",
					 "user_properties": {"tns-csi:csi_volume_name": {"value": "pvc-123"}}},
					{"id": "tank/k8s/pvc-456", "name": "tank/k8s/pvc-456",
					 "user_properties": {"tns-csi:csi_volume_name": {"value": "pvc-456"}}}
				]`)
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

	client, err := NewClient(server.URL(), "test-api-key", false)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer cleanupClient(client)

	got, err := client.FindDatasetsByProperty(context.Background(), "", PropertyCSIVolumeName, "pvc-123")
	if err != nil {
		t.Fatalf("FindDatasetsByProperty() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !queryCalled {
		t.Fatal("pool.dataset.query was not called")
	}
	// Round-trip through JSON to compare against what went over the wire.
	wantFilters := []interface{}{
		[]interface{}{"user_properties.tns-csi:csi_volume_name.value", "=", "pvc-123"},
	}
	wantJSON, err := json.Marshal(wantFilters)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	gotJSON, err := json.Marshal(gotFilters)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Errorf("query filters = %s, want %s", gotJSON, wantJSON)
	}

	if len(got) != 1 || got[0].ID != "tank/k8s/pvc-123" {
		t.Errorf("FindDatasetsByProperty() returned %d datasets (%v), want only tank/k8s/pvc-123", len(got), got)
	}
}
