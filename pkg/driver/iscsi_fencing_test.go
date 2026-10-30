package driver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fencingAPIClient keeps one iSCSI target and the initiator groups in memory.
type fencingAPIClient struct {
	*mockAPIClient
	initiators []tnsapi.ISCSIInitiator
	target     tnsapi.ISCSITarget
	updates    int
}

func (m *fencingAPIClient) QueryISCSIInitiators(_ context.Context) ([]tnsapi.ISCSIInitiator, error) {
	return slices.Clone(m.initiators), nil
}

func (m *fencingAPIClient) CreateISCSIInitiator(_ context.Context, initiators []string, comment string) (*tnsapi.ISCSIInitiator, error) {
	g := tnsapi.ISCSIInitiator{ID: len(m.initiators) + 1, Initiators: initiators, Comment: comment}
	m.initiators = append(m.initiators, g)
	return &g, nil
}

func (m *fencingAPIClient) ISCSITargetByName(_ context.Context, name string) (*tnsapi.ISCSITarget, error) {
	if name != m.target.Name {
		return nil, fmt.Errorf("unexpected target name %q", name)
	}
	t := m.target
	return &t, nil
}

func (m *fencingAPIClient) UpdateISCSITarget(_ context.Context, id int, groups []tnsapi.ISCSITargetGroup) (*tnsapi.ISCSITarget, error) {
	m.target.Groups = groups
	m.updates++
	return &m.target, nil
}

func newFencingController(protocol string) (*ControllerService, *fencingAPIClient) {
	client := &fencingAPIClient{
		mockAPIClient: &mockAPIClient{getDatasetWithPropertiesFunc: func(_ context.Context, id string) (*tnsapi.DatasetWithProperties, error) {
			return &tnsapi.DatasetWithProperties{
				Dataset: tnsapi.Dataset{ID: id},
				UserProperties: map[string]tnsapi.UserProperty{
					tnsapi.PropertyManagedBy: {Value: tnsapi.ManagedByValue},
					tnsapi.PropertyProtocol:  {Value: protocol},
					tnsapi.PropertyISCSIIQN:  {Value: "iqn.2005-10.org.freenas.ctl:pvc-1"},
				},
			}, nil
		}},
		target: tnsapi.ISCSITarget{ID: 7, Name: "pvc-1", Groups: []tnsapi.ISCSITargetGroup{{Portal: 3, Initiator: 1, AuthMethod: "CHAP"}}},
		initiators: []tnsapi.ISCSIInitiator{
			{ID: 1, Initiators: []string{}}, // TrueNAS's allow-all group
			{ID: 2, Initiators: []string{"iqn.a"}, Comment: "tns-csi:node:node-a"},
			{ID: 3, Initiators: []string{"iqn.b"}, Comment: "tns-csi:node:node-b"},
			{ID: 4, Initiators: []string{"iqn.c"}, Comment: "tns-csi:other:node:node-c"}, // another cluster
		},
	}
	s := NewControllerService(client, nil, "")
	s.iscsiFencing = true
	return s, client
}

func publishTo(t *testing.T, s *ControllerService, node string, mode csi.VolumeCapability_AccessMode_Mode) error {
	t.Helper()
	_, err := s.ControllerPublishVolume(context.Background(), &csi.ControllerPublishVolumeRequest{
		VolumeId: "pool/pvc-1",
		NodeId:   node,
		VolumeCapability: &csi.VolumeCapability{
			AccessType: &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: mode},
		},
	})
	return err
}

func unpublishFrom(t *testing.T, s *ControllerService, node string) {
	t.Helper()
	if _, err := s.ControllerUnpublishVolume(context.Background(), &csi.ControllerUnpublishVolumeRequest{VolumeId: "pool/pvc-1", NodeId: node}); err != nil {
		t.Fatalf("unpublish from %q: %v", node, err)
	}
}

func assertInitiators(t *testing.T, c *fencingAPIClient, want ...int) {
	t.Helper()
	if got := groupInitiators(c.target.Groups); !slices.Equal(got, want) {
		t.Fatalf("target admits initiator groups %v, want %v", got, want)
	}
	for _, g := range c.target.Groups {
		if g.Portal != 3 || g.AuthMethod != "CHAP" {
			t.Fatalf("group %+v lost the target's portal or auth", g)
		}
	}
}

func TestISCSIFencingSingleNode(t *testing.T) {
	s, c := newFencingController(ProtocolISCSI)
	single := csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER

	if err := publishTo(t, s, "node-a", single); err != nil {
		t.Fatal(err)
	}
	assertInitiators(t, c, 2) // the allow-all group is gone

	unpublishFrom(t, s, "node-a")
	fenced := c.initiators[len(c.initiators)-1]
	if fenced.Comment != fencedGroupComment || !slices.Equal(fenced.Initiators, []string{fencedInitiatorIQN}) {
		t.Fatalf("fenced group = %+v", fenced)
	}
	assertInitiators(t, c, fenced.ID)

	unpublishFrom(t, s, "node-a") // idempotent: no further update
	if c.updates != 2 {
		t.Fatalf("got %d target updates, want 2", c.updates)
	}

	if err := publishTo(t, s, "node-b", single); err != nil {
		t.Fatal(err)
	}
	assertInitiators(t, c, 3)
}

func TestISCSIFencingMultiNode(t *testing.T) {
	s, c := newFencingController(ProtocolISCSI)
	multi := csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER

	for _, node := range []string{"node-a", "node-b"} {
		if err := publishTo(t, s, node, multi); err != nil {
			t.Fatal(err)
		}
	}
	assertInitiators(t, c, 2, 3)

	unpublishFrom(t, s, "node-a")
	assertInitiators(t, c, 3)

	if err := publishTo(t, s, "node-a", multi); err != nil {
		t.Fatal(err)
	}
	unpublishFrom(t, s, "") // every node
	assertInitiators(t, c, len(c.initiators))
}

func TestISCSIFencingRefusesUnsafePublish(t *testing.T) {
	s, c := newFencingController(ProtocolISCSI)
	single := csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER

	// node-c is registered by another cluster only.
	if err := publishTo(t, s, "node-c", single); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unregistered node: got %v, want FailedPrecondition", err)
	}

	c.initiators[2].Initiators = []string{"iqn.a"} // node-b cloned from node-a
	if err := publishTo(t, s, "node-a", single); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("duplicate IQN: got %v, want FailedPrecondition", err)
	}
	if c.updates != 0 {
		t.Fatalf("target was updated %d times by refused publishes", c.updates)
	}
}

func TestISCSIFencingIgnoresOtherProtocols(t *testing.T) {
	s, c := newFencingController(ProtocolNVMeOF)
	if err := publishTo(t, s, "node-a", csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER); err != nil {
		t.Fatal(err)
	}
	unpublishFrom(t, s, "node-a")
	if c.updates != 0 {
		t.Fatalf("NVMe-oF volume changed an iSCSI target %d times", c.updates)
	}
}

func TestReadInitiatorName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "initiatorname.iscsi")
	content := "## generated by open-iscsi\n  InitiatorName=iqn.2004-10.com.ubuntu:01:abc\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if iqn, err := readInitiatorName(path); err != nil || iqn != "iqn.2004-10.com.ubuntu:01:abc" {
		t.Fatalf("got %q, %v", iqn, err)
	}
	if err := os.WriteFile(path, []byte("# empty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readInitiatorName(path); err == nil {
		t.Fatal("want an error for a file without InitiatorName")
	}
}
