package driver

import (
	"context"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGetPluginInfo(t *testing.T) {
	tests := []struct {
		name       string
		driverName string
		version    string
		wantErr    bool
		wantCode   codes.Code
	}{
		{
			name:       "Valid driver info",
			driverName: "tns.csi.io",
			version:    "v0.1.0",
			wantErr:    false,
		},
		{
			name:       "Missing driver name",
			driverName: "",
			version:    "v0.1.0",
			wantErr:    true,
			wantCode:   codes.Unavailable,
		},
		{
			name:       "Missing version",
			driverName: "tns.csi.io",
			version:    "",
			wantErr:    true,
			wantCode:   codes.Unavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := NewIdentityService(tt.driverName, tt.version)
			resp, err := service.GetPluginInfo(context.Background(), &csi.GetPluginInfoRequest{})

			if tt.wantErr {
				if err == nil {
					t.Error("GetPluginInfo() expected error, got nil")
					return
				}
				st, ok := status.FromError(err)
				if !ok {
					t.Errorf("GetPluginInfo() error is not a gRPC status: %v", err)
					return
				}
				if st.Code() != tt.wantCode {
					t.Errorf("GetPluginInfo() error code = %v, want %v", st.Code(), tt.wantCode)
				}
				return
			}

			if err != nil {
				t.Errorf("GetPluginInfo() unexpected error = %v", err)
				return
			}

			// Use require pattern - fail immediately if nil.
			requireNotNil(t, resp, "GetPluginInfo() returned nil response")

			if resp.Name != tt.driverName {
				t.Errorf("GetPluginInfo() Name = %v, want %v", resp.Name, tt.driverName)
			}

			if resp.VendorVersion != tt.version {
				t.Errorf("GetPluginInfo() VendorVersion = %v, want %v", resp.VendorVersion, tt.version)
			}
		})
	}
}

func TestGetPluginCapabilities(t *testing.T) {
	service := NewIdentityService("tns.csi.io", "v0.1.0")

	resp, err := service.GetPluginCapabilities(context.Background(), &csi.GetPluginCapabilitiesRequest{})
	if err != nil {
		t.Fatalf("GetPluginCapabilities() error = %v", err)
	}

	// Use require pattern - fail immediately if nil.
	requireNotNil(t, resp, "GetPluginCapabilities() returned nil response")

	if len(resp.Capabilities) == 0 {
		t.Error("GetPluginCapabilities() returned no capabilities")
	}

	// Verify expected capabilities.
	hasControllerService := false

	for _, cap := range resp.Capabilities {
		if svc := cap.GetService(); svc != nil {
			if svc.Type == csi.PluginCapability_Service_CONTROLLER_SERVICE {
				hasControllerService = true
			}
		}
	}

	if !hasControllerService {
		t.Error("GetPluginCapabilities() missing CONTROLLER_SERVICE capability")
	}

	// Note: VOLUME_ACCESSIBILITY_CONSTRAINTS intentionally removed for csi-provisioner v5+ compatibility.
}

func TestProbe(t *testing.T) {
	service := NewIdentityService("tns.csi.io", "v0.1.0")

	resp, err := service.Probe(context.Background(), &csi.ProbeRequest{})
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}

	// Use require pattern - fail immediately if nil.
	requireNotNil(t, resp, "Probe() returned nil response")
	requireNotNil(t, resp.Ready, "Probe() Ready field is nil")

	if !resp.Ready.Value {
		t.Error("Probe() Ready = false, want true")
	}
}

// requireNotNil fails the test immediately if v is nil.
// This helper avoids staticcheck SA5011 warnings about nil pointer dereference
// that occur when using the pattern: if x == nil { t.Fatal(...) }; x.Field.
func requireNotNil(t *testing.T, v any, msg string) {
	t.Helper()
	if v == nil {
		t.Fatal(msg)
	}
}
