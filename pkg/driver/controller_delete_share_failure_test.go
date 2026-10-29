package driver

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The share ID lives in a property on the dataset. If the share delete fails for a
// transient reason and the dataset is destroyed anyway, the export is orphaned with
// nothing left that remembers it. iSCSI and NVMe-oF already keep the ZVOL in that
// case; NFS and SMB must do the same and report Unavailable so the sidecar retries.
func TestFilesystemDeleteKeepsDatasetWhenShareDeleteFails(t *testing.T) {
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
	}

	for protocol, meta := range cases {
		t.Run(protocol, func(t *testing.T) {
			rec := &destructiveCalls{}
			mock := rec.mock()
			mock.DeleteNFSShareFunc = func(context.Context, int) error { rec.calls = append(rec.calls, "DeleteNFSShare"); return transient }
			mock.DeleteSMBShareFunc = func(context.Context, int) error { rec.calls = append(rec.calls, "DeleteSMBShare"); return transient }
			s := NewControllerService(mock, NewNodeRegistry(), "")

			var err error
			switch protocol {
			case ProtocolNFS:
				_, err = s.deleteNFSVolume(context.Background(), meta)
			case ProtocolSMB:
				_, err = s.deleteSMBVolume(context.Background(), meta)
			}

			if status.Code(err) != codes.Unavailable {
				t.Fatalf("delete error = %v (code %s), want Unavailable", err, status.Code(err))
			}
			for _, call := range rec.calls {
				if call == "DeleteDataset" {
					t.Fatalf("dataset was deleted after the share delete failed; calls: %v", rec.calls)
				}
			}
		})
	}
}
