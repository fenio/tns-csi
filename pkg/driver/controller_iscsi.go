// Package driver implements iSCSI-specific CSI controller operations.
//
// NOTE: iSCSI support is planned for future implementation. Currently, all iSCSI
// operations return codes.Unimplemented errors per CSI specification.
// Focus is on NFS and NVMe-oF protocols which are fully functional.
package driver

import (
	"context"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// createISCSIVolume creates an iSCSI volume (not yet implemented).
func (s *ControllerService) createISCSIVolume(_ context.Context, _ *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	klog.V(4).Info("Creating iSCSI volume")

	// TODO: Implement iSCSI volume creation
	// 1. Create zvol
	// 2. Create iSCSI extent
	// 3. Create target
	// 4. Associate extent with target

	return nil, status.Error(codes.Unimplemented, "iSCSI volume creation not yet implemented")
}

// deleteISCSIVolume deletes an iSCSI volume (not yet implemented).
func (s *ControllerService) deleteISCSIVolume(_ context.Context, _ *VolumeMetadata) (*csi.DeleteVolumeResponse, error) {
	klog.V(4).Info("Deleting iSCSI volume")

	// TODO: Implement iSCSI volume deletion
	// 1. Remove target associations
	// 2. Delete iSCSI extent
	// 3. Delete zvol

	return nil, status.Error(codes.Unimplemented, "iSCSI volume deletion not yet implemented")
}

// expandISCSIVolume expands an iSCSI volume (not yet implemented).
func (s *ControllerService) expandISCSIVolume(_ context.Context, _ *VolumeMetadata, _ int64) (*csi.ControllerExpandVolumeResponse, error) {
	klog.V(4).Info("Expanding iSCSI volume")

	// TODO: Implement iSCSI volume expansion
	// Similar to NVMe-oF: update ZVOL size

	return nil, status.Error(codes.Unimplemented, "iSCSI volume expansion not yet implemented")
}
