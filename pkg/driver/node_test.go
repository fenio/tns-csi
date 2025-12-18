package driver

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// requireNotNilNode fails the test immediately if v is nil.
// This helper avoids staticcheck SA5011 warnings about nil pointer dereference
// that occur when using the pattern: if x == nil { t.Fatal(...) }; x.Field.
func requireNotNilNode(t *testing.T, v any, msg string) {
	t.Helper()
	if v == nil {
		t.Fatal(msg)
	}
}

func TestNewNodeService(t *testing.T) {
	registry := NewNodeRegistry()
	mockClient := &mockAPIClient{}
	nodeID := "test-node-123"

	service := NewNodeService(nodeID, mockClient, true, registry)

	// Use require pattern - fail immediately if nil.
	requireNotNilNode(t, service, "NewNodeService returned nil")

	if service.nodeID != nodeID {
		t.Errorf("Expected nodeID=%q, got %q", nodeID, service.nodeID)
	}
	if service.testMode != true {
		t.Error("Expected testMode=true")
	}
	if service.nodeRegistry != registry {
		t.Error("Expected nodeRegistry to be set")
	}
}

func TestNodeGetCapabilities(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil)

	resp, err := service.NodeGetCapabilities(context.Background(), nil)
	if err != nil {
		t.Fatalf("NodeGetCapabilities() error = %v", err)
	}

	// Use require pattern - fail immediately if nil.
	requireNotNilNode(t, resp, "NodeGetCapabilities() returned nil response")

	if len(resp.Capabilities) == 0 {
		t.Error("NodeGetCapabilities() returned no capabilities")
	}

	// Verify expected capabilities are present.
	expectedCaps := map[csi.NodeServiceCapability_RPC_Type]bool{
		csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME: false,
		csi.NodeServiceCapability_RPC_GET_VOLUME_STATS:     false,
		csi.NodeServiceCapability_RPC_EXPAND_VOLUME:        false,
	}

	for _, cap := range resp.Capabilities {
		if rpc := cap.GetRpc(); rpc != nil {
			expectedCaps[rpc.Type] = true
		}
	}

	for capType, found := range expectedCaps {
		if !found {
			t.Errorf("Expected capability %v not found", capType)
		}
	}
}

func TestNodeGetInfo(t *testing.T) {
	t.Run("with registry", func(t *testing.T) {
		registry := NewNodeRegistry()
		nodeID := "test-node-456"
		service := NewNodeService(nodeID, nil, true, registry)

		resp, err := service.NodeGetInfo(context.Background(), nil)
		if err != nil {
			t.Fatalf("NodeGetInfo() error = %v", err)
		}

		// Use require pattern - fail immediately if nil.
		requireNotNilNode(t, resp, "NodeGetInfo() returned nil response")

		if resp.NodeId != nodeID {
			t.Errorf("Expected NodeId=%q, got %q", nodeID, resp.NodeId)
		}

		// Verify node was registered.
		if !registry.IsRegistered(nodeID) {
			t.Error("Expected node to be registered in registry")
		}
	})

	t.Run("without registry", func(t *testing.T) {
		nodeID := "test-node-789"
		service := NewNodeService(nodeID, nil, true, nil)

		resp, err := service.NodeGetInfo(context.Background(), nil)
		if err != nil {
			t.Fatalf("NodeGetInfo() error = %v", err)
		}

		if resp.NodeId != nodeID {
			t.Errorf("Expected NodeId=%q, got %q", nodeID, resp.NodeId)
		}
	})
}

func TestNodeStageVolume_Validation(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil)
	ctx := context.Background()

	//nolint:govet // Field alignment not critical for test structs
	tests := []struct {
		name     string
		req      *csi.NodeStageVolumeRequest
		wantErr  bool
		wantCode codes.Code
	}{
		{
			name: "missing volume ID",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          "",
				StagingTargetPath: "/staging/path",
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "missing staging target path",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          "test-volume",
				StagingTargetPath: "",
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "missing volume capability",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          "test-volume",
				StagingTargetPath: "/staging/path",
				VolumeCapability:  nil,
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "unsupported protocol",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          "test-volume",
				StagingTargetPath: "/staging/path",
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
				VolumeContext: map[string]string{
					VolumeContextKeyProtocol: "unsupported",
				},
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.NodeStageVolume(ctx, tt.req)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				if st, ok := status.FromError(err); ok {
					if st.Code() != tt.wantCode {
						t.Errorf("Expected error code %v, got %v", tt.wantCode, st.Code())
					}
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestNodeUnstageVolume_Validation(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil)
	ctx := context.Background()

	//nolint:govet // Field alignment not critical for test structs
	tests := []struct {
		name     string
		req      *csi.NodeUnstageVolumeRequest
		wantErr  bool
		wantCode codes.Code
	}{
		{
			name: "missing volume ID",
			req: &csi.NodeUnstageVolumeRequest{
				VolumeId:          "",
				StagingTargetPath: "/staging/path",
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "missing staging target path",
			req: &csi.NodeUnstageVolumeRequest{
				VolumeId:          "test-volume",
				StagingTargetPath: "",
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.NodeUnstageVolume(ctx, tt.req)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				if st, ok := status.FromError(err); ok {
					if st.Code() != tt.wantCode {
						t.Errorf("Expected error code %v, got %v", tt.wantCode, st.Code())
					}
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestNodePublishVolume_Validation(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil)
	ctx := context.Background()

	//nolint:govet // Field alignment not critical for test structs
	tests := []struct {
		name     string
		req      *csi.NodePublishVolumeRequest
		wantErr  bool
		wantCode codes.Code
	}{
		{
			name: "missing volume ID",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:   "",
				TargetPath: "/target/path",
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "missing target path",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:   "test-volume",
				TargetPath: "",
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "missing volume capability",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:         "test-volume",
				TargetPath:       "/target/path",
				VolumeCapability: nil,
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "NVMe-oF missing staging target path",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:          "test-volume",
				TargetPath:        "/target/path",
				StagingTargetPath: "", // Missing for NVMe-oF
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
				VolumeContext: map[string]string{
					VolumeContextKeyProtocol: ProtocolNVMeOF,
				},
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.NodePublishVolume(ctx, tt.req)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				if st, ok := status.FromError(err); ok {
					if st.Code() != tt.wantCode {
						t.Errorf("Expected error code %v, got %v", tt.wantCode, st.Code())
					}
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestNodeUnpublishVolume_Validation(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil)
	ctx := context.Background()

	//nolint:govet // Field alignment not critical for test structs
	tests := []struct {
		name     string
		req      *csi.NodeUnpublishVolumeRequest
		wantErr  bool
		wantCode codes.Code
	}{
		{
			name: "missing volume ID",
			req: &csi.NodeUnpublishVolumeRequest{
				VolumeId:   "",
				TargetPath: "/target/path",
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "missing target path",
			req: &csi.NodeUnpublishVolumeRequest{
				VolumeId:   "test-volume",
				TargetPath: "",
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.NodeUnpublishVolume(ctx, tt.req)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				if st, ok := status.FromError(err); ok {
					if st.Code() != tt.wantCode {
						t.Errorf("Expected error code %v, got %v", tt.wantCode, st.Code())
					}
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestNodeUnpublishVolume_TestMode(t *testing.T) {
	// Create a temporary directory to act as target path.
	tmpDir, err := os.MkdirTemp("", "csi-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	targetPath := filepath.Join(tmpDir, "target")
	if mkdirErr := os.MkdirAll(targetPath, 0o755); mkdirErr != nil {
		t.Fatalf("Failed to create target path: %v", mkdirErr)
	}

	service := NewNodeService("test-node", nil, true, nil) // testMode=true
	ctx := context.Background()

	resp, err := service.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{
		VolumeId:   "test-volume",
		TargetPath: targetPath,
	})

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if resp == nil {
		t.Error("Expected non-nil response")
	}

	// Verify the target path was removed.
	if _, statErr := os.Stat(targetPath); !os.IsNotExist(statErr) {
		t.Error("Expected target path to be removed in test mode")
	}
}

func TestNodeGetVolumeStats_Validation(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil)
	ctx := context.Background()

	//nolint:govet // Field alignment not critical for test structs
	tests := []struct {
		name     string
		req      *csi.NodeGetVolumeStatsRequest
		wantErr  bool
		wantCode codes.Code
	}{
		{
			name: "missing volume ID",
			req: &csi.NodeGetVolumeStatsRequest{
				VolumeId:   "",
				VolumePath: "/volume/path",
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "missing volume path",
			req: &csi.NodeGetVolumeStatsRequest{
				VolumeId:   "test-volume",
				VolumePath: "",
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "volume path does not exist",
			req: &csi.NodeGetVolumeStatsRequest{
				VolumeId:   "test-volume",
				VolumePath: "/nonexistent/path",
			},
			wantErr:  true,
			wantCode: codes.NotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.NodeGetVolumeStats(ctx, tt.req)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				if st, ok := status.FromError(err); ok {
					if st.Code() != tt.wantCode {
						t.Errorf("Expected error code %v, got %v", tt.wantCode, st.Code())
					}
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestNodeGetVolumeStats_TestMode(t *testing.T) {
	// Create a temporary directory to act as volume path.
	tmpDir, err := os.MkdirTemp("", "csi-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	service := NewNodeService("test-node", nil, true, nil) // testMode=true
	ctx := context.Background()

	resp, err := service.NodeGetVolumeStats(ctx, &csi.NodeGetVolumeStatsRequest{
		VolumeId:   "test-volume",
		VolumePath: tmpDir,
	})

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Use require pattern - fail immediately if nil.
	requireNotNilNode(t, resp, "Expected non-nil response")

	// In test mode, should return mock stats.
	if len(resp.Usage) == 0 {
		t.Error("Expected usage stats in response")
	}

	// Check for BYTES usage.
	var foundBytes bool
	for _, usage := range resp.Usage {
		if usage.Unit == csi.VolumeUsage_BYTES {
			foundBytes = true
			if usage.Total != 1073741824 { // 1GB mock value
				t.Errorf("Expected Total=1GB, got %d", usage.Total)
			}
		}
	}
	if !foundBytes {
		t.Error("Expected BYTES usage in response")
	}
}

func TestNodeExpandVolume_Validation(t *testing.T) {
	service := NewNodeService("test-node", nil, true, nil)
	ctx := context.Background()

	//nolint:govet // Field alignment not critical for test structs
	tests := []struct {
		name     string
		req      *csi.NodeExpandVolumeRequest
		wantErr  bool
		wantCode codes.Code
	}{
		{
			name: "missing volume ID",
			req: &csi.NodeExpandVolumeRequest{
				VolumeId:   "",
				VolumePath: "/volume/path",
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "missing volume path",
			req: &csi.NodeExpandVolumeRequest{
				VolumeId:   "test-volume",
				VolumePath: "",
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "volume path does not exist",
			req: &csi.NodeExpandVolumeRequest{
				VolumeId:   "test-volume",
				VolumePath: "/nonexistent/path",
			},
			wantErr:  true,
			wantCode: codes.NotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.NodeExpandVolume(ctx, tt.req)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				if st, ok := status.FromError(err); ok {
					if st.Code() != tt.wantCode {
						t.Errorf("Expected error code %v, got %v", tt.wantCode, st.Code())
					}
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestNodeExpandVolume_TestMode(t *testing.T) {
	// Create a temporary directory to act as volume path.
	tmpDir, err := os.MkdirTemp("", "csi-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	service := NewNodeService("test-node", nil, true, nil) // testMode=true
	ctx := context.Background()

	requestedBytes := int64(5 * 1024 * 1024 * 1024) // 5GB
	resp, err := service.NodeExpandVolume(ctx, &csi.NodeExpandVolumeRequest{
		VolumeId:   "test-volume",
		VolumePath: tmpDir,
		CapacityRange: &csi.CapacityRange{
			RequiredBytes: requestedBytes,
		},
	})

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Use require pattern - fail immediately if nil.
	requireNotNilNode(t, resp, "Expected non-nil response")

	// In test mode, should return the requested capacity.
	if resp.CapacityBytes != requestedBytes {
		t.Errorf("Expected CapacityBytes=%d, got %d", requestedBytes, resp.CapacityBytes)
	}
}

func TestSafeUint64ToInt64(t *testing.T) {
	tests := []struct {
		name  string
		input uint64
		want  int64
	}{
		{
			name:  "zero",
			input: 0,
			want:  0,
		},
		{
			name:  "small value",
			input: 1024,
			want:  1024,
		},
		{
			name:  "1GB",
			input: 1073741824,
			want:  1073741824,
		},
		{
			name:  "max int64",
			input: 9223372036854775807,
			want:  9223372036854775807,
		},
		{
			name:  "exceeds max int64",
			input: 9223372036854775808, // max int64 + 1
			want:  9223372036854775807, // should be capped
		},
		{
			name:  "max uint64",
			input: 18446744073709551615,
			want:  9223372036854775807, // should be capped
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := safeUint64ToInt64(tt.input)
			if got != tt.want {
				t.Errorf("safeUint64ToInt64(%d) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestGetProtocolFromVolumeContext(t *testing.T) {
	tests := []struct {
		name          string
		volumeContext map[string]string
		want          string
	}{
		{
			name:          "nil context defaults to NFS",
			volumeContext: nil,
			want:          ProtocolNFS,
		},
		{
			name:          "empty context defaults to NFS",
			volumeContext: map[string]string{},
			want:          ProtocolNFS,
		},
		{
			name: "explicit NFS protocol",
			volumeContext: map[string]string{
				VolumeContextKeyProtocol: ProtocolNFS,
			},
			want: ProtocolNFS,
		},
		{
			name: "explicit NVMe-oF protocol",
			volumeContext: map[string]string{
				VolumeContextKeyProtocol: ProtocolNVMeOF,
			},
			want: ProtocolNVMeOF,
		},
		{
			name: "unknown protocol returns as-is",
			volumeContext: map[string]string{
				VolumeContextKeyProtocol: "iscsi",
			},
			want: "iscsi",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getProtocolFromVolumeContext(tt.volumeContext)
			if got != tt.want {
				t.Errorf("getProtocolFromVolumeContext(%v) = %q, want %q", tt.volumeContext, got, tt.want)
			}
		})
	}
}

func TestGetNFSMountOptions(t *testing.T) {
	//nolint:govet // Field alignment not critical for test structs
	tests := []struct {
		name        string
		userOptions []string
		wantLen     int
		wantContain []string
	}{
		{
			name:        "no user options returns defaults",
			userOptions: nil,
			wantLen:     len(defaultNFSMountOptions),
			wantContain: defaultNFSMountOptions,
		},
		{
			name:        "empty user options returns defaults",
			userOptions: []string{},
			wantLen:     len(defaultNFSMountOptions),
			wantContain: defaultNFSMountOptions,
		},
		{
			name:        "user options merged with defaults",
			userOptions: []string{"hard", "nointr"},
			wantLen:     4, // user options + defaults
			wantContain: []string{"hard", "nointr"},
		},
		{
			name:        "user option overrides default vers",
			userOptions: []string{"vers=3"},
			wantLen:     2, // vers=3 + nolock (default vers=4.x is overridden)
			wantContain: []string{"vers=3", "nolock"},
		},
		{
			name:        "user option lock is added along with defaults",
			userOptions: []string{"lock"},
			// Note: Our simple key-based conflict detection doesn't handle
			// lock/nolock pairs - they're different keys. User must specify
			// both options explicitly if they want to override nolock with lock.
			wantLen:     3, // lock + vers=4.x + nolock (all added)
			wantContain: []string{"lock"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getNFSMountOptions(tt.userOptions)
			if len(got) != tt.wantLen {
				t.Errorf("getNFSMountOptions(%v) returned %d options, want %d. Got: %v",
					tt.userOptions, len(got), tt.wantLen, got)
			}
			for _, want := range tt.wantContain {
				found := false
				for _, opt := range got {
					if opt == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("getNFSMountOptions(%v) missing expected option %q. Got: %v",
						tt.userOptions, want, got)
				}
			}
		})
	}
}

func TestExtractOptionKey(t *testing.T) {
	tests := []struct {
		name   string
		option string
		want   string
	}{
		{
			name:   "key=value option",
			option: "vers=4.2",
			want:   "vers",
		},
		{
			name:   "flag option",
			option: "nolock",
			want:   "nolock",
		},
		{
			name:   "another flag option",
			option: "hard",
			want:   "hard",
		},
		{
			name:   "complex key=value",
			option: "rsize=1048576",
			want:   "rsize",
		},
		{
			name:   "empty option",
			option: "",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractOptionKey(tt.option)
			if got != tt.want {
				t.Errorf("extractOptionKey(%q) = %q, want %q", tt.option, got, tt.want)
			}
		})
	}
}

func TestGetNVMeOFMountOptions(t *testing.T) {
	//nolint:govet // Field alignment not critical for test structs
	tests := []struct {
		name        string
		userOptions []string
		wantLen     int
		wantContain []string
	}{
		{
			name:        "no user options returns defaults",
			userOptions: nil,
			wantLen:     len(defaultNVMeOFMountOptions),
			wantContain: defaultNVMeOFMountOptions,
		},
		{
			name:        "empty user options returns defaults",
			userOptions: []string{},
			wantLen:     len(defaultNVMeOFMountOptions),
			wantContain: defaultNVMeOFMountOptions,
		},
		{
			name:        "user options merged with defaults",
			userOptions: []string{"discard", "data=ordered"},
			wantLen:     3, // user options + noatime default
			wantContain: []string{"discard", "data=ordered", "noatime"},
		},
		{
			name:        "user option atime is added along with defaults",
			userOptions: []string{"atime"},
			// Note: Our simple key-based conflict detection doesn't handle
			// atime/noatime pairs - they're different keys. User must specify
			// both options explicitly if they want to override noatime with atime.
			wantLen:     2, // atime + noatime (both added)
			wantContain: []string{"atime"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getNVMeOFMountOptions(tt.userOptions)
			if len(got) != tt.wantLen {
				t.Errorf("getNVMeOFMountOptions(%v) returned %d options, want %d. Got: %v",
					tt.userOptions, len(got), tt.wantLen, got)
			}
			for _, want := range tt.wantContain {
				found := false
				for _, opt := range got {
					if opt == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("getNVMeOFMountOptions(%v) missing expected option %q. Got: %v",
						tt.userOptions, want, got)
				}
			}
		})
	}
}

func TestExtractNVMeOFOptionKey(t *testing.T) {
	tests := []struct {
		name   string
		option string
		want   string
	}{
		{
			name:   "key=value option",
			option: "data=ordered",
			want:   "data",
		},
		{
			name:   "flag option",
			option: "noatime",
			want:   "noatime",
		},
		{
			name:   "another flag option",
			option: "discard",
			want:   "discard",
		},
		{
			name:   "empty option",
			option: "",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractNVMeOFOptionKey(tt.option)
			if got != tt.want {
				t.Errorf("extractNVMeOFOptionKey(%q) = %q, want %q", tt.option, got, tt.want)
			}
		})
	}
}
