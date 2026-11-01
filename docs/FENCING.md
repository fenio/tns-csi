# iSCSI Fencing

With fencing on, an iSCSI volume can only be reached on TrueNAS by the nodes it is currently published to. When Kubernetes detaches a volume from a node, the driver revokes that node's access on TrueNAS. That holds even when the node is unreachable. The node can no longer log in to the volume's target, and TrueNAS rejects I/O over a session the node still holds. This is what makes it safe to move a volume away from a node that is partitioned rather than powered off ([#299](https://github.com/fenio/tns-csi/issues/299)).

Fencing is opt-in and covers iSCSI volumes only.

## Requirements

- `csiDriver.attachRequired: true` (the default since v0.21.0). Fencing runs in `ControllerPublishVolume`/`ControllerUnpublishVolume`, which Kubernetes only calls when attach is required. The chart refuses `fencing.enabled` without it.
- A unique iSCSI initiator name (IQN) on every node, in `/etc/iscsi/initiatorname.iscsi`. Nodes cloned from one disk image often share one. Regenerate it (for example with `iscsi-iname`) and restart `iscsid`. The driver refuses to publish a volume to a node whose IQN another node also reports.

## Enabling

```yaml
fencing:
  enabled: true
```

## How it works

- **Node registration:** at startup, each node plugin creates (or updates) an iSCSI initiator group on TrueNAS holding its own IQN, with the comment `tns-csi:node:<node>`, or `tns-csi:<clusterID>:node:<node>` when `clusterID` is set. If that fails (e.g. TrueNAS is unreachable), the node plugin's registration with the kubelet fails and node-driver-registrar restarts until it succeeds, so the node takes no volumes of any protocol until then.
- **Publish:** the volume's target is restricted to the publishing node's group. Any other group it used is removed, including TrueNAS's default allow-all group. For a multi-node raw block volume (e.g. KubeVirt live migration) the node is added to the nodes already admitted.
- **Unpublish:** the node's group is removed from the target. A target that admits no node points at the group `tns-csi:fenced`, whose only initiator (`iqn.2005-10.invalid.tns-csi:fenced`) never exists. An empty initiator group means "allow all" on TrueNAS, so it can't be used for this.
- **New volumes** are created fenced and admit their node only when they are first published.
- **Existing volumes** keep their current access until they are next published, i.e. until their pod next moves or restarts after a full unstage. Until then they are not fenced.

With fencing on, the driver manages each target's initiator groups itself. An `initiatorId` StorageClass parameter only applies until the volume is first published.

## What a fenced node sees

TrueNAS keeps the old node's session logged in, but rejects every I/O on it (`ILLEGAL REQUEST`, `LOGICAL UNIT NOT SUPPORTED`). On the node, ext4 aborts its journal and remounts read-only within about a second. If the node's access is restored later, the filesystem stays read-only: the volume has to be unmounted and staged again before the node can write to it.

## Nodes that crash or become unreachable

A pod on an unresponsive node stays `Terminating`, and its volumes stay attached, so the replacement waits with a `Multi-Attach error` (see [DEPLOYMENT.md](DEPLOYMENT.md#nodes-that-crash-or-become-unreachable)). Adding the out-of-service taint lets Kubernetes delete the old pods and detach their volumes:

```bash
kubectl taint node <node> node.kubernetes.io/out-of-service=nodeshutdown:NoExecute
```

The detach unpublishes the volume from the old node, so with fencing on, its iSCSI volumes are cut off on TrueNAS before they are attached anywhere else. That makes the taint safe for iSCSI volumes even when the node may still be running. You can apply it automatically, for example once a node has been `NotReady` for a fixed time. Remove the taint once the node is healthy again.

Fencing doesn't protect the following, so check them before automating the taint:

- **NVMe-oF volumes** are not fenced. TrueNAS can stop a host from connecting again, but not one that is already connected.
- **NFS and SMB volumes** are not fenced.
- **Volumes that haven't been published since fencing was enabled.**

## Defence in depth: ext4 MMP

ext4 multi-mount protection makes the kernel refuse a second read-write mount while another node is still updating the filesystem. It covers mounts that fencing can't see, such as a manual login for maintenance. Enable it with `mkfsOptions` (see [FEATURES.md](FEATURES.md)), for example `-O mmp -E mmp_update_interval=5`. Notes:

- `tune2fs` refuses any change to a mounted MMP filesystem (`MMP: device currently active`). To make it remount read-only on errors, set `errors=remount-ro` in the PV's `spec.mountOptions` instead of `tune2fs -e`.
- New mount options only take effect after a full unstage (scale to zero, move the pod, or reboot the node). A pod restarting on the same node keeps the existing staging mount.
- The second mount waits about twice the MMP check interval: up to ~45 s on a freshly formatted volume, and less once the interval settles.
