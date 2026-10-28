package framework

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
)

// Helpers for specs that need more than one node: draining, eviction, and the kubelet's
// view of which volumes are in use.

// SchedulableReadyNodes returns the names of Ready, schedulable nodes, sorted.
func (k *KubernetesClient) SchedulableReadyNodes(ctx context.Context) ([]string, error) {
	nodes, err := k.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var names []string
	for i := range nodes.Items {
		n := &nodes.Items[i]
		if n.Spec.Unschedulable {
			continue
		}
		for _, c := range n.Status.Conditions {
			if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
				names = append(names, n.Name)
			}
		}
	}
	sort.Strings(names)
	return names, nil
}

// SetNodeUnschedulable cordons (true) or uncordons (false) a node.
func (k *KubernetesClient) SetNodeUnschedulable(ctx context.Context, node string, unschedulable bool) error {
	patch := fmt.Sprintf(`{"spec":{"unschedulable":%t}}`, unschedulable)
	_, err := k.clientset.CoreV1().Nodes().Patch(ctx, node, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}

// EvictPod evicts a pod through the eviction API, the way kubectl drain does.
func (k *KubernetesClient) EvictPod(ctx context.Context, name string) error {
	return k.clientset.PolicyV1().Evictions(k.namespace).Evict(ctx, &policyv1.Eviction{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: k.namespace},
	})
}

// CSIUniqueVolumeName is the name the kubelet uses for a CSI volume in
// node.status.volumesInUse and volumesAttached.
func CSIUniqueVolumeName(driver, volumeHandle string) string {
	return fmt.Sprintf("kubernetes.io/csi/%s^%s", driver, volumeHandle)
}

// NodeVolumesInUse reports whether the node lists the volume in status.volumesInUse.
// The attach/detach controller only waits for a node to unstage a volume it lists
// there, so this is what decides whether a volume is fenced.
func (k *KubernetesClient) NodeVolumesInUse(ctx context.Context, node, uniqueName string) (bool, error) {
	n, err := k.clientset.CoreV1().Nodes().Get(ctx, node, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	for _, v := range n.Status.VolumesInUse {
		if string(v) == uniqueName {
			return true, nil
		}
	}
	return false, nil
}

// NodeVolumesAttached reports whether the node lists the volume in status.volumesAttached.
func (k *KubernetesClient) NodeVolumesAttached(ctx context.Context, node, uniqueName string) (bool, error) {
	n, err := k.clientset.CoreV1().Nodes().Get(ctx, node, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	for _, v := range n.Status.VolumesAttached {
		if string(v.Name) == uniqueName {
			return true, nil
		}
	}
	return false, nil
}

// CSIStagingDir is the part of a CSI volume's staging path that the kubelet derives from
// its handle: <plugin dir>/<driver>/<sha256 of the handle>/globalmount. The mount stays
// while the volume is staged on the node and disappears once NodeUnstageVolume has run.
func CSIStagingDir(driver, volumeHandle string) string {
	return fmt.Sprintf("%s/%x/globalmount", driver, sha256.Sum256([]byte(volumeHandle)))
}

// StartNodeShell starts a privileged pod in the host's PID namespace on node, for specs
// that need to look at or act on the node itself, and returns its name once it runs.
func (k *KubernetesClient) StartNodeShell(ctx context.Context, node string) (string, error) {
	name := "node-shell-" + node
	privileged := true
	var zero int64
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: k.namespace},
		Spec: corev1.PodSpec{
			NodeName:                      node,
			HostPID:                       true,
			TerminationGracePeriodSeconds: &zero,
			Tolerations:                   []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
			Containers: []corev1.Container{{
				Name:            "shell",
				Image:           TestPodImage,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Command:         []string{"sh", "-c", "while true; do sleep 30; done"},
				SecurityContext: &corev1.SecurityContext{Privileged: &privileged},
			}},
		},
	}
	if _, err := k.clientset.CoreV1().Pods(k.namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return "", err
	}
	return name, k.WaitForPodReady(ctx, name, 3*time.Minute)
}

// NodeHasMount reports whether the host's mount table, read through a node shell from
// StartNodeShell, has a mount whose line contains substr.
func (k *KubernetesClient) NodeHasMount(ctx context.Context, shell, substr string) (bool, error) {
	out, err := k.ExecInPod(ctx, shell, []string{"sh", "-c", "grep -cF -- \"$0\" /proc/1/mountinfo || true", substr})
	if err != nil {
		return false, err
	}
	return out != "0", nil
}

// RunOnHost runs a shell command in the host's namespaces through a node shell.
func (k *KubernetesClient) RunOnHost(ctx context.Context, shell, command string) (string, error) {
	return k.ExecInPod(ctx, shell, []string{"nsenter", "-t", "1", "-m", "-u", "-i", "-n", "-p", "--", "sh", "-c", command})
}

// DeploymentOptions describes a single-volume Deployment for cross-node specs.
type DeploymentOptions struct {
	Name     string
	PVCName  string
	NodeName string // pins the pod with a hostname nodeSelector when set
	Command  []string
	Replicas int32
}

// CreateDeployment creates a Deployment whose pods mount PVCName at /data.
func (k *KubernetesClient) CreateDeployment(ctx context.Context, opts DeploymentOptions) error {
	labels := map[string]string{"app": opts.Name}
	cmd := opts.Command
	if cmd == nil {
		cmd = []string{"sh", "-c", "while true; do sleep 30; done"}
	}
	var zero int64
	spec := corev1.PodSpec{
		TerminationGracePeriodSeconds: &zero,
		Containers: []corev1.Container{{
			Name:            "app",
			Image:           TestPodImage,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Command:         cmd,
			VolumeMounts:    []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
		}},
		Volumes: []corev1.Volume{{
			Name: "data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: opts.PVCName},
			},
		}},
	}
	if opts.NodeName != "" {
		spec.NodeSelector = map[string]string{"kubernetes.io/hostname": opts.NodeName}
	}
	replicas := opts.Replicas
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: opts.Name, Namespace: k.namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: spec},
		},
	}
	_, err := k.clientset.AppsV1().Deployments(k.namespace).Create(ctx, d, metav1.CreateOptions{})
	return err
}

// ScaleDeployment sets a Deployment's replica count.
func (k *KubernetesClient) ScaleDeployment(ctx context.Context, name string, replicas int32) error {
	patch := fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas)
	_, err := k.clientset.AppsV1().Deployments(k.namespace).Patch(ctx, name, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}

// DeleteDeployment deletes a Deployment and its pods.
func (k *KubernetesClient) DeleteDeployment(ctx context.Context, name string) error {
	policy := metav1.DeletePropagationForeground
	return k.clientset.AppsV1().Deployments(k.namespace).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &policy})
}

// DeploymentPods returns the non-terminating pods of a Deployment.
func (k *KubernetesClient) DeploymentPods(ctx context.Context, name string) ([]corev1.Pod, error) {
	list, err := k.clientset.CoreV1().Pods(k.namespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + name})
	if err != nil {
		return nil, err
	}
	var pods []corev1.Pod
	for i := range list.Items {
		if list.Items[i].DeletionTimestamp == nil {
			pods = append(pods, list.Items[i])
		}
	}
	return pods, nil
}

// WaitForDeploymentPodRunning waits for exactly one running, ready pod other than
// excludeUID and returns it.
func (k *KubernetesClient) WaitForDeploymentPodRunning(ctx context.Context, name string, excludeUID types.UID, timeout time.Duration) (*corev1.Pod, error) {
	var found *corev1.Pod
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		pods, listErr := k.DeploymentPods(ctx, name)
		if listErr != nil {
			klog.V(4).Infof("Listing pods of %s: %v", name, listErr) // transient: keep polling
		}
		for i := range pods {
			p := pods[i]
			if p.UID == excludeUID || p.Status.Phase != corev1.PodRunning {
				continue
			}
			for _, c := range p.Status.Conditions {
				if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
					found = &p
					return true, nil
				}
			}
		}
		return false, nil
	})
	return found, err
}

// WaitForDeploymentPodsGone waits until a Deployment has no pods at all, including
// terminating ones.
func (k *KubernetesClient) WaitForDeploymentPodsGone(ctx context.Context, name string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		list, err := k.clientset.CoreV1().Pods(k.namespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + name})
		return err == nil && len(list.Items) == 0, nil
	})
}
