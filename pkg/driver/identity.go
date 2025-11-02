package driver

import (
	"context"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"k8s.io/klog/v2"
)

// IdentityService implements the CSI Identity service.
type IdentityService struct {
	csi.UnimplementedIdentityServer
	driverName string
	version    string
}

// NewIdentityService creates a new identity service.
func NewIdentityService(driverName, version string) *IdentityService {
	return &IdentityService{
		driverName: driverName,
		version:    version,
	}
}

// GetPluginInfo returns plugin information.
func (s *IdentityService) GetPluginInfo(_ context.Context, _ *csi.GetPluginInfoRequest) (*csi.GetPluginInfoResponse, error) {
	klog.V(4).Info("GetPluginInfo called")

	if s.driverName == "" {
		return nil, status.Error(codes.Unavailable, "Driver name not configured")
	}

	if s.version == "" {
		return nil, status.Error(codes.Unavailable, "Driver version not configured")
	}

	return &csi.GetPluginInfoResponse{
		Name:          s.driverName,
		VendorVersion: s.version,
	}, nil
}

// GetPluginCapabilities returns plugin capabilities.
func (s *IdentityService) GetPluginCapabilities(_ context.Context, _ *csi.GetPluginCapabilitiesRequest) (*csi.GetPluginCapabilitiesResponse, error) {
	klog.V(4).Info("GetPluginCapabilities called")

	return &csi.GetPluginCapabilitiesResponse{
		Capabilities: []*csi.PluginCapability{
			{
				Type: &csi.PluginCapability_Service_{
					Service: &csi.PluginCapability_Service{
						Type: csi.PluginCapability_Service_CONTROLLER_SERVICE,
					},
				},
			},
			{
				Type: &csi.PluginCapability_Service_{
					Service: &csi.PluginCapability_Service{
						Type: csi.PluginCapability_Service_VOLUME_ACCESSIBILITY_CONSTRAINTS,
					},
				},
			},
		},
	}, nil
}

// Probe returns the health and readiness of the plugin.
func (s *IdentityService) Probe(_ context.Context, _ *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	klog.V(4).Info("Probe called")
	return &csi.ProbeResponse{
		Ready: wrapperspb.Bool(true),
	}, nil
}
