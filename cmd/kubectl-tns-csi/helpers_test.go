package main

import (
	"reflect"
	"testing"

	"github.com/fenio/tns-csi/pkg/dashboard"
)

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		name  string
		want  string
		bytes int64
	}{
		{
			name:  "zero bytes",
			bytes: 0,
			want:  "0B",
		},
		{
			name:  "below 1Ki",
			bytes: 1023,
			want:  "1023B",
		},
		{
			name:  "exactly 1Ki",
			bytes: 1024,
			want:  "1.0Ki",
		},
		{
			name:  "1.5Ki",
			bytes: 1536,
			want:  "1.5Ki",
		},
		{
			name:  "exactly 1Mi",
			bytes: 1048576,
			want:  "1.0Mi",
		},
		{
			name:  "exactly 1Gi",
			bytes: 1073741824,
			want:  "1.0Gi",
		},
		{
			name:  "exactly 1Ti",
			bytes: 1099511627776,
			want:  "1.0Ti",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dashboard.FormatBytes(tt.bytes)
			if got != tt.want {
				t.Errorf("dashboard.FormatBytes(%d) = %q, want %q", tt.bytes, got, tt.want)
			}
		})
	}
}

func TestBuildNamespaceSearchOrder(t *testing.T) {
	tests := []struct {
		name             string
		contextNamespace string
		want             []string
	}{
		{
			name:             "empty context namespace",
			contextNamespace: "",
			want:             []string{"tns-csi", "kube-system"},
		},
		{
			name:             "default context namespace",
			contextNamespace: "default",
			want:             []string{"default", "tns-csi", "kube-system"},
		},
		{
			name:             "preferred context namespace is deduplicated",
			contextNamespace: "tns-csi",
			want:             []string{"tns-csi", "kube-system"},
		},
		{
			name:             "legacy context namespace is prioritized",
			contextNamespace: "kube-system",
			want:             []string{"kube-system", "tns-csi"},
		},
		{
			name:             "custom namespace",
			contextNamespace: "my-namespace",
			want:             []string{"my-namespace", "tns-csi", "kube-system"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildNamespaceSearchOrder(tt.contextNamespace)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildNamespaceSearchOrder(%q) = %v, want %v", tt.contextNamespace, got, tt.want)
			}
		})
	}
}

func TestExtractConfigFromSecretData(t *testing.T) {
	tests := []struct {
		name       string
		data       map[string][]byte
		wantURL    string
		wantAPIKey string
		wantNil    bool
	}{
		{
			name: "complete data with standard keys",
			data: map[string][]byte{
				"url":     []byte("wss://truenas:443/api/current"),
				"api-key": []byte("my-secret-key"),
			},
			wantNil:    false,
			wantURL:    "wss://truenas:443/api/current",
			wantAPIKey: "my-secret-key",
		},
		{
			name: "alternative key names",
			data: map[string][]byte{
				"truenas-url": []byte("wss://alt-host:443/api/current"),
				"apiKey":      []byte("alt-key"),
			},
			wantNil:    false,
			wantURL:    "wss://alt-host:443/api/current",
			wantAPIKey: "alt-key",
		},
		{
			name:    "empty map returns nil",
			data:    map[string][]byte{},
			wantNil: true,
		},
		{
			name: "partial data with only URL",
			data: map[string][]byte{
				"url": []byte("wss://truenas:443/api/current"),
			},
			wantNil:    false,
			wantURL:    "wss://truenas:443/api/current",
			wantAPIKey: "",
		},
		{
			name: "nil values in map are skipped",
			data: map[string][]byte{
				"url":     nil,
				"api-key": []byte("key-only"),
			},
			wantNil:    false,
			wantURL:    "",
			wantAPIKey: "key-only",
		},
		{
			name: "empty byte values are skipped",
			data: map[string][]byte{
				"url":     []byte(""),
				"api-key": []byte("key-only"),
			},
			wantNil:    false,
			wantURL:    "",
			wantAPIKey: "key-only",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractConfigFromSecretData(tt.data)
			if tt.wantNil {
				if got != nil {
					t.Errorf("extractConfigFromSecretData() = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("extractConfigFromSecretData() = nil, want non-nil")
			}
			if got.URL != tt.wantURL {
				t.Errorf("URL = %q, want %q", got.URL, tt.wantURL)
			}
			if got.APIKey != tt.wantAPIKey {
				t.Errorf("APIKey = %q, want %q", got.APIKey, tt.wantAPIKey)
			}
		})
	}
}
