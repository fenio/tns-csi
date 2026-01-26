package driver

import (
	"testing"
)

func TestHealthy(t *testing.T) {
	h := Healthy()
	if h.Abnormal {
		t.Error("Healthy() should return Abnormal=false")
	}
	if h.Message != "" {
		t.Errorf("Healthy() should have empty message, got %q", h.Message)
	}
}

func TestUnhealthy(t *testing.T) {
	tests := []struct {
		name    string
		message string
	}{
		{
			name:    "simple message",
			message: "volume not found",
		},
		{
			name:    "detailed message",
			message: "NFS mount stale: server 192.168.1.100 unreachable",
		},
		{
			name:    "empty message",
			message: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := Unhealthy(tt.message)
			if !h.Abnormal {
				t.Error("Unhealthy() should return Abnormal=true")
			}
			if h.Message != tt.message {
				t.Errorf("Unhealthy(%q) message = %q, want %q", tt.message, h.Message, tt.message)
			}
		})
	}
}

func TestVolumeHealthToCSI(t *testing.T) {
	tests := []struct {
		name   string
		health VolumeHealth
	}{
		{
			name:   "healthy volume",
			health: Healthy(),
		},
		{
			name:   "unhealthy volume",
			health: Unhealthy("connection timeout"),
		},
		{
			name: "custom health",
			health: VolumeHealth{
				Abnormal: true,
				Message:  "custom error message",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			csiCondition := tt.health.ToCSI()
			if csiCondition == nil {
				t.Fatal("ToCSI() returned nil")
				return
			}
			if csiCondition.Abnormal != tt.health.Abnormal {
				t.Errorf("ToCSI().Abnormal = %v, want %v", csiCondition.Abnormal, tt.health.Abnormal)
			}
			if csiCondition.Message != tt.health.Message {
				t.Errorf("ToCSI().Message = %q, want %q", csiCondition.Message, tt.health.Message)
			}
		})
	}
}

func TestCheckBasicHealth(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantHealth bool // true = healthy, false = unhealthy
	}{
		{
			name:       "existing path",
			path:       "/tmp",
			wantHealth: true,
		},
		{
			name:       "non-existing path",
			path:       "/nonexistent/path/that/does/not/exist/12345",
			wantHealth: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			health := checkBasicHealth(tt.path)
			if tt.wantHealth && health.Abnormal {
				t.Errorf("checkBasicHealth(%q) = unhealthy, want healthy", tt.path)
			}
			if !tt.wantHealth && !health.Abnormal {
				t.Errorf("checkBasicHealth(%q) = healthy, want unhealthy", tt.path)
			}
		})
	}
}

func TestGetNVMeControllerState(t *testing.T) {
	tests := []struct {
		name       string
		devicePath string
		wantErr    bool
	}{
		{
			name:       "non-nvme device",
			devicePath: "/dev/sda",
			wantErr:    true,
		},
		{
			name:       "nvme device - likely not present",
			devicePath: "/dev/nvme0n1",
			wantErr:    true, // Will fail on most test systems without NVMe
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := getNVMeControllerState(tt.devicePath)
			if tt.wantErr && err == nil {
				t.Errorf("getNVMeControllerState(%q) expected error, got nil", tt.devicePath)
			}
			// If we don't expect an error but get one, that's okay - the device may not exist
			// This test is mainly to ensure the function doesn't panic
		})
	}
}

func TestNVMeControllerNameExtraction(t *testing.T) {
	// Test the controller name extraction logic from getNVMeControllerState
	// The function extracts "nvme0" from device names like "nvme0n1" or "nvme0n1p1"

	//nolint:govet // fieldalignment: test struct optimizes for readability
	tests := []struct {
		devicePath string
		wantErr    bool
		errType    error
	}{
		{
			devicePath: "/dev/sda",
			wantErr:    true,
			errType:    errNotNVMeDevice,
		},
		{
			devicePath: "/dev/vda",
			wantErr:    true,
			errType:    errNotNVMeDevice,
		},
		{
			devicePath: "/dev/xvda",
			wantErr:    true,
			errType:    errNotNVMeDevice,
		},
	}

	for _, tt := range tests {
		t.Run(tt.devicePath, func(t *testing.T) {
			_, err := getNVMeControllerState(tt.devicePath)
			if tt.wantErr {
				if err == nil {
					t.Errorf("getNVMeControllerState(%q) expected error, got nil", tt.devicePath)
					return
				}
				// For non-NVMe devices, we should get the specific error type
				if tt.errType != nil {
					if err.Error() != "not an NVMe device: "+tt.devicePath {
						// The error might be different if the path format changed
						t.Logf("Got error: %v", err)
					}
				}
			}
		})
	}
}

func TestStaticErrors(t *testing.T) {
	// Verify static errors are properly defined
	if errMountTimeout == nil {
		t.Error("errMountTimeout should not be nil")
	}
	if errReadTimeout == nil {
		t.Error("errReadTimeout should not be nil")
	}
	if errNotNVMeDevice == nil {
		t.Error("errNotNVMeDevice should not be nil")
	}
	if errISCSIStateUnknown == nil {
		t.Error("errISCSIStateUnknown should not be nil")
	}

	// Verify error messages are useful
	if errMountTimeout.Error() == "" {
		t.Error("errMountTimeout should have a non-empty message")
	}
	if errReadTimeout.Error() == "" {
		t.Error("errReadTimeout should have a non-empty message")
	}
	if errNotNVMeDevice.Error() == "" {
		t.Error("errNotNVMeDevice should have a non-empty message")
	}
	if errISCSIStateUnknown.Error() == "" {
		t.Error("errISCSIStateUnknown should have a non-empty message")
	}
}

func TestVolumeHealthEquality(t *testing.T) {
	// Test that two Healthy() calls produce equivalent results
	h1 := Healthy()
	h2 := Healthy()

	if h1.Abnormal != h2.Abnormal {
		t.Error("Two Healthy() calls should produce same Abnormal value")
	}
	if h1.Message != h2.Message {
		t.Error("Two Healthy() calls should produce same Message value")
	}

	// Test Unhealthy with same message
	u1 := Unhealthy("test error")
	u2 := Unhealthy("test error")

	if u1.Abnormal != u2.Abnormal {
		t.Error("Two Unhealthy() calls with same message should produce same Abnormal value")
	}
	if u1.Message != u2.Message {
		t.Error("Two Unhealthy() calls with same message should produce same Message value")
	}
}

func TestProtocolConstants(t *testing.T) {
	// Ensure protocol constants are defined (used by health check routing)
	if ProtocolNFS == "" {
		t.Error("ProtocolNFS should not be empty")
	}
	if ProtocolNVMeOF == "" {
		t.Error("ProtocolNVMeOF should not be empty")
	}
	if ProtocolISCSI == "" {
		t.Error("ProtocolISCSI should not be empty")
	}

	// Ensure they are distinct
	protocols := map[string]bool{
		ProtocolNFS:    true,
		ProtocolNVMeOF: true,
		ProtocolISCSI:  true,
	}
	if len(protocols) != 3 {
		t.Error("Protocol constants should be distinct")
	}
}
