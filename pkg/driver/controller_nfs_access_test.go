package driver

import (
	"context"
	"slices"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/metrics"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestParseNFSShareAccess(t *testing.T) {
	tests := []struct {
		params       map[string]string
		name         string
		wantHosts    []string
		wantNetworks []string
		wantError    bool
	}{
		{name: "unset allows existing behavior"},
		{
			name: "trims and deduplicates hosts and canonicalizes networks",
			params: map[string]string{
				"nfsHosts":    " node1.example.com,192.0.2.6, node1.example.com,2001:db8::6 ",
				"nfsNetworks": " 10.10.20.1/24 , 2001:db8:1::1/64,10.10.20.0/24 ",
			},
			wantHosts:    []string{"node1.example.com", "192.0.2.6", "2001:db8::6"},
			wantNetworks: []string{"10.10.20.0/24", "2001:db8:1::/64"},
		},
		{name: "empty host entry", params: map[string]string{"nfsHosts": "node1,,node2"}, wantError: true},
		{name: "trailing network comma", params: map[string]string{"nfsNetworks": "10.0.0.0/8,"}, wantError: true},
		{name: "whitespace only", params: map[string]string{"nfsHosts": "  "}, wantError: true},
		{name: "host with CIDR suffix", params: map[string]string{"nfsHosts": "192.0.2.6/32"}, wantError: true},
		{name: "invalid IPv4 address", params: map[string]string{"nfsHosts": "192.168.1.999"}, wantError: true},
		{name: "invalid hostname", params: map[string]string{"nfsHosts": "-node.example.com"}, wantError: true},
		{name: "network without prefix", params: map[string]string{"nfsNetworks": "192.0.2.6"}, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseNFSShareAccess(tt.params)
			if tt.wantError {
				if status.Code(err) != codes.InvalidArgument {
					t.Fatalf("parseNFSShareAccess() error = %v, want InvalidArgument", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got.hosts, tt.wantHosts) || !slices.Equal(got.networks, tt.wantNetworks) {
				t.Fatalf("parseNFSShareAccess() = hosts %v, networks %v; want %v, %v", got.hosts, got.networks, tt.wantHosts, tt.wantNetworks)
			}
		})
	}
}

func TestNFSShareCreationPassesAccessLists(t *testing.T) {
	const (
		datasetID = "tank/csi/pvc-1"
		sharePath = "/mnt/tank/csi/pvc-1"
	)
	request := &csi.CreateVolumeRequest{
		Name: "pvc-1",
		Parameters: map[string]string{
			"protocol": "nfs", "pool": "tank", "server": "truenas.example", "parentDataset": "csi",
			"nfsHosts": "node1.example.com,192.0.2.6", "nfsNetworks": "10.10.20.0/24",
		},
	}
	dataset := &tnsapi.Dataset{ID: datasetID, Name: datasetID, Mountpoint: sharePath}
	//nolint:govet // Field alignment is not relevant for a small test table.
	for _, tt := range []struct {
		name string
		call func(*ControllerService) error
	}{
		{name: "new volume", call: func(s *ControllerService) error {
			_, err := s.createNFSVolume(context.Background(), request)
			return err
		}},
		{name: "snapshot clone", call: func(s *ControllerService) error {
			_, err := s.setupNFSVolumeFromClone(context.Background(), request, dataset, "truenas.example", &cloneInfo{})
			return err
		}},
		{name: "adoption creating a share", call: func(s *ControllerService) error {
			_, err := s.adoptNFSVolume(context.Background(), request, &tnsapi.DatasetWithProperties{Dataset: *dataset}, request.Parameters)
			return err
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var created *tnsapi.NFSShareCreateParams
			mock := &MockAPIClientForSnapshots{
				QueryAllDatasetsFunc: func(context.Context, string) ([]tnsapi.Dataset, error) { return nil, nil },
				CreateDatasetFunc:    func(context.Context, tnsapi.DatasetCreateParams) (*tnsapi.Dataset, error) { return dataset, nil },
				QueryNFSShareFunc:    func(context.Context, string) ([]tnsapi.NFSShare, error) { return nil, nil },
				CreateNFSShareFunc: func(_ context.Context, params tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error) {
					created = &params
					return &tnsapi.NFSShare{ID: 1, Path: params.Path, Hosts: params.Hosts, Networks: params.Networks}, nil
				},
			}
			controller := NewControllerService(mock, NewNodeRegistry(), "")
			if err := tt.call(controller); err != nil {
				t.Fatal(err)
			}
			if created == nil {
				t.Fatal("CreateNFSShare was not called")
			}
			matchHosts := slices.Equal(created.Hosts, []string{"node1.example.com", "192.0.2.6"})
			matchNetworks := slices.Equal(created.Networks, []string{"10.10.20.0/24"})
			if !matchHosts || !matchNetworks {
				t.Fatalf("CreateNFSShare access lists = %+v", created)
			}
		})
	}
}

func TestExistingNFSShareAccessFailsClosed(t *testing.T) {
	access := nfsShareAccess{hosts: []string{"node1.example.com", "node2.example.com"}, networks: []string{"10.10.20.0/24"}}
	for _, tt := range []struct {
		name  string
		share tnsapi.NFSShare
		want  codes.Code
	}{
		{name: "unrestricted share", share: tnsapi.NFSShare{ID: 1, Path: "/mnt/tank/pvc"}, want: codes.FailedPrecondition},
		{name: "other host", share: tnsapi.NFSShare{ID: 1, Path: "/mnt/tank/pvc", Hosts: []string{"other.example.com"}, Networks: access.networks}, want: codes.FailedPrecondition},
		{name: "other network", share: tnsapi.NFSShare{ID: 1, Path: "/mnt/tank/pvc", Hosts: access.hosts, Networks: []string{"0.0.0.0/0"}}, want: codes.FailedPrecondition},
		{name: "same restrictions in different order", share: tnsapi.NFSShare{ID: 1, Path: "/mnt/tank/pvc", Hosts: []string{"node2.example.com", "node1.example.com"}, Networks: access.networks}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := status.Code(checkExistingNFSShareAccess(access, &tt.share)); got != tt.want {
				t.Fatalf("checkExistingNFSShareAccess() code = %v, want %v", got, tt.want)
			}
		})
	}
	if err := checkExistingNFSShareAccess(nfsShareAccess{}, &tnsapi.NFSShare{ID: 1}); err != nil {
		t.Fatalf("unconfigured access lists must preserve existing behavior: %v", err)
	}
}

func TestExistingNFSVolumeRejectsUnrestrictedShare(t *testing.T) {
	request := &csi.CreateVolumeRequest{Name: "pvc-1", Parameters: map[string]string{"pool": "tank", "nfsNetworks": "10.10.20.0/24"}}
	dataset := &tnsapi.Dataset{ID: "tank/pvc-1", Name: "tank/pvc-1", Mountpoint: "/mnt/tank/pvc-1"}
	mock := &MockAPIClientForSnapshots{
		QueryNFSShareFunc: func(context.Context, string) ([]tnsapi.NFSShare, error) {
			return []tnsapi.NFSShare{{ID: 1, Path: dataset.Mountpoint}}, nil
		},
	}
	controller := NewControllerService(mock, NewNodeRegistry(), "")
	_, _, err := controller.checkExistingNFSVolume(context.Background(), request, request.Parameters, dataset, dataset.ID, 1<<30)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("checkExistingNFSVolume() error = %v, want FailedPrecondition", err)
	}
	// Legacy idempotency checks must not bypass the access comparison either.
	params, err := validateNFSParams(request)
	if err != nil {
		t.Fatal(err)
	}
	mock.QueryAllNFSSharesFunc = func(context.Context, string) ([]tnsapi.NFSShare, error) {
		return []tnsapi.NFSShare{{ID: 1, Path: dataset.Mountpoint}}, nil
	}
	_, _, err = controller.handleExistingNFSVolume(context.Background(), params, dataset, metrics.NewVolumeOperationTimer(metrics.ProtocolNFS, "create"))
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("handleExistingNFSVolume() error = %v, want FailedPrecondition", err)
	}
}

func TestNFSAccessValidationPrecedesVolumeLookup(t *testing.T) {
	request := &csi.CreateVolumeRequest{
		Name:       "pvc-1",
		Parameters: map[string]string{"protocol": "nfs", "pool": "tank", "nfsNetworks": "bad-network"},
		VolumeCapabilities: []*csi.VolumeCapability{{
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER},
		}},
	}
	// A nil API client would panic if validation were postponed until after the
	// existing-volume lookup or the snapshot/adoption provisioning paths.
	controller := NewControllerService(nil, NewNodeRegistry(), "")
	_, err := controller.CreateVolume(context.Background(), request)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("CreateVolume() error = %v, want InvalidArgument", err)
	}
}

func TestNFSAccessRejectedForOtherProtocols(t *testing.T) {
	request := &csi.CreateVolumeRequest{
		Name:       "pvc-1",
		Parameters: map[string]string{"protocol": "nvmeof", "pool": "tank", "nfsHosts": "node1.example.com"},
		VolumeCapabilities: []*csi.VolumeCapability{{
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{FsType: "ext4"}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
		}},
	}
	controller := NewControllerService(nil, NewNodeRegistry(), "")
	_, err := controller.CreateVolume(context.Background(), request)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("CreateVolume() error = %v, want InvalidArgument", err)
	}
}

func TestAdoptNFSVolumeRejectsUnrestrictedExistingShare(t *testing.T) {
	dataset := &tnsapi.DatasetWithProperties{Dataset: tnsapi.Dataset{ID: "tank/pvc-1", Name: "tank/pvc-1", Mountpoint: "/mnt/tank/pvc-1"}}
	request := &csi.CreateVolumeRequest{Name: "pvc-1", Parameters: map[string]string{"pool": "tank", "nfsHosts": "node1.example.com"}}
	mock := &MockAPIClientForSnapshots{
		QueryNFSShareFunc: func(context.Context, string) ([]tnsapi.NFSShare, error) {
			return []tnsapi.NFSShare{{ID: 1, Path: dataset.Mountpoint}}, nil
		},
		CreateNFSShareFunc: func(context.Context, tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error) {
			t.Fatal("adoption must not create a second NFS share")
			return &tnsapi.NFSShare{}, nil
		},
	}
	controller := NewControllerService(mock, NewNodeRegistry(), "")
	_, err := controller.adoptNFSVolume(context.Background(), request, dataset, request.Parameters)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("adoptNFSVolume() error = %v, want FailedPrecondition", err)
	}
}
