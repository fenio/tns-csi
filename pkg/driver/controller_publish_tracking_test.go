package driver

import (
	"context"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func publishRequest(volumeID, nodeID string, readonly bool) *csi.ControllerPublishVolumeRequest {
	return &csi.ControllerPublishVolumeRequest{
		VolumeId: volumeID,
		NodeId:   nodeID,
		Readonly: readonly,
		VolumeCapability: &csi.VolumeCapability{
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
		},
	}
}

// TestControllerUnpublishVolumeWithoutNodeID covers the CSI rule that an
// unpublish without node_id removes the volume from every node. Before the fix
// it was a no-op, so a later publish with a different readonly flag was
// rejected with AlreadyExists although the volume was unpublished everywhere.
func TestControllerUnpublishVolumeWithoutNodeID(t *testing.T) {
	ctx := context.Background()
	service := NewControllerService(&mockAPIClient{}, nil, "")

	for _, node := range []string{"node-a", "node-b"} {
		if _, err := service.ControllerPublishVolume(ctx, publishRequest("vol-1", node, false)); err != nil {
			t.Fatalf("publish to %s: %v", node, err)
		}
	}
	if _, err := service.ControllerPublishVolume(ctx, publishRequest("vol-2", "node-a", false)); err != nil {
		t.Fatalf("publish vol-2: %v", err)
	}

	if _, err := service.ControllerUnpublishVolume(ctx, &csi.ControllerUnpublishVolumeRequest{VolumeId: "vol-1"}); err != nil {
		t.Fatalf("unpublish without node: %v", err)
	}

	for _, node := range []string{"node-a", "node-b"} {
		if _, err := service.ControllerPublishVolume(ctx, publishRequest("vol-1", node, true)); err != nil {
			t.Errorf("vol-1 should be unpublished from %s, re-publish readonly got: %v", node, err)
		}
	}
	// Other volumes keep their tracking.
	_, err := service.ControllerPublishVolume(ctx, publishRequest("vol-2", "node-a", true))
	if status.Code(err) != codes.AlreadyExists {
		t.Errorf("vol-2 on node-a should still be tracked, got code %v", status.Code(err))
	}
}

// TestControllerUnpublishVolumeOtherNodeKeepsTracking guards the per-node key:
// unpublishing from one node must not forget the publish on another.
func TestControllerUnpublishVolumeOtherNodeKeepsTracking(t *testing.T) {
	ctx := context.Background()
	service := NewControllerService(&mockAPIClient{}, nil, "")

	for _, node := range []string{"node-a", "node-b"} {
		if _, err := service.ControllerPublishVolume(ctx, publishRequest("vol-1", node, false)); err != nil {
			t.Fatalf("publish to %s: %v", node, err)
		}
	}
	if _, err := service.ControllerUnpublishVolume(ctx, &csi.ControllerUnpublishVolumeRequest{VolumeId: "vol-1", NodeId: "node-a"}); err != nil {
		t.Fatalf("unpublish from node-a: %v", err)
	}
	if _, err := service.ControllerPublishVolume(ctx, publishRequest("vol-1", "node-a", true)); err != nil {
		t.Errorf("node-a should be unpublished, got: %v", err)
	}
	_, err := service.ControllerPublishVolume(ctx, publishRequest("vol-1", "node-b", true))
	if status.Code(err) != codes.AlreadyExists {
		t.Errorf("node-b should still be tracked, got code %v", status.Code(err))
	}
}
