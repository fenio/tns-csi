package driver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/fenio/tns-csi/pkg/tnsapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// iSCSI fencing (opt-in): each node registers an initiator group holding its own IQN,
// publish restricts the volume's target to the publishing node's group and unpublish
// removes it again. A target published to no node points at fencedGroupComment, whose
// only initiator never exists. An empty initiator list would mean "allow all" on TrueNAS.
const (
	fencedGroupComment = "tns-csi:fenced"
	fencedInitiatorIQN = "iqn.2005-10.invalid.tns-csi:fenced"
	initiatorNameFile  = "/etc/iscsi/initiatorname.iscsi"
)

var errNoInitiatorName = errors.New("no InitiatorName line")

// nodeGroupPrefix is the comment prefix of this cluster's per-node initiator groups.
func nodeGroupPrefix(clusterID string) string {
	if clusterID == "" {
		return "tns-csi:node:"
	}
	return "tns-csi:" + clusterID + ":node:"
}

// readInitiatorName returns the IQN from an open-iscsi initiatorname file.
func readInitiatorName(path string) (string, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if iqn, ok := strings.CutPrefix(strings.TrimSpace(line), "InitiatorName="); ok && iqn != "" {
			return iqn, nil
		}
	}
	return "", fmt.Errorf("%s: %w", path, errNoInitiatorName)
}

// registerISCSIInitiator creates or updates this node's initiator group on TrueNAS.
// A node without open-iscsi has nothing to register.
func (s *NodeService) registerISCSIInitiator(ctx context.Context) error {
	iqn, err := readInitiatorName(initiatorNameFile)
	if os.IsNotExist(err) {
		klog.Warningf("iSCSI fencing: %s not found, node %s cannot use iSCSI volumes", initiatorNameFile, s.nodeID)
		return nil
	}
	if err != nil {
		return err
	}
	groups, err := s.apiClient.QueryISCSIInitiators(ctx)
	if err != nil {
		return err
	}
	comment := nodeGroupPrefix(s.clusterID) + s.nodeID
	for _, g := range groups {
		if g.Comment != comment {
			continue
		}
		if slices.Equal(g.Initiators, []string{iqn}) {
			return nil
		}
		_, err = s.apiClient.UpdateISCSIInitiator(ctx, g.ID, []string{iqn})
		klog.Infof("iSCSI fencing: updated initiator group %d of node %s to %s", g.ID, s.nodeID, iqn)
		return err
	}
	g, err := s.apiClient.CreateISCSIInitiator(ctx, []string{iqn}, comment)
	if err != nil {
		return err
	}
	klog.Infof("iSCSI fencing: registered initiator group %d for node %s (%s)", g.ID, s.nodeID, iqn)
	return nil
}

// fenceISCSIVolume restricts the iSCSI target of volumeID to nodeID (publish) or removes
// nodeID from it (unpublish). Non-iSCSI and unknown volumes are left alone.
func (s *ControllerService) fenceISCSIVolume(ctx context.Context, volumeID, nodeID string, publish, multiNode bool) error {
	meta, err := s.lookupVolumeByCSIName(ctx, "", volumeID)
	if err != nil {
		return status.Errorf(codes.Internal, "iSCSI fencing: failed to look up volume %s: %v", volumeID, err)
	}
	if meta == nil || meta.Protocol != ProtocolISCSI {
		return nil
	}
	targetName := presentationVolumeName(meta)
	if i := strings.LastIndex(meta.ISCSIIQN, ":"); i != -1 {
		targetName = meta.ISCSIIQN[i+1:]
	}

	unlock, err := s.fencingLocks.lock(ctx, targetName)
	if err != nil {
		return status.FromContextError(err).Err()
	}
	defer unlock()

	target, err := s.apiClient.ISCSITargetByName(ctx, targetName)
	if err != nil {
		return status.Errorf(codes.Internal, "iSCSI fencing: failed to look up target %s: %v", targetName, err)
	}
	if target == nil {
		if publish {
			return status.Errorf(codes.NotFound, "iSCSI fencing: target %s of volume %s not found", targetName, volumeID)
		}
		return nil
	}

	initiators, err := s.apiClient.QueryISCSIInitiators(ctx)
	if err != nil {
		return status.Errorf(codes.Internal, "iSCSI fencing: failed to query initiator groups: %v", err)
	}
	nodeGroups := map[int]tnsapi.ISCSIInitiator{}
	prefix := nodeGroupPrefix(s.clusterID)
	var own *tnsapi.ISCSIInitiator
	for i := range initiators {
		if !strings.HasPrefix(initiators[i].Comment, prefix) {
			continue
		}
		nodeGroups[initiators[i].ID] = initiators[i]
		if initiators[i].Comment == prefix+nodeID {
			own = &initiators[i]
		}
	}
	if !publish && nodeID == "" {
		nodeGroups = nil // unpublished from every node: keep no node's access
	}
	if publish {
		if own == nil {
			return status.Errorf(codes.FailedPrecondition,
				"iSCSI fencing: node %s has not registered its iSCSI initiator on TrueNAS; check its node plugin log", nodeID)
		}
		if other := sharedIQNNode(*own, nodeGroups, prefix); other != "" {
			return status.Errorf(codes.FailedPrecondition,
				"iSCSI fencing: nodes %s and %s report the same iSCSI initiator name %v; give each node a unique IQN",
				nodeID, other, own.Initiators)
		}
	}

	groups := fencedTargetGroups(target.Groups, nodeGroups, own, publish, multiNode)
	if len(groups) == 0 {
		fenced, err := s.fencedInitiatorGroup(ctx, initiators)
		if err != nil {
			return status.Errorf(codes.Internal, "iSCSI fencing: %v", err)
		}
		groups = []tnsapi.ISCSITargetGroup{targetGroupFor(target.Groups, fenced)}
	}
	if slices.Equal(groups, target.Groups) {
		return nil
	}
	if _, err := s.apiClient.UpdateISCSITarget(ctx, target.ID, groups); err != nil {
		return status.Errorf(codes.Internal, "iSCSI fencing: failed to update target %s: %v", targetName, err)
	}
	klog.Infof("iSCSI fencing: target %s now admits initiator groups %v (publish=%v, node %s)",
		targetName, groupInitiators(groups), publish, nodeID)
	return nil
}

// fencedTargetGroups returns the target's new groups: only other nodes' groups are kept
// (and only for a multi-node publish or an unpublish), so legacy shared groups such as
// TrueNAS's allow-all group are dropped. An empty result means nobody may log in.
func fencedTargetGroups(current []tnsapi.ISCSITargetGroup, nodeGroups map[int]tnsapi.ISCSIInitiator, own *tnsapi.ISCSIInitiator, publish, multiNode bool) []tnsapi.ISCSITargetGroup {
	var groups []tnsapi.ISCSITargetGroup
	if !publish || multiNode {
		for _, g := range current {
			if _, isNode := nodeGroups[g.Initiator]; isNode && (own == nil || g.Initiator != own.ID) {
				groups = append(groups, g)
			}
		}
	}
	if publish {
		groups = append(groups, targetGroupFor(current, own.ID))
	}
	return groups
}

// targetGroupFor builds a group for initiator group id, keeping the target's portal and auth.
func targetGroupFor(current []tnsapi.ISCSITargetGroup, id int) tnsapi.ISCSITargetGroup {
	// shortcut: assumes portal 1 for a target without groups, which the driver never creates.
	g := tnsapi.ISCSITargetGroup{Portal: 1, Initiator: id}
	if len(current) > 0 {
		g = current[0]
		g.Initiator = id
	}
	return g
}

// sharedIQNNode returns another node whose group contains one of own's IQNs.
func sharedIQNNode(own tnsapi.ISCSIInitiator, nodeGroups map[int]tnsapi.ISCSIInitiator, prefix string) string {
	for _, g := range nodeGroups {
		if g.ID == own.ID {
			continue
		}
		for _, iqn := range g.Initiators {
			if slices.Contains(own.Initiators, iqn) {
				return strings.TrimPrefix(g.Comment, prefix)
			}
		}
	}
	return ""
}

// fencedInitiatorGroup returns the ID of the group that admits no real initiator, creating it if needed.
func (s *ControllerService) fencedInitiatorGroup(ctx context.Context, initiators []tnsapi.ISCSIInitiator) (int, error) {
	for _, g := range initiators {
		if g.Comment == fencedGroupComment {
			return g.ID, nil
		}
	}
	g, err := s.apiClient.CreateISCSIInitiator(ctx, []string{fencedInitiatorIQN}, fencedGroupComment)
	if err != nil {
		return 0, fmt.Errorf("failed to create the fenced initiator group: %w", err)
	}
	return g.ID, nil
}

func groupInitiators(groups []tnsapi.ISCSITargetGroup) []int {
	ids := make([]int, 0, len(groups))
	for _, g := range groups {
		ids = append(ids, g.Initiator)
	}
	return ids
}
