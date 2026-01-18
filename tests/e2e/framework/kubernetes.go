// Package framework provides utilities for E2E testing of the TrueNAS CSI driver.
package framework

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

// Kubernetes client errors.
var (
	ErrPVCNoCapacity           = errors.New("PVC has no capacity set")
	ErrPVCNotBound             = errors.New("PVC is not bound to a PV")
	ErrNotCSIVolume            = errors.New("PV is not a CSI volume")
	ErrSnapshotNoBoundContent  = errors.New("volumesnapshot has no bound content")
	ErrStorageClassProvisioner = errors.New("storageclass has wrong provisioner")
)

// Default access mode for PVCs.
const defaultAccessMode = "ReadWriteOnce"

// KubernetesClient wraps a Kubernetes clientset with helper methods.
type KubernetesClient struct {
	clientset *kubernetes.Clientset
	namespace string
}

// NewKubernetesClient creates a new KubernetesClient.
func NewKubernetesClient(kubeconfig, namespace string) (*KubernetesClient, error) {
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build kubeconfig: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	return &KubernetesClient{
		clientset: clientset,
		namespace: namespace,
	}, nil
}

// Clientset returns the underlying Kubernetes clientset.
func (k *KubernetesClient) Clientset() *kubernetes.Clientset {
	return k.clientset
}

// Namespace returns the test namespace.
func (k *KubernetesClient) Namespace() string {
	return k.namespace
}

// CreateNamespace creates the test namespace.
func (k *KubernetesClient) CreateNamespace(ctx context.Context) error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: k.namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "e2e-test",
			},
		},
	}

	_, err := k.clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create namespace: %w", err)
	}
	return nil
}

// DeleteNamespace deletes the test namespace and waits for deletion.
func (k *KubernetesClient) DeleteNamespace(ctx context.Context, timeout time.Duration) error {
	err := k.clientset.CoreV1().Namespaces().Delete(ctx, k.namespace, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete namespace: %w", err)
	}

	// Wait for namespace to be fully deleted
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		_, err := k.clientset.CoreV1().Namespaces().Get(ctx, k.namespace, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, nil // Continue polling on transient errors
	})
}

// PVCOptions configures a PVC creation.
type PVCOptions struct {
	Name             string
	StorageClassName string
	Size             string
	VolumeMode       *corev1.PersistentVolumeMode
	AccessModes      []corev1.PersistentVolumeAccessMode
}

// CreatePVC creates a PersistentVolumeClaim.
func (k *KubernetesClient) CreatePVC(ctx context.Context, opts PVCOptions) (*corev1.PersistentVolumeClaim, error) {
	quantity, err := resource.ParseQuantity(opts.Size)
	if err != nil {
		return nil, fmt.Errorf("invalid size %q: %w", opts.Size, err)
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      opts.Name,
			Namespace: k.namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      opts.AccessModes,
			StorageClassName: &opts.StorageClassName,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: quantity,
				},
			},
		},
	}

	if opts.VolumeMode != nil {
		pvc.Spec.VolumeMode = opts.VolumeMode
	}

	return k.clientset.CoreV1().PersistentVolumeClaims(k.namespace).Create(ctx, pvc, metav1.CreateOptions{})
}

// GetPVC retrieves a PVC by name.
func (k *KubernetesClient) GetPVC(ctx context.Context, name string) (*corev1.PersistentVolumeClaim, error) {
	return k.clientset.CoreV1().PersistentVolumeClaims(k.namespace).Get(ctx, name, metav1.GetOptions{})
}

// DeletePVC deletes a PVC by name.
func (k *KubernetesClient) DeletePVC(ctx context.Context, name string) error {
	err := k.clientset.CoreV1().PersistentVolumeClaims(k.namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// WaitForPVCBound waits for a PVC to reach the Bound phase.
func (k *KubernetesClient) WaitForPVCBound(ctx context.Context, name string, timeout time.Duration) error {
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		pvc, err := k.GetPVC(ctx, name)
		if err != nil {
			return false, nil //nolint:nilerr // Continue polling on transient errors
		}
		return pvc.Status.Phase == corev1.ClaimBound, nil
	})
	if err != nil {
		// Dump diagnostic information on failure
		k.dumpPVCDiagnostics(ctx, name)
		return err
	}

	// Log the volume handle for debugging - this is the TrueNAS dataset/zvol name
	pvc, getErr := k.GetPVC(ctx, name)
	if getErr == nil && pvc.Spec.VolumeName != "" {
		if volumeHandle, handleErr := k.GetVolumeHandle(ctx, pvc.Spec.VolumeName); handleErr == nil {
			fmt.Printf("PVC %s bound to PV %s (VolumeHandle/TrueNAS path: %s)\n", name, pvc.Spec.VolumeName, volumeHandle)
		}
	}

	return nil
}

// dumpPVCDiagnostics dumps diagnostic information when PVC binding fails.
func (k *KubernetesClient) dumpPVCDiagnostics(ctx context.Context, pvcName string) {
	klog.Infof("=== PVC BINDING FAILURE DIAGNOSTICS ===")

	// Get PVC details
	pvc, err := k.GetPVC(ctx, pvcName)
	if err != nil {
		klog.Errorf("Failed to get PVC %s: %v", pvcName, err)
	} else {
		klog.Infof("PVC %s status: %s", pvcName, pvc.Status.Phase)
		klog.Infof("PVC StorageClassName: %s", *pvc.Spec.StorageClassName)
	}

	// Get StorageClass details
	if pvc != nil && pvc.Spec.StorageClassName != nil {
		scName := *pvc.Spec.StorageClassName
		sc, scErr := k.GetStorageClass(ctx, scName)
		if scErr != nil {
			klog.Errorf("Failed to get StorageClass %s: %v", scName, scErr)
		} else {
			klog.Infof("StorageClass %s provisioner: %s", scName, sc.Provisioner)
			klog.Infof("StorageClass %s parameters: %v", scName, sc.Parameters)
			klog.Infof("StorageClass %s volumeBindingMode: %v", scName, sc.VolumeBindingMode)
		}
	}

	// Get PVC events using kubectl
	eventsCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	//nolint:gosec // args are controlled by the framework
	cmd := exec.CommandContext(eventsCtx, "kubectl", "get", "events",
		"-n", k.namespace,
		"--field-selector", "involvedObject.name="+pvcName,
		"-o", "wide")
	output, cmdErr := cmd.CombinedOutput()
	if cmdErr != nil {
		klog.Errorf("Failed to get PVC events: %v", cmdErr)
	} else {
		klog.Infof("PVC Events:\n%s", string(output))
	}

	// Get controller pod logs
	logsCtx, logsCancel := context.WithTimeout(ctx, 10*time.Second)
	defer logsCancel()
	logsCmd := exec.CommandContext(logsCtx, "kubectl", "logs",
		"-n", "kube-system",
		"-l", "app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller",
		"--tail", "100")
	logsOutput, logsErr := logsCmd.CombinedOutput()
	if logsErr != nil {
		klog.Errorf("Failed to get controller logs: %v", logsErr)
	} else {
		klog.Infof("Controller Logs (last 100 lines):\n%s", string(logsOutput))
	}

	// Get node pod logs (important for mount failures like iSCSI/NVMe-oF)
	nodeLogsCtx, nodeLogsCancel := context.WithTimeout(ctx, 10*time.Second)
	defer nodeLogsCancel()
	nodeLogsCmd := exec.CommandContext(nodeLogsCtx, "kubectl", "logs",
		"-n", "kube-system",
		"-l", "app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node",
		"-c", "tns-csi-plugin",
		"--tail", "200")
	nodeLogsOutput, nodeLogsErr := nodeLogsCmd.CombinedOutput()
	if nodeLogsErr != nil {
		klog.Errorf("Failed to get node logs: %v", nodeLogsErr)
	} else {
		klog.Infof("Node Logs (last 200 lines):\n%s", string(nodeLogsOutput))
	}

	klog.Infof("=== END DIAGNOSTICS ===")
}

// GetStorageClass retrieves a StorageClass by name.
func (k *KubernetesClient) GetStorageClass(ctx context.Context, name string) (*storagev1.StorageClass, error) {
	return k.clientset.StorageV1().StorageClasses().Get(ctx, name, metav1.GetOptions{})
}

// ExpandPVC updates a PVC to request more storage.
func (k *KubernetesClient) ExpandPVC(ctx context.Context, name, newSize string) error {
	quantity, err := resource.ParseQuantity(newSize)
	if err != nil {
		return fmt.Errorf("invalid size %q: %w", newSize, err)
	}

	pvc, err := k.GetPVC(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to get PVC: %w", err)
	}

	pvc.Spec.Resources.Requests[corev1.ResourceStorage] = quantity

	_, err = k.clientset.CoreV1().PersistentVolumeClaims(k.namespace).Update(ctx, pvc, metav1.UpdateOptions{})
	return err
}

// GetPVCCapacity returns the current capacity of a PVC.
func (k *KubernetesClient) GetPVCCapacity(ctx context.Context, name string) (string, error) {
	pvc, err := k.GetPVC(ctx, name)
	if err != nil {
		return "", err
	}

	if capacity, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
		return capacity.String(), nil
	}
	return "", fmt.Errorf("%w: %s", ErrPVCNoCapacity, name)
}

// GetPVForPVC retrieves the PV bound to a PVC.
func (k *KubernetesClient) GetPVForPVC(ctx context.Context, pvcName string) (*corev1.PersistentVolume, error) {
	pvc, err := k.GetPVC(ctx, pvcName)
	if err != nil {
		return nil, err
	}

	if pvc.Spec.VolumeName == "" {
		return nil, fmt.Errorf("%w: %s", ErrPVCNotBound, pvcName)
	}

	return k.clientset.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
}

// PodOptions configures a Pod creation.
type PodOptions struct {
	Name       string
	PVCName    string
	MountPath  string
	Image      string
	VolumeMode corev1.PersistentVolumeMode
	Command    []string
}

// CreatePod creates a test pod with a volume mount.
func (k *KubernetesClient) CreatePod(ctx context.Context, opts PodOptions) (*corev1.Pod, error) {
	if opts.Image == "" {
		opts.Image = "public.ecr.aws/docker/library/busybox:latest"
	}
	if opts.Command == nil {
		opts.Command = []string{"sleep", "3600"}
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      opts.Name,
			Namespace: k.namespace,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "test",
					Image:   opts.Image,
					Command: opts.Command,
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "test-volume",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: opts.PVCName,
						},
					},
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}

	// Configure volume mount based on volume mode
	if opts.VolumeMode == corev1.PersistentVolumeBlock {
		// Block mode - use VolumeDevices
		pod.Spec.Containers[0].VolumeDevices = []corev1.VolumeDevice{
			{
				Name:       "test-volume",
				DevicePath: opts.MountPath,
			},
		}
	} else {
		// Filesystem mode - use VolumeMounts
		pod.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{
			{
				Name:      "test-volume",
				MountPath: opts.MountPath,
			},
		}
	}

	return k.clientset.CoreV1().Pods(k.namespace).Create(ctx, pod, metav1.CreateOptions{})
}

// GetPod retrieves a Pod by name.
func (k *KubernetesClient) GetPod(ctx context.Context, name string) (*corev1.Pod, error) {
	return k.clientset.CoreV1().Pods(k.namespace).Get(ctx, name, metav1.GetOptions{})
}

// DeletePod deletes a Pod by name.
func (k *KubernetesClient) DeletePod(ctx context.Context, name string) error {
	err := k.clientset.CoreV1().Pods(k.namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// WaitForPodReady waits for a Pod to be Running and Ready.
// On timeout, it logs diagnostic information about why the pod isn't ready.
func (k *KubernetesClient) WaitForPodReady(ctx context.Context, name string, timeout time.Duration) error {
	var lastPod *corev1.Pod
	var lastLogTime time.Time

	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		pod, err := k.GetPod(ctx, name)
		if err != nil {
			return false, nil //nolint:nilerr // Continue polling on transient errors
		}
		lastPod = pod

		// Log status every 30 seconds while waiting
		if time.Since(lastLogTime) > 30*time.Second {
			klog.Infof("Pod %s status: Phase=%s", name, pod.Status.Phase)
			for i := range pod.Status.ContainerStatuses {
				cs := &pod.Status.ContainerStatuses[i]
				if cs.State.Waiting != nil {
					klog.Infof("  Container %s: Waiting - %s: %s", cs.Name, cs.State.Waiting.Reason, cs.State.Waiting.Message)
				}
			}
			lastLogTime = time.Now()
		}

		if pod.Status.Phase != corev1.PodRunning {
			return false, nil
		}

		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})

	// On timeout, log detailed diagnostics
	if err != nil && lastPod != nil {
		klog.Errorf("Pod %s failed to become ready. Final status:", name)
		klog.Errorf("  Phase: %s", lastPod.Status.Phase)
		for _, cond := range lastPod.Status.Conditions {
			klog.Errorf("  Condition %s: %s (Reason: %s, Message: %s)",
				cond.Type, cond.Status, cond.Reason, cond.Message)
		}
		for i := range lastPod.Status.ContainerStatuses {
			cs := &lastPod.Status.ContainerStatuses[i]
			if cs.State.Waiting != nil {
				klog.Errorf("  Container %s: Waiting - %s: %s", cs.Name, cs.State.Waiting.Reason, cs.State.Waiting.Message)
			} else if cs.State.Terminated != nil {
				klog.Errorf("  Container %s: Terminated - %s (exit code %d)", cs.Name, cs.State.Terminated.Reason, cs.State.Terminated.ExitCode)
			}
		}
		// Log pod events
		k.logPodEvents(ctx, name)
		// Log CSI node pod logs (important for mount failures)
		k.logCSINodeLogs(ctx)
	}

	return err
}

// logCSINodeLogs logs the CSI node pod logs for debugging mount failures.
func (k *KubernetesClient) logCSINodeLogs(ctx context.Context) {
	nodeLogsCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(nodeLogsCtx, "kubectl", "logs",
		"-n", "kube-system",
		"-l", "app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node",
		"-c", "tns-csi-plugin",
		"--tail", "200")
	output, err := cmd.CombinedOutput()
	if err != nil {
		klog.Warningf("Failed to get CSI node logs: %v", err)
		return
	}
	klog.Errorf("CSI Node Logs (last 200 lines):\n%s", string(output))
}

// logPodEvents logs events related to a pod for debugging.
func (k *KubernetesClient) logPodEvents(ctx context.Context, podName string) {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "events", //nolint:gosec // args are controlled by the framework
		"-n", k.namespace,
		"--field-selector", "involvedObject.name="+podName,
		"--sort-by=.lastTimestamp",
		"-o", "custom-columns=TIME:.lastTimestamp,TYPE:.type,REASON:.reason,MESSAGE:.message")
	output, err := cmd.CombinedOutput()
	if err != nil {
		klog.Warningf("Failed to get pod events: %v", err)
		return
	}
	klog.Errorf("Pod %s events:\n%s", podName, string(output))
}

// WaitForPodDeleted waits for a Pod to be fully deleted.
func (k *KubernetesClient) WaitForPodDeleted(ctx context.Context, name string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		_, err := k.GetPod(ctx, name)
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, nil // Continue polling on transient errors
	})
}

// ExecInPod executes a command in a pod and returns the output.
// Uses kubectl exec for simplicity and better compatibility across environments.
func (k *KubernetesClient) ExecInPod(ctx context.Context, podName string, command []string) (string, error) {
	args := make([]string, 0, 5+len(command))
	args = append(args, "exec", podName, "-n", k.namespace, "--")
	args = append(args, command...)

	cmd := exec.CommandContext(ctx, "kubectl", args...) //nolint:gosec // args are controlled by the framework
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("exec failed: %w\nstderr: %s", err, stderr.String())
	}

	return strings.TrimSpace(stdout.String()), nil
}

// WaitForPVDeleted waits for a PV to be deleted (after PVC deletion).
func (k *KubernetesClient) WaitForPVDeleted(ctx context.Context, pvName string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		_, err := k.clientset.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, nil // Continue polling on transient errors
	})
}

// WaitForPVCDeleted waits for a PVC to be deleted.
func (k *KubernetesClient) WaitForPVCDeleted(ctx context.Context, name string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		_, err := k.GetPVC(ctx, name)
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, nil // Continue polling on transient errors
	})
}

// CreatePVCFromSnapshot creates a PVC from a VolumeSnapshot using kubectl.
func (k *KubernetesClient) CreatePVCFromSnapshot(ctx context.Context, pvcName, snapshotName, storageClass, size string, accessModes []corev1.PersistentVolumeAccessMode) error {
	accessModeStr := defaultAccessMode
	if len(accessModes) > 0 {
		accessModeStr = string(accessModes[0])
	}

	yaml := fmt.Sprintf(`apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: %s
  namespace: %s
spec:
  accessModes:
    - %s
  storageClassName: %s
  resources:
    requests:
      storage: %s
  dataSource:
    name: %s
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
`, pvcName, k.namespace, accessModeStr, storageClass, size, snapshotName)

	return k.applyYAML(ctx, yaml)
}

// CreatePVCFromPVC creates a clone PVC from an existing PVC using kubectl.
func (k *KubernetesClient) CreatePVCFromPVC(ctx context.Context, cloneName, sourcePVCName, storageClass, size string, accessModes []corev1.PersistentVolumeAccessMode) error {
	accessModeStr := defaultAccessMode
	if len(accessModes) > 0 {
		accessModeStr = string(accessModes[0])
	}

	yaml := fmt.Sprintf(`apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: %s
  namespace: %s
spec:
  accessModes:
    - %s
  storageClassName: %s
  resources:
    requests:
      storage: %s
  dataSource:
    name: %s
    kind: PersistentVolumeClaim
`, cloneName, k.namespace, accessModeStr, storageClass, size, sourcePVCName)

	return k.applyYAML(ctx, yaml)
}

// CreateVolumeSnapshot creates a VolumeSnapshot using kubectl.
func (k *KubernetesClient) CreateVolumeSnapshot(ctx context.Context, snapshotName, pvcName, snapshotClass string) error {
	yaml := fmt.Sprintf(`apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: %s
  namespace: %s
spec:
  volumeSnapshotClassName: %s
  source:
    persistentVolumeClaimName: %s
`, snapshotName, k.namespace, snapshotClass, pvcName)

	return k.applyYAML(ctx, yaml)
}

// WaitForSnapshotReady waits for a VolumeSnapshot to be ready using kubectl.
func (k *KubernetesClient) WaitForSnapshotReady(ctx context.Context, snapshotName string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		args := []string{
			"get", "volumesnapshot", snapshotName,
			"-n", k.namespace,
			"-o", "jsonpath={.status.readyToUse}",
		}
		cmd := exec.CommandContext(ctx, "kubectl", args...) //nolint:gosec // args are controlled by the framework
		output, err := cmd.Output()
		if err != nil {
			return false, nil //nolint:nilerr // Continue polling on transient errors
		}
		return strings.TrimSpace(string(output)) == "true", nil
	})
}

// DeleteVolumeSnapshot deletes a VolumeSnapshot using kubectl.
func (k *KubernetesClient) DeleteVolumeSnapshot(ctx context.Context, snapshotName string) error {
	args := []string{
		"delete", "volumesnapshot", snapshotName,
		"-n", k.namespace,
		"--ignore-not-found=true",
	}
	cmd := exec.CommandContext(ctx, "kubectl", args...) //nolint:gosec // args are controlled by the framework
	return cmd.Run()
}

// CreateVolumeSnapshotClass creates a VolumeSnapshotClass using kubectl.
func (k *KubernetesClient) CreateVolumeSnapshotClass(ctx context.Context, name, driver, deletionPolicy string) error {
	yaml := fmt.Sprintf(`apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: %s
driver: %s
deletionPolicy: %s
`, name, driver, deletionPolicy)

	return k.applyYAML(ctx, yaml)
}

// CreateVolumeSnapshotClassWithParams creates a VolumeSnapshotClass with parameters using kubectl.
func (k *KubernetesClient) CreateVolumeSnapshotClassWithParams(ctx context.Context, name, driver, deletionPolicy string, params map[string]string) error {
	var paramsYAML string
	if len(params) > 0 {
		paramLines := make([]string, 0, len(params))
		for key, value := range params {
			paramLines = append(paramLines, fmt.Sprintf("  %s: %q", key, value))
		}
		paramsYAML = "parameters:\n" + strings.Join(paramLines, "\n") + "\n"
	}

	yaml := fmt.Sprintf(`apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: %s
driver: %s
deletionPolicy: %s
%s`, name, driver, deletionPolicy, paramsYAML)

	return k.applyYAML(ctx, yaml)
}

// VolumeSnapshotInfo contains information about a VolumeSnapshot.
type VolumeSnapshotInfo struct {
	Name       string
	ReadyToUse *bool
	Error      string
}

// GetVolumeSnapshot gets a VolumeSnapshot by name.
func (k *KubernetesClient) GetVolumeSnapshot(ctx context.Context, name string) (*VolumeSnapshotInfo, error) {
	args := []string{
		"get", "volumesnapshot", name,
		"-n", k.namespace,
		"-o", "json",
	}
	cmd := exec.CommandContext(ctx, "kubectl", args...) //nolint:gosec // args are controlled by the framework
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("kubectl get volumesnapshot failed: %w\nstderr: %s", err, stderr.String())
	}

	// Parse just the fields we need (nolint:govet - anonymous struct used for JSON parsing)
	var snapshot struct { //nolint:govet
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Status struct {
			ReadyToUse *bool `json:"readyToUse"`
			Error      *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"status"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &snapshot); err != nil {
		return nil, fmt.Errorf("failed to unmarshal volumesnapshot: %w", err)
	}

	info := &VolumeSnapshotInfo{
		Name:       snapshot.Metadata.Name,
		ReadyToUse: snapshot.Status.ReadyToUse,
	}
	if snapshot.Status.Error != nil {
		info.Error = snapshot.Status.Error.Message
	}
	return info, nil
}

// VolumeSnapshotContentInfo contains the relevant information from a VolumeSnapshotContent.
type VolumeSnapshotContentInfo struct {
	Name           string
	SnapshotHandle string
	DeletionPolicy string
	ReadyToUse     bool
}

// GetVolumeSnapshotContent gets the VolumeSnapshotContent for a VolumeSnapshot.
// It first gets the snapshot to find the content name, then fetches the content.
func (k *KubernetesClient) GetVolumeSnapshotContent(ctx context.Context, snapshotName string) (*VolumeSnapshotContentInfo, error) {
	// First get the VolumeSnapshot to find the boundVolumeSnapshotContentName
	args := []string{
		"get", "volumesnapshot", snapshotName,
		"-n", k.namespace,
		"-o", "jsonpath={.status.boundVolumeSnapshotContentName}",
	}
	cmd := exec.CommandContext(ctx, "kubectl", args...) //nolint:gosec // args are controlled by the framework
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to get volumesnapshot content name: %w\nstderr: %s", err, stderr.String())
	}

	contentName := strings.TrimSpace(stdout.String())
	if contentName == "" {
		return nil, fmt.Errorf("%w: %s", ErrSnapshotNoBoundContent, snapshotName)
	}

	// Now get the VolumeSnapshotContent (cluster-scoped, no namespace)
	args = []string{
		"get", "volumesnapshotcontent", contentName,
		"-o", "json",
	}
	cmd = exec.CommandContext(ctx, "kubectl", args...) //nolint:gosec // args are controlled by the framework
	stdout.Reset()
	stderr.Reset()
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to get volumesnapshotcontent: %w\nstderr: %s", err, stderr.String())
	}

	// Parse the content
	var content struct { //nolint:govet
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec struct {
			DeletionPolicy string `json:"deletionPolicy"`
		} `json:"spec"`
		Status struct {
			SnapshotHandle *string `json:"snapshotHandle"`
			ReadyToUse     *bool   `json:"readyToUse"`
		} `json:"status"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &content); err != nil {
		return nil, fmt.Errorf("failed to unmarshal volumesnapshotcontent: %w", err)
	}

	info := &VolumeSnapshotContentInfo{
		Name:           content.Metadata.Name,
		DeletionPolicy: content.Spec.DeletionPolicy,
	}
	if content.Status.SnapshotHandle != nil {
		info.SnapshotHandle = *content.Status.SnapshotHandle
	}
	if content.Status.ReadyToUse != nil {
		info.ReadyToUse = *content.Status.ReadyToUse
	}
	return info, nil
}

// DeleteVolumeSnapshotClass deletes a VolumeSnapshotClass using kubectl.
func (k *KubernetesClient) DeleteVolumeSnapshotClass(ctx context.Context, name string) error {
	args := []string{
		"delete", "volumesnapshotclass", name,
		"--ignore-not-found=true",
	}
	cmd := exec.CommandContext(ctx, "kubectl", args...) //nolint:gosec // args are controlled by the framework
	return cmd.Run()
}

// applyYAML applies a YAML manifest using kubectl.
func (k *KubernetesClient) applyYAML(ctx context.Context, yaml string) error {
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(yaml)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kubectl apply failed: %w\nstderr: %s", err, stderr.String())
	}
	return nil
}

// FileExistsInPod checks if a file exists in a pod.
func (k *KubernetesClient) FileExistsInPod(ctx context.Context, podName, filePath string) (bool, error) {
	_, err := k.ExecInPod(ctx, podName, []string{"test", "-f", filePath})
	if err != nil {
		// Check if it's just a "file doesn't exist" error vs actual error
		if strings.Contains(err.Error(), "exit status 1") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ForceDeletePod force deletes a pod (simulating crash).
func (k *KubernetesClient) ForceDeletePod(ctx context.Context, name string) error {
	args := []string{
		"delete", "pod", name,
		"-n", k.namespace,
		"--force",
		"--grace-period=0",
	}
	cmd := exec.CommandContext(ctx, "kubectl", args...) //nolint:gosec // args are controlled by the framework
	return cmd.Run()
}

// StatefulSetOptions configures a StatefulSet creation.
type StatefulSetOptions struct {
	Labels           map[string]string
	Name             string
	ServiceName      string
	StorageClassName string
	StorageSize      string
	MountPath        string
	Image            string
	AccessModes      []corev1.PersistentVolumeAccessMode
	Command          []string
	Replicas         int32
}

// CreateHeadlessService creates a headless service for StatefulSet.
func (k *KubernetesClient) CreateHeadlessService(ctx context.Context, name string, labels map[string]string) error {
	yaml := fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: %s
  labels:
    app: %s
spec:
  ports:
  - port: 80
    name: web
  clusterIP: None
  selector:
    app: %s
`, name, k.namespace, labels["app"], labels["app"])

	return k.applyYAML(ctx, yaml)
}

// CreateStatefulSet creates a StatefulSet with volumeClaimTemplates using kubectl.
func (k *KubernetesClient) CreateStatefulSet(ctx context.Context, opts StatefulSetOptions) error {
	if opts.Image == "" {
		opts.Image = "public.ecr.aws/docker/library/busybox:latest"
	}
	if opts.MountPath == "" {
		opts.MountPath = "/data"
	}

	// Build access modes string
	accessModeStr := defaultAccessMode
	if len(opts.AccessModes) > 0 {
		accessModeStr = string(opts.AccessModes[0])
	}

	// Build labels
	appLabel := opts.Labels["app"]
	if appLabel == "" {
		appLabel = opts.Name
	}

	// Build command - the command writes pod identity to the volume
	commandYAML := `command:
          - sh
          - -c
          - |
            echo "Pod: ${HOSTNAME}" > /data/pod-identity.txt
            echo "Started at: $(date)" >> /data/pod-identity.txt
            sync
            while true; do sleep 30; done`

	yaml := fmt.Sprintf(`apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: %s
  namespace: %s
spec:
  serviceName: %s
  replicas: %d
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
    spec:
      containers:
      - name: test
        image: %s
        %s
        volumeMounts:
        - name: data
          mountPath: %s
  volumeClaimTemplates:
  - metadata:
      name: data
    spec:
      accessModes: [ "%s" ]
      storageClassName: %s
      resources:
        requests:
          storage: %s
`, opts.Name, k.namespace, opts.ServiceName, opts.Replicas, appLabel, appLabel, opts.Image, commandYAML, opts.MountPath, accessModeStr, opts.StorageClassName, opts.StorageSize)

	return k.applyYAML(ctx, yaml)
}

// ScaleStatefulSet scales a StatefulSet to the specified number of replicas.
func (k *KubernetesClient) ScaleStatefulSet(ctx context.Context, name string, replicas int32) error {
	args := []string{
		"scale", "statefulset", name,
		"-n", k.namespace,
		fmt.Sprintf("--replicas=%d", replicas),
	}
	cmd := exec.CommandContext(ctx, "kubectl", args...) //nolint:gosec // args are controlled by the framework
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("scale statefulset failed: %w\nstderr: %s", err, stderr.String())
	}
	return nil
}

// WaitForStatefulSetReady waits for all pods in a StatefulSet to be ready.
func (k *KubernetesClient) WaitForStatefulSetReady(ctx context.Context, name string, replicas int32, timeout time.Duration) error {
	// Wait for each pod to be ready (StatefulSets create pods in order)
	for i := range replicas {
		podName := fmt.Sprintf("%s-%d", name, i)
		if err := k.WaitForPodReady(ctx, podName, timeout); err != nil {
			return fmt.Errorf("pod %s not ready: %w", podName, err)
		}
	}
	return nil
}

// DeleteStatefulSet deletes a StatefulSet using kubectl.
func (k *KubernetesClient) DeleteStatefulSet(ctx context.Context, name string) error {
	args := []string{
		"delete", "statefulset", name,
		"-n", k.namespace,
		"--ignore-not-found=true",
	}
	cmd := exec.CommandContext(ctx, "kubectl", args...) //nolint:gosec // args are controlled by the framework
	return cmd.Run()
}

// DeleteService deletes a Service using kubectl.
func (k *KubernetesClient) DeleteService(ctx context.Context, name string) error {
	args := []string{
		"delete", "service", name,
		"-n", k.namespace,
		"--ignore-not-found=true",
	}
	cmd := exec.CommandContext(ctx, "kubectl", args...) //nolint:gosec // args are controlled by the framework
	return cmd.Run()
}

// GetStatefulSetPodName returns the pod name for a given StatefulSet replica index.
func (k *KubernetesClient) GetStatefulSetPodName(stsName string, index int) string {
	return fmt.Sprintf("%s-%d", stsName, index)
}

// GetStatefulSetPVCName returns the PVC name for a given StatefulSet replica index.
// StatefulSet PVC naming convention: <volumeClaimTemplate.name>-<statefulset.name>-<ordinal>.
func (k *KubernetesClient) GetStatefulSetPVCName(stsName, volumeName string, index int) string {
	return fmt.Sprintf("%s-%s-%d", volumeName, stsName, index)
}

// ListStatefulSetPods returns all pods belonging to a StatefulSet.
func (k *KubernetesClient) ListStatefulSetPods(ctx context.Context, stsName string, replicas int32) ([]string, error) {
	pods := make([]string, replicas)
	for i := range replicas {
		pods[i] = k.GetStatefulSetPodName(stsName, int(i))
	}
	return pods, nil
}

// WaitForPodToBeDeleted waits for a specific pod to be deleted (used for scale down).
func (k *KubernetesClient) WaitForPodToBeDeleted(ctx context.Context, name string, timeout time.Duration) error {
	args := []string{
		"wait", "--for=delete",
		"pod/" + name,
		"-n", k.namespace,
		"--timeout=" + timeout.String(),
	}
	cmd := exec.CommandContext(ctx, "kubectl", args...) //nolint:gosec // args are controlled by the framework
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Ignore if pod is already deleted
		if strings.Contains(stderr.String(), "not found") {
			return nil
		}
		return fmt.Errorf("wait for pod delete failed: %w\nstderr: %s", err, stderr.String())
	}
	return nil
}

// CreateStorageClassWithParams creates a StorageClass with custom parameters.
func (k *KubernetesClient) CreateStorageClassWithParams(ctx context.Context, name, provisioner string, params map[string]string) error {
	// Build parameters YAML
	var paramsBuilder strings.Builder
	for key, value := range params {
		paramsBuilder.WriteString("  ")
		paramsBuilder.WriteString(key)
		paramsBuilder.WriteString(": \"")
		paramsBuilder.WriteString(value)
		paramsBuilder.WriteString("\"\n")
	}

	yaml := fmt.Sprintf(`apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: %s
provisioner: %s
parameters:
%sreclaimPolicy: Delete
allowVolumeExpansion: true
volumeBindingMode: Immediate
`, name, provisioner, paramsBuilder.String())

	klog.Infof("Creating StorageClass %s with provisioner %s", name, provisioner)
	klog.V(2).Infof("StorageClass YAML:\n%s", yaml)

	if err := k.applyYAML(ctx, yaml); err != nil {
		return err
	}

	// Verify the StorageClass was created correctly
	sc, err := k.GetStorageClass(ctx, name)
	if err != nil {
		return fmt.Errorf("StorageClass %s was not created: %w", name, err)
	}
	if sc.Provisioner != provisioner {
		return fmt.Errorf("%w: %s has %s, want %s", ErrStorageClassProvisioner, name, sc.Provisioner, provisioner)
	}
	klog.Infof("StorageClass %s created successfully with provisioner %s", name, sc.Provisioner)
	return nil
}

// CreateStorageClassWithParamsAndBindingMode creates a StorageClass with custom parameters and binding mode.
func (k *KubernetesClient) CreateStorageClassWithParamsAndBindingMode(ctx context.Context, name, provisioner string, params map[string]string, bindingMode string) error {
	// Build parameters YAML
	var paramsBuilder strings.Builder
	for key, value := range params {
		paramsBuilder.WriteString("  ")
		paramsBuilder.WriteString(key)
		paramsBuilder.WriteString(": \"")
		paramsBuilder.WriteString(value)
		paramsBuilder.WriteString("\"\n")
	}

	yaml := fmt.Sprintf(`apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: %s
provisioner: %s
parameters:
%sreclaimPolicy: Delete
allowVolumeExpansion: true
volumeBindingMode: %s
`, name, provisioner, paramsBuilder.String(), bindingMode)

	return k.applyYAML(ctx, yaml)
}

// DeleteStorageClass deletes a StorageClass using kubectl.
func (k *KubernetesClient) DeleteStorageClass(ctx context.Context, name string) error {
	args := []string{
		"delete", "storageclass", name,
		"--ignore-not-found=true",
	}
	cmd := exec.CommandContext(ctx, "kubectl", args...) //nolint:gosec // args are controlled by the framework
	return cmd.Run()
}

// GetPVName returns the PV name bound to a PVC.
func (k *KubernetesClient) GetPVName(ctx context.Context, pvcName string) (string, error) {
	pvc, err := k.GetPVC(ctx, pvcName)
	if err != nil {
		return "", err
	}
	if pvc.Spec.VolumeName == "" {
		return "", fmt.Errorf("%w: %s", ErrPVCNotBound, pvcName)
	}
	return pvc.Spec.VolumeName, nil
}

// GetVolumeHandle returns the CSI volume handle for a PV.
func (k *KubernetesClient) GetVolumeHandle(ctx context.Context, pvName string) (string, error) {
	pv, err := k.clientset.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if pv.Spec.CSI == nil {
		return "", fmt.Errorf("%w: %s", ErrNotCSIVolume, pvName)
	}
	return pv.Spec.CSI.VolumeHandle, nil
}

// GetPV retrieves a PersistentVolume by name.
func (k *KubernetesClient) GetPV(ctx context.Context, pvName string) (*corev1.PersistentVolume, error) {
	return k.clientset.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
}

// GetControllerLogs returns recent logs from the CSI controller.
func (k *KubernetesClient) GetControllerLogs(ctx context.Context, tailLines int) (string, error) {
	args := []string{
		"logs",
		"-n", "kube-system",
		"-l", "app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller",
		fmt.Sprintf("--tail=%d", tailLines),
	}
	cmd := exec.CommandContext(ctx, "kubectl", args...) //nolint:gosec // args are controlled by the framework
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get controller logs: %w", err)
	}
	return string(output), nil
}
