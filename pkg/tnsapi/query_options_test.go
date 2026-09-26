package tnsapi

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
)

// datasetTopLevelFields are pool.dataset.query fields returned regardless of
// extra.properties; every other Dataset field is a ZFS property that must be requested.
var datasetTopLevelFields = map[string]bool{"id": true, "name": true, "type": true, "mountpoint": true}

// TestDatasetDecodedPropertiesMatchStruct keeps datasetDecodedProperties in sync with the
// Dataset struct: a ZFS-property field added to Dataset but not requested would silently
// decode as empty.
func TestDatasetDecodedPropertiesMatchStruct(t *testing.T) {
	var want []string
	typ := reflect.TypeOf(Dataset{})
	for i := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" || datasetTopLevelFields[name] {
			continue
		}
		want = append(want, name)
	}
	got := append([]string(nil), datasetDecodedProperties...)
	sort.Strings(want)
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("datasetDecodedProperties = %v, want the Dataset struct's ZFS-property fields %v", got, want)
	}
}

// TestDatasetQueriesRequestOnlyDecodedFields verifies each dataset query sends options that
// stop TrueNAS from returning nested child trees and every ZFS property.
func TestDatasetQueriesRequestOnlyDecodedFields(t *testing.T) {
	tests := []struct {
		call           func(context.Context, *Client) error
		name           string
		wantProperties []string
		wantUserProps  bool
	}{
		{
			name: "Dataset",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.Dataset(ctx, "tank/k8s/pvc-1")
				return err
			},
			wantProperties: datasetDecodedProperties,
		},
		{
			name: "QueryAllDatasets",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.QueryAllDatasets(ctx, "tank/k8s")
				return err
			},
			wantProperties: datasetDecodedProperties,
		},
		{
			name: "GetDatasetWithProperties",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.GetDatasetWithProperties(ctx, "tank/k8s/pvc-1")
				return err
			},
			wantProperties: datasetDecodedProperties,
			wantUserProps:  true,
		},
		{
			name: "GetDatasetProperties",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.GetDatasetProperties(ctx, "tank/k8s/pvc-1", []string{PropertyManagedBy})
				return err
			},
			wantProperties: []string{},
			wantUserProps:  true,
		},
		{
			name: "GetAllDatasetProperties",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.GetAllDatasetProperties(ctx, "tank/k8s/pvc-1")
				return err
			},
			wantProperties: []string{},
			wantUserProps:  true,
		},
		{
			name: "FindDatasetsByProperty",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.FindDatasetsByProperty(ctx, "", PropertyManagedBy, ManagedByValue)
				return err
			},
			wantProperties: datasetDecodedProperties,
			wantUserProps:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newMockWSServer()
			defer server.Close()

			var (
				mu      sync.Mutex
				gotOpts json.RawMessage
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
						if len(req.Params) > 1 {
							raw, errMarshal := json.Marshal(req.Params[1])
							if errMarshal == nil {
								mu.Lock()
								gotOpts = raw
								mu.Unlock()
							}
						}
						resp.Result = json.RawMessage(`[{"id": "tank/k8s/pvc-1", "name": "tank/k8s/pvc-1"}]`)
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

			if err := tt.call(context.Background(), client); err != nil {
				t.Fatalf("call error = %v", err)
			}

			mu.Lock()
			defer mu.Unlock()
			if gotOpts == nil {
				t.Fatal("pool.dataset.query sent no query options")
			}
			var opts struct {
				Extra struct {
					Flat             *bool    `json:"flat"`
					RetrieveChildren *bool    `json:"retrieve_children"`
					UserProperties   *bool    `json:"user_properties"`
					Properties       []string `json:"properties"`
				} `json:"extra"`
			}
			if err := json.Unmarshal(gotOpts, &opts); err != nil {
				t.Fatalf("decode options %s: %v", gotOpts, err)
			}
			e := opts.Extra
			if e.Flat == nil || !*e.Flat {
				t.Errorf("flat = %v, want true (options %s)", e.Flat, gotOpts)
			}
			if e.RetrieveChildren == nil || *e.RetrieveChildren {
				t.Errorf("retrieve_children = %v, want false (options %s)", e.RetrieveChildren, gotOpts)
			}
			if e.UserProperties == nil || *e.UserProperties != tt.wantUserProps {
				t.Errorf("user_properties = %v, want %v (options %s)", e.UserProperties, tt.wantUserProps, gotOpts)
			}
			// A missing or null "properties" means "all ZFS properties" to the middleware.
			if e.Properties == nil || !reflect.DeepEqual(e.Properties, tt.wantProperties) {
				t.Errorf("properties = %#v, want %#v (options %s)", e.Properties, tt.wantProperties, gotOpts)
			}
		})
	}
}
