package driver

import (
	"context"
	"strings"

	"k8s.io/klog/v2"

	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// checkUnmanagedDatasetAtVolumeID is called when DeleteVolume's lookup found no managed
// volume. For dataset-path volume IDs (e.g. "tank/csi/pvc-x") the ID names the dataset
// directly; if a dataset still exists there without tns-csi:managed_by=tns-csi, the
// volume was created by this driver but its properties were never written (or were
// changed). Reporting success would leak the dataset and its share/target forever, and
// deleting it is unsafe, so it fails with FailedPrecondition and a recovery hint.
// Legacy plain-name IDs are found by a property scan and cannot be checked this way.
func (s *ControllerService) checkUnmanagedDatasetAtVolumeID(ctx context.Context, volumeID string) error {
	if !isDatasetPathVolumeID(volumeID) {
		return nil
	}
	dataset, err := s.apiClient.GetDatasetWithProperties(ctx, volumeID)
	if err != nil {
		return status.Errorf(codes.Unavailable, "cannot verify whether dataset %s still exists: %v", volumeID, err)
	}
	if dataset == nil {
		return nil
	}
	managedBy := ""
	if p, ok := dataset.UserProperties[tnsapi.PropertyManagedBy]; ok {
		managedBy = p.Value
	}
	klog.Errorf("DeleteVolume %s: dataset exists but is not marked as managed by tns-csi (managed_by=%q); "+
		"refusing to delete it and refusing to report it as deleted. If this volume was created by tns-csi, run "+
		"'zfs set %s=%s %s' on TrueNAS and the deletion will be retried; otherwise remove the PV without deleting storage.",
		volumeID, managedBy, tnsapi.PropertyManagedBy, tnsapi.ManagedByValue, volumeID)
	return status.Errorf(codes.FailedPrecondition,
		"dataset %s exists but is not managed by tns-csi (managed_by=%q); set %s=%s on it to allow deletion",
		volumeID, managedBy, tnsapi.PropertyManagedBy, tnsapi.ManagedByValue)
}

// volumeOwnership is what DeleteVolume learns from a volume's ZFS user properties.
type volumeOwnership struct {
	props          map[string]string // all requested properties that were present
	deleteStrategy string            // tnsapi.DeleteStrategyDelete unless the volume says otherwise
	notFound       bool              // the dataset/ZVOL no longer exists
}

// readVolumeOwnership reads the properties that authorize deleting meta's dataset, plus
// any protocol-specific extras, and verifies that tns-csi owns the volume.
//
// It fails closed: if the properties cannot be read for any reason other than the
// dataset being gone, it returns Unavailable so the caller deletes nothing and the
// sidecar retries with backoff. Proceeding without them would silently ignore
// deleteStrategy=retain and the ownership checks.
func (s *ControllerService) readVolumeOwnership(ctx context.Context, meta *VolumeMetadata, extraProps ...string) (volumeOwnership, error) {
	names := append([]string{
		tnsapi.PropertyManagedBy,
		tnsapi.PropertyCSIVolumeName,
		tnsapi.PropertyDeleteStrategy,
	}, extraProps...)

	props, err := s.apiClient.GetDatasetProperties(ctx, meta.DatasetID, names)
	if err != nil {
		if isNotFoundError(err) {
			return volumeOwnership{deleteStrategy: tnsapi.DeleteStrategyDelete, notFound: true}, nil
		}
		return volumeOwnership{}, status.Errorf(codes.Unavailable,
			"cannot read ownership properties of %s for volume %s: %v; refusing to delete until they can be verified",
			meta.DatasetID, meta.Name, err)
	}

	if managedBy, ok := props[tnsapi.PropertyManagedBy]; ok && managedBy != tnsapi.ManagedByValue {
		return volumeOwnership{}, status.Errorf(codes.FailedPrecondition,
			"dataset %s is not managed by tns-csi (managed_by=%s), refusing to delete", meta.DatasetID, managedBy)
	}

	// For dataset-path volume IDs (e.g. "tank/pvc-xxx") the stored name is just "pvc-xxx".
	if stored, ok := props[tnsapi.PropertyCSIVolumeName]; ok {
		if stored != meta.Name && (!isDatasetPathVolumeID(meta.Name) || !strings.HasSuffix(meta.Name, "/"+stored)) {
			return volumeOwnership{}, status.Errorf(codes.FailedPrecondition,
				"dataset %s belongs to volume %q, not %q (possible ID reuse), refusing to delete",
				meta.DatasetID, stored, meta.Name)
		}
	}

	o := volumeOwnership{props: props, deleteStrategy: tnsapi.DeleteStrategyDelete}
	if strategy := props[tnsapi.PropertyDeleteStrategy]; strategy != "" {
		o.deleteStrategy = strategy
	}
	return o, nil
}
