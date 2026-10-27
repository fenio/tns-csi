package driver

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/tnsapi"
)

const (
	nvmeRetryVolume   = "pvc-retry"
	nvmeRetryZvol     = "tank/nvme/" + nvmeRetryVolume
	nvmeRetryNQN      = defaultNQNPrefix + ":" + nvmeRetryVolume
	nvmeRetryFullNQN  = "nqn.2011-06.com.truenas:uuid:test:" + nvmeRetryNQN
	nvmeRetryCapacity = int64(1 << 30)
	nvmeRetryPortID   = 1
)

// NVMe-oF create stages, in the order createNVMeOFVolume performs them. A fake set up
// "at" a stage holds every object up to and including it, as an interrupted earlier
// attempt would have left them.
const (
	nvmeStageNone = iota
	nvmeStageZvol
	nvmeStageSubsystem
	nvmeStagePortBound
	nvmeStageNamespace
	nvmeStageProperties
)

// nvmeofRetryFake is a minimal stateful TrueNAS for one NVMe-oF volume.
type nvmeofRetryFake struct {
	zvol          *tnsapi.Dataset
	subsystem     *tnsapi.NVMeOFSubsystem
	namespace     *tnsapi.NVMeOFNamespace
	props         map[string]string
	creates       map[string]int
	namespaceFail func() error // when set, CreateNVMeOFNamespace returns its error
	portFail      error        // when set, AddSubsystemToPort binds and then returns it
	deletes       []string
	staleCtx      []string // deletes that ran on an already-canceled context
	mu            sync.Mutex
	bound         bool
}

func newNVMeOFRetryFake(stage int) *nvmeofRetryFake {
	f := &nvmeofRetryFake{props: map[string]string{}, creates: map[string]int{}}
	if stage >= nvmeStageZvol {
		f.zvol = nvmeRetryZvolDataset()
	}
	if stage >= nvmeStageSubsystem {
		f.subsystem = &tnsapi.NVMeOFSubsystem{ID: 100, Name: nvmeRetryNQN, NQN: nvmeRetryFullNQN}
	}
	if stage >= nvmeStagePortBound {
		f.bound = true
	}
	if stage >= nvmeStageNamespace {
		f.namespace = nvmeRetryNamespace()
	}
	if stage >= nvmeStageProperties {
		f.props[tnsapi.PropertyNVMeSubsystemID] = "100"
		f.props[tnsapi.PropertyNVMeNamespaceID] = "200"
		f.props[tnsapi.PropertyNVMeSubsystemNQN] = nvmeRetryFullNQN
	}
	return f
}

func nvmeRetryZvolDataset() *tnsapi.Dataset {
	return &tnsapi.Dataset{
		ID: nvmeRetryZvol, Name: nvmeRetryZvol, Type: datasetTypeVolume,
		Volsize: map[string]interface{}{"parsed": float64(nvmeRetryCapacity)},
	}
}

func nvmeRetryNamespace() *tnsapi.NVMeOFNamespace {
	return &tnsapi.NVMeOFNamespace{
		ID: 200, NSID: 1, Device: "zvol/" + nvmeRetryZvol,
		Subsys: &tnsapi.NVMeOFNamespaceSubsystem{ID: 100, Name: nvmeRetryNQN, SubNQN: nvmeRetryFullNQN},
	}
}

func (f *nvmeofRetryFake) deleted(ctx context.Context, what string) {
	f.deletes = append(f.deletes, what)
	if ctx.Err() != nil {
		f.staleCtx = append(f.staleCtx, what)
	}
}

func (f *nvmeofRetryFake) mock() *MockAPIClientForSnapshots {
	alreadyExists := func(what string) error { return fmt.Errorf("[EEXIST] %s already exists", what) }
	return &MockAPIClientForSnapshots{
		QueryAllDatasetsFunc: func(_ context.Context, prefix string) ([]tnsapi.Dataset, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.zvol != nil && f.zvol.ID == prefix {
				return []tnsapi.Dataset{*f.zvol}, nil
			}
			return nil, nil
		},
		CreateZvolFunc: func(_ context.Context, _ tnsapi.ZvolCreateParams) (*tnsapi.Dataset, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.creates["zvol"]++
			if f.zvol != nil {
				return nil, alreadyExists("dataset")
			}
			f.zvol = nvmeRetryZvolDataset()
			return f.zvol, nil
		},
		DeleteDatasetFunc: func(ctx context.Context, _ string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.deleted(ctx, "zvol")
			f.zvol = nil
			return nil
		},
		NVMeOFSubsystemByNQNFunc: func(_ context.Context, nqn string) (*tnsapi.NVMeOFSubsystem, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.subsystem != nil && f.subsystem.Name == nqn {
				s := *f.subsystem
				return &s, nil
			}
			return nil, fmt.Errorf("%w: NQN %s", tnsapi.ErrSubsystemNotFound, nqn)
		},
		QueryNVMeOFSubsystemFunc: func(_ context.Context, nqn string) ([]tnsapi.NVMeOFSubsystem, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.subsystem != nil && f.subsystem.Name == nqn {
				return []tnsapi.NVMeOFSubsystem{*f.subsystem}, nil
			}
			return nil, nil
		},
		CreateNVMeOFSubsystemFunc: func(_ context.Context, params tnsapi.NVMeOFSubsystemCreateParams) (*tnsapi.NVMeOFSubsystem, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.creates["subsystem"]++
			if f.subsystem != nil {
				return nil, alreadyExists("subsystem")
			}
			f.subsystem = &tnsapi.NVMeOFSubsystem{ID: 100, Name: params.Name, NQN: nvmeRetryFullNQN}
			s := *f.subsystem
			return &s, nil
		},
		DeleteNVMeOFSubsystemFunc: func(ctx context.Context, _ int) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.deleted(ctx, "subsystem")
			f.subsystem = nil
			return nil
		},
		QueryNVMeOFPortsFunc: func(context.Context) ([]tnsapi.NVMeOFPort, error) {
			return []tnsapi.NVMeOFPort{{ID: nvmeRetryPortID}}, nil
		},
		QuerySubsystemPortBindingsFunc: func(_ context.Context, subsystemID int) ([]tnsapi.NVMeOFPortSubsystem, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.bound && f.subsystem != nil && f.subsystem.ID == subsystemID {
				return []tnsapi.NVMeOFPortSubsystem{{ID: 7, PortID: nvmeRetryPortID, SubsystemID: subsystemID}}, nil
			}
			return nil, nil
		},
		AddSubsystemToPortFunc: func(_ context.Context, _, _ int) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.creates["port"]++
			if f.bound {
				return alreadyExists("port binding")
			}
			f.bound = true
			return f.portFail
		},
		RemoveSubsystemFromPortFunc: func(ctx context.Context, _ int) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.deleted(ctx, "port")
			f.bound = false
			return nil
		},
		QueryNVMeOFNamespacesFunc: func(context.Context, []interface{}) ([]tnsapi.NVMeOFNamespace, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.namespace != nil {
				return []tnsapi.NVMeOFNamespace{*f.namespace}, nil
			}
			return nil, nil
		},
		CreateNVMeOFNamespaceFunc: func(_ context.Context, _ tnsapi.NVMeOFNamespaceCreateParams) (*tnsapi.NVMeOFNamespace, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.creates["namespace"]++
			if f.namespaceFail != nil {
				return nil, f.namespaceFail()
			}
			if f.namespace != nil {
				return nil, alreadyExists("namespace")
			}
			f.namespace = nvmeRetryNamespace()
			ns := *f.namespace
			return &ns, nil
		},
		DeleteNVMeOFNamespaceFunc: func(ctx context.Context, _ int) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.deleted(ctx, "namespace")
			f.namespace = nil
			return nil
		},
		GetDatasetPropertiesFunc: func(_ context.Context, _ string, names []string) (map[string]string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			out := map[string]string{}
			for _, n := range names {
				if v, ok := f.props[n]; ok {
					out[n] = v
				}
			}
			return out, nil
		},
		SetDatasetPropertiesFunc: func(_ context.Context, _ string, props map[string]string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			for k, v := range props {
				f.props[k] = v
			}
			return nil
		},
	}
}

func nvmeRetryRequest() *csi.CreateVolumeRequest {
	return &csi.CreateVolumeRequest{
		Name:          nvmeRetryVolume,
		CapacityRange: &csi.CapacityRange{RequiredBytes: nvmeRetryCapacity},
		VolumeCapabilities: []*csi.VolumeCapability{{
			AccessType: &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
		}},
		Parameters: map[string]string{
			"protocol": ProtocolNVMeOF, "pool": "tank", "parentDataset": "nvme", "server": "192.168.1.100",
		},
	}
}

func nvmeRetryWantCreates(stage int) map[string]int {
	want := map[string]int{}
	for obj, at := range map[string]int{
		"zvol": nvmeStageZvol, "subsystem": nvmeStageSubsystem, "port": nvmeStagePortBound, "namespace": nvmeStageNamespace,
	} {
		if stage < at {
			want[obj] = 1
		}
	}
	return want
}

// The external-provisioner retries CreateVolume with the same name after a timeout. A
// retry must pick up whatever the interrupted attempt left on TrueNAS, create only what
// is missing, and return the same volume as an uninterrupted create.
func TestCreateNVMeOFVolumeConvergesOnRetry(t *testing.T) {
	t.Parallel()
	baselineFake := newNVMeOFRetryFake(nvmeStageNone)
	baseline, err := NewControllerService(baselineFake.mock(), NewNodeRegistry(), "").
		createNVMeOFVolume(context.Background(), nvmeRetryRequest())
	if err != nil {
		t.Fatalf("uninterrupted create failed: %v", err)
	}

	tests := []struct {
		name  string
		stage int
	}{
		{"after zvol", nvmeStageZvol},
		{"after subsystem", nvmeStageSubsystem},
		{"after port bind", nvmeStagePortBound},
		{"after namespace", nvmeStageNamespace},
		{"after properties", nvmeStageProperties},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newNVMeOFRetryFake(tt.stage)
			resp, err := NewControllerService(f.mock(), NewNodeRegistry(), "").
				createNVMeOFVolume(context.Background(), nvmeRetryRequest())
			if err != nil {
				t.Fatalf("retried create failed: %v", err)
			}
			if resp.GetVolume().GetVolumeId() != baseline.GetVolume().GetVolumeId() {
				t.Errorf("volume ID = %q, want %q", resp.GetVolume().GetVolumeId(), baseline.GetVolume().GetVolumeId())
			}
			if !reflect.DeepEqual(resp.GetVolume().GetVolumeContext(), baseline.GetVolume().GetVolumeContext()) {
				t.Errorf("volume context = %v, want %v", resp.GetVolume().GetVolumeContext(), baseline.GetVolume().GetVolumeContext())
			}
			if want := nvmeRetryWantCreates(tt.stage); !reflect.DeepEqual(f.creates, want) {
				t.Errorf("create calls = %v, want %v (objects already present must be reused)", f.creates, want)
			}
			if len(f.deletes) != 0 {
				t.Errorf("successful retry deleted %v", f.deletes)
			}
			if f.zvol == nil || f.subsystem == nil || !f.bound || f.namespace == nil {
				t.Errorf("volume incomplete after retry: zvol=%v subsystem=%v bound=%v namespace=%v",
					f.zvol != nil, f.subsystem != nil, f.bound, f.namespace != nil)
			}
		})
	}
}

// When the namespace create fails (typically because the request deadline expired),
// cleanup must remove only what this call created, port binding before subsystem, and
// must not depend on the request context still being alive.
func TestCreateNVMeOFVolumeCleanupRemovesOnlyWhatItCreated(t *testing.T) {
	tests := []struct {
		name        string
		wantDeletes []string
		stage       int
	}{
		{"fresh create", []string{"port", "subsystem", "zvol"}, nvmeStageNone},
		{"zvol pre-existing", []string{"port", "subsystem"}, nvmeStageZvol},
		{"subsystem pre-existing", []string{"port"}, nvmeStageSubsystem},
		{"subsystem and port pre-existing", nil, nvmeStagePortBound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f := newNVMeOFRetryFake(tt.stage)
			f.namespaceFail = func() error {
				cancel() // the request deadline expires while the namespace is being created
				return errors.New("context deadline exceeded")
			}
			_, err := NewControllerService(f.mock(), NewNodeRegistry(), "").createNVMeOFVolume(ctx, nvmeRetryRequest())
			if err == nil {
				t.Fatal("create succeeded although the namespace create failed")
			}
			if !reflect.DeepEqual(f.deletes, tt.wantDeletes) {
				t.Errorf("cleanup deleted %v, want %v", f.deletes, tt.wantDeletes)
			}
			if len(f.staleCtx) != 0 {
				t.Errorf("cleanup of %v ran on the canceled request context", f.staleCtx)
			}
		})
	}
}

// A port bind that times out on the client may still have been made on TrueNAS. A
// subsystem this call created is entirely its own, so cleanup unbinds it before deleting.
func TestCreateNVMeOFVolumeCleanupUnbindsTimedOutBind(t *testing.T) {
	f := newNVMeOFRetryFake(nvmeStageZvol)
	f.portFail = errors.New("context deadline exceeded")
	_, err := NewControllerService(f.mock(), NewNodeRegistry(), "").createNVMeOFVolume(context.Background(), nvmeRetryRequest())
	if err == nil {
		t.Fatal("create succeeded although the port bind failed")
	}
	if want := []string{"port", "subsystem"}; !reflect.DeepEqual(f.deletes, want) {
		t.Errorf("cleanup deleted %v, want %v", f.deletes, want)
	}
}

// The clone path builds the same presentation on top of a freshly cloned ZVOL; a
// subsystem, port binding or namespace left by an earlier attempt must be reused.
func TestSetupNVMeOFVolumeFromCloneConvergesOnRetry(t *testing.T) {
	t.Parallel()
	info := &cloneInfo{Mode: tnsapi.CloneModeCOW, SnapshotID: "snap", OriginSnapshot: "tank/src@snap"}

	tests := []struct {
		name  string
		stage int
	}{
		{"nothing left over", nvmeStageZvol},
		{"subsystem left over", nvmeStageSubsystem},
		{"port binding left over", nvmeStagePortBound},
		{"namespace left over", nvmeStageNamespace},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newNVMeOFRetryFake(tt.stage)
			resp, err := NewControllerService(f.mock(), NewNodeRegistry(), "").setupNVMeOFVolumeFromClone(
				context.Background(), nvmeRetryRequest(), nvmeRetryZvolDataset(), "192.168.1.100", "", info)
			if err != nil {
				t.Fatalf("clone setup failed: %v", err)
			}
			if got := resp.GetVolume().GetVolumeContext()["nvmeofSubsystemID"]; got != "100" {
				t.Errorf("subsystem ID in context = %q, want 100", got)
			}
			if got := resp.GetVolume().GetVolumeContext()["nvmeofNamespaceID"]; got != "200" {
				t.Errorf("namespace ID in context = %q, want 200", got)
			}
			want := nvmeRetryWantCreates(tt.stage)
			delete(want, "zvol") // the clone itself is made by the caller
			if !reflect.DeepEqual(f.creates, want) {
				t.Errorf("create calls = %v, want %v (objects already present must be reused)", f.creates, want)
			}
			if len(f.deletes) != 0 {
				t.Errorf("successful clone setup deleted %v", f.deletes)
			}
		})
	}
}

func TestSetupNVMeOFVolumeFromCloneCleanupKeepsReusedSubsystem(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := newNVMeOFRetryFake(nvmeStageSubsystem)
	f.namespaceFail = func() error {
		cancel()
		return errors.New("context deadline exceeded")
	}
	info := &cloneInfo{Mode: tnsapi.CloneModeCOW, SnapshotID: "snap"}
	_, err := NewControllerService(f.mock(), NewNodeRegistry(), "").setupNVMeOFVolumeFromClone(
		ctx, nvmeRetryRequest(), nvmeRetryZvolDataset(), "192.168.1.100", "", info)
	if err == nil {
		t.Fatal("clone setup succeeded although the namespace create failed")
	}
	// The clone ZVOL was made by this CreateVolume call; the subsystem was reused.
	if want := []string{"port", "zvol"}; !reflect.DeepEqual(f.deletes, want) {
		t.Errorf("cleanup deleted %v, want %v", f.deletes, want)
	}
	if len(f.staleCtx) != 0 {
		t.Errorf("cleanup of %v ran on the canceled request context", f.staleCtx)
	}
}
