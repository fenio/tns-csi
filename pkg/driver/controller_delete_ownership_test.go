package driver

import (
	"context"
	"errors"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// destructiveCalls records every API call that would remove storage or its export.
type destructiveCalls struct{ calls []string }

func (d *destructiveCalls) mock() *MockAPIClientForSnapshots {
	record := func(name string) { d.calls = append(d.calls, name) }
	return &MockAPIClientForSnapshots{
		DeleteDatasetFunc:         func(context.Context, string) error { record("DeleteDataset"); return nil },
		DeleteNFSShareFunc:        func(context.Context, int) error { record("DeleteNFSShare"); return nil },
		DeleteNVMeOFSubsystemFunc: func(context.Context, int) error { record("DeleteNVMeOFSubsystem"); return nil },
		DeleteNVMeOFNamespaceFunc: func(context.Context, int) error { record("DeleteNVMeOFNamespace"); return nil },
		DeleteISCSIExtentFunc:     func(context.Context, int) error { record("DeleteISCSIExtent"); return nil },
	}
}

// A transient failure reading the ownership/deleteStrategy properties must not be
// treated as "no properties": that silently dropped deleteStrategy=retain and deleted
// the volume. Delete must fail with Unavailable (sidecar backs off and retries) and
// touch nothing.
func TestDeleteVolumeFailsClosedWhenOwnershipCannotBeRead(t *testing.T) {
	transient := errors.New("websocket: i/o timeout")

	cases := map[string]*VolumeMetadata{
		ProtocolNFS: {
			Name: "tank/csi/pvc-1", Protocol: ProtocolNFS, DatasetID: "tank/csi/pvc-1",
			DatasetName: "tank/csi/pvc-1", NFSShareID: 7,
		},
		ProtocolSMB: {
			Name: "tank/csi/pvc-2", Protocol: ProtocolSMB, DatasetID: "tank/csi/pvc-2",
			DatasetName: "tank/csi/pvc-2", SMBShareID: 8,
		},
		ProtocolISCSI: {
			Name: "tank/csi/pvc-3", Protocol: ProtocolISCSI, DatasetID: "tank/csi/pvc-3",
			DatasetName: "tank/csi/pvc-3", ISCSITargetID: 9, ISCSIExtentID: 10,
		},
		ProtocolNVMeOF: {
			Name: "tank/csi/pvc-4", Protocol: ProtocolNVMeOF, DatasetID: "tank/csi/pvc-4",
			DatasetName: "tank/csi/pvc-4", NVMeOFSubsystemID: 11, NVMeOFNamespaceID: 12,
		},
	}

	for protocol, meta := range cases {
		t.Run(protocol, func(t *testing.T) {
			rec := &destructiveCalls{}
			mock := rec.mock()
			mock.GetDatasetPropertiesFunc = func(context.Context, string, []string) (map[string]string, error) {
				return nil, transient
			}
			s := NewControllerService(mock, NewNodeRegistry(), "")

			var err error
			switch protocol {
			case ProtocolNFS:
				_, err = s.deleteNFSVolume(context.Background(), meta)
			case ProtocolSMB:
				_, err = s.deleteSMBVolume(context.Background(), meta)
			case ProtocolISCSI:
				_, err = s.deleteISCSIVolume(context.Background(), meta)
			case ProtocolNVMeOF:
				_, err = s.deleteNVMeOFVolume(context.Background(), meta)
			}

			if status.Code(err) != codes.Unavailable {
				t.Errorf("delete error code = %v (err %v), want Unavailable", status.Code(err), err)
			}
			if len(rec.calls) != 0 {
				t.Errorf("delete issued destructive calls %v after failing to read ownership", rec.calls)
			}
		})
	}
}

// A dataset that exists at a dataset-path volume ID but lacks tns-csi:managed_by was
// previously reported as "not found", so DeleteVolume returned success and the dataset
// (plus its share/target) leaked forever. It must be surfaced instead.
func TestDeleteVolumeReportsExistingUnmanagedDataset(t *testing.T) {
	rec := &destructiveCalls{}
	mock := rec.mock()
	mock.GetDatasetWithPropertiesFunc = func(_ context.Context, id string) (*tnsapi.DatasetWithProperties, error) {
		ds := &tnsapi.DatasetWithProperties{}
		ds.ID = id
		ds.UserProperties = map[string]tnsapi.UserProperty{
			tnsapi.PropertyCSIVolumeName: {Value: "pvc-5"}, // managed_by missing
		}
		return ds, nil
	}
	s := NewControllerService(mock, NewNodeRegistry(), "")

	_, err := s.DeleteVolume(context.Background(), &csi.DeleteVolumeRequest{VolumeId: "tank/csi/pvc-5"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("DeleteVolume code = %v (err %v), want FailedPrecondition", status.Code(err), err)
	}
	if len(rec.calls) != 0 {
		t.Errorf("DeleteVolume issued destructive calls %v for an unmanaged dataset", rec.calls)
	}
}

func TestDeleteVolumeStillSucceedsWhenDatasetIsGone(t *testing.T) {
	mock := (&destructiveCalls{}).mock()
	mock.GetDatasetWithPropertiesFunc = func(context.Context, string) (*tnsapi.DatasetWithProperties, error) {
		return nil, nil //nolint:nilnil // mirrors the real client: nil, nil means the dataset does not exist
	}
	s := NewControllerService(mock, NewNodeRegistry(), "")

	if _, err := s.DeleteVolume(context.Background(), &csi.DeleteVolumeRequest{VolumeId: "tank/csi/pvc-6"}); err != nil {
		t.Fatalf("DeleteVolume for a missing dataset = %v, want success (idempotent)", err)
	}
}
