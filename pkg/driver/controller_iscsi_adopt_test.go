package driver

import (
	"context"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func adoptISCSIFixture() (*csi.CreateVolumeRequest, *tnsapi.DatasetWithProperties) {
	req := &csi.CreateVolumeRequest{
		Name:          "pvc-adopt",
		CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30},
	}
	dataset := &tnsapi.DatasetWithProperties{
		Dataset: tnsapi.Dataset{ID: "tank/csi/pvc-adopt", Name: "tank/csi/pvc-adopt"},
	}
	return req, dataset
}

func TestAdoptISCSIVolume_HonoursPortalAndInitiatorParameters(t *testing.T) {
	tests := []struct {
		params        map[string]string
		name          string
		wantPortal    int
		wantInitiator int
	}{
		{
			name:          "explicit ids",
			params:        map[string]string{"server": "10.0.0.1", "portalId": "7", "initiatorId": "9"},
			wantPortal:    7,
			wantInitiator: 9,
		},
		{
			name:          "unset ids resolve to the first available",
			params:        map[string]string{"server": "10.0.0.1"},
			wantPortal:    1,
			wantInitiator: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []tnsapi.ISCSITargetGroup
			mock := &MockAPIClientForSnapshots{
				CreateISCSITargetFunc: func(_ context.Context, p tnsapi.ISCSITargetCreateParams) (*tnsapi.ISCSITarget, error) {
					got = p.Groups
					return &tnsapi.ISCSITarget{ID: 5, Name: p.Name}, nil
				},
			}
			s := NewControllerService(mock, NewNodeRegistry(), "")
			req, dataset := adoptISCSIFixture()

			if _, err := s.adoptISCSIVolume(context.Background(), req, dataset, tt.params); err != nil {
				t.Fatalf("adoptISCSIVolume() error = %v", err)
			}
			if len(got) != 1 || got[0].Portal != tt.wantPortal || got[0].Initiator != tt.wantInitiator {
				t.Errorf("target groups = %+v, want portal %d initiator %d", got, tt.wantPortal, tt.wantInitiator)
			}
		})
	}
}

func TestAdoptISCSIVolume_RejectsInvalidPortalID(t *testing.T) {
	s := NewControllerService(&MockAPIClientForSnapshots{}, NewNodeRegistry(), "")
	req, dataset := adoptISCSIFixture()
	_, err := s.adoptISCSIVolume(context.Background(), req, dataset, map[string]string{"server": "10.0.0.1", "portalId": "abc"})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument (err=%v)", status.Code(err), err)
	}
}

func TestAdoptISCSIVolume_ExtentWithWrongDiskIsNotReused(t *testing.T) {
	var createdExtent, createdAssoc bool
	mock := &MockAPIClientForSnapshots{
		ISCSIExtentByNameFunc: func(_ context.Context, name string) (*tnsapi.ISCSIExtent, error) {
			return &tnsapi.ISCSIExtent{ID: 3, Name: name, Disk: "zvol/tank/other/data"}, nil
		},
		CreateISCSIExtentFunc: func(_ context.Context, _ tnsapi.ISCSIExtentCreateParams) (*tnsapi.ISCSIExtent, error) {
			createdExtent = true
			return &tnsapi.ISCSIExtent{ID: 4}, nil
		},
		CreateISCSITargetExtentFunc: func(_ context.Context, _ tnsapi.ISCSITargetExtentCreateParams) (*tnsapi.ISCSITargetExtent, error) {
			createdAssoc = true
			return &tnsapi.ISCSITargetExtent{ID: 1}, nil
		},
	}
	s := NewControllerService(mock, NewNodeRegistry(), "")
	req, dataset := adoptISCSIFixture()

	_, err := s.adoptISCSIVolume(context.Background(), req, dataset, map[string]string{"server": "10.0.0.1"})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("code = %v, want AlreadyExists (err=%v)", status.Code(err), err)
	}
	if createdExtent || createdAssoc {
		t.Errorf("created extent=%v association=%v, want neither", createdExtent, createdAssoc)
	}
}

func TestAdoptISCSIVolume_ReusesExtentWithMatchingDisk(t *testing.T) {
	var createdExtent bool
	mock := &MockAPIClientForSnapshots{
		ISCSIExtentByNameFunc: func(_ context.Context, name string) (*tnsapi.ISCSIExtent, error) {
			return &tnsapi.ISCSIExtent{ID: 3, Name: name, Disk: "zvol/tank/csi/pvc-adopt"}, nil
		},
		CreateISCSIExtentFunc: func(_ context.Context, _ tnsapi.ISCSIExtentCreateParams) (*tnsapi.ISCSIExtent, error) {
			createdExtent = true
			return &tnsapi.ISCSIExtent{ID: 4}, nil
		},
	}
	s := NewControllerService(mock, NewNodeRegistry(), "")
	req, dataset := adoptISCSIFixture()

	if _, err := s.adoptISCSIVolume(context.Background(), req, dataset, map[string]string{"server": "10.0.0.1"}); err != nil {
		t.Fatalf("adoptISCSIVolume() error = %v", err)
	}
	if createdExtent {
		t.Error("extent was recreated although a matching one exists")
	}
}

func TestAdoptISCSIVolume_AssociationAtWrongLUNIsRejected(t *testing.T) {
	var createdAssoc bool
	mock := &MockAPIClientForSnapshots{
		ISCSITargetByNameFunc: func(_ context.Context, name string) (*tnsapi.ISCSITarget, error) {
			return &tnsapi.ISCSITarget{ID: 5, Name: name}, nil
		},
		ISCSIExtentByNameFunc: func(_ context.Context, name string) (*tnsapi.ISCSIExtent, error) {
			return &tnsapi.ISCSIExtent{ID: 3, Name: name, Disk: "zvol/tank/csi/pvc-adopt"}, nil
		},
		ISCSITargetExtentByTargetFunc: func(_ context.Context, targetID int) ([]tnsapi.ISCSITargetExtent, error) {
			return []tnsapi.ISCSITargetExtent{{ID: 8, Target: targetID, Extent: 3, LunID: 2}}, nil
		},
		CreateISCSITargetExtentFunc: func(_ context.Context, _ tnsapi.ISCSITargetExtentCreateParams) (*tnsapi.ISCSITargetExtent, error) {
			createdAssoc = true
			return &tnsapi.ISCSITargetExtent{ID: 1}, nil
		},
	}
	s := NewControllerService(mock, NewNodeRegistry(), "")
	req, dataset := adoptISCSIFixture()

	_, err := s.adoptISCSIVolume(context.Background(), req, dataset, map[string]string{"server": "10.0.0.1"})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("code = %v, want AlreadyExists (err=%v)", status.Code(err), err)
	}
	if createdAssoc {
		t.Error("created an association despite the extent being mapped at LUN 2")
	}
}
