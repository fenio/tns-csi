package dashboard

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

// FetchK8sVolumes builds VolumeInfo directly from K8s PVs without any TrueNAS API calls.
// This provides a fast initial view of volumes using only the Kubernetes API.
func FetchK8sVolumes(ctx context.Context) ([]VolumeInfo, *K8sEnrichmentResult) {
	result := &K8sEnrichmentResult{
		Bindings: make(map[string]*K8sVolumeBinding),
	}

	enrichCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	config, err := rest.InClusterConfig()
	if err != nil {
		klog.V(4).Infof("K8s volumes unavailable (not in cluster): %v", err)
		return nil, result
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.V(4).Infof("K8s volumes failed to create client: %v", err)
		return nil, result
	}

	pvList, err := clientset.CoreV1().PersistentVolumes().List(enrichCtx, metav1.ListOptions{})
	if err != nil {
		klog.V(4).Infof("K8s volumes failed to list PVs: %v", err)
		return nil, result
	}

	result.Available = true
	var volumes []VolumeInfo

	for i := range pvList.Items {
		pv := &pvList.Items[i]

		if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != "tns.csi.io" {
			continue
		}

		attrs := pv.Spec.CSI.VolumeAttributes
		binding := &K8sVolumeBinding{
			PVName:   pv.Name,
			PVStatus: string(pv.Status.Phase),
		}
		if pv.Spec.ClaimRef != nil {
			binding.PVCName = pv.Spec.ClaimRef.Name
			binding.PVCNamespace = pv.Spec.ClaimRef.Namespace
		}

		vol := VolumeInfo{
			Dataset:  pv.Spec.CSI.VolumeHandle,
			VolumeID: attrs["datasetName"],
			Protocol: attrs["protocol"],
			K8s:      binding,
		}
		if vol.VolumeID == "" {
			vol.VolumeID = pv.Spec.CSI.VolumeHandle
		}

		// Get capacity from PV spec
		if storage, ok := pv.Spec.Capacity[corev1.ResourceStorage]; ok {
			vol.CapacityBytes = storage.Value()
			vol.CapacityHuman = FormatBytes(vol.CapacityBytes)
		}

		// Determine type from protocol
		switch vol.Protocol {
		case protocolNVMeOF, protocolISCSI:
			vol.Type = "VOLUME"
		default:
			vol.Type = "FILESYSTEM"
		}

		result.Bindings[pv.Spec.CSI.VolumeHandle] = binding
		volumes = append(volumes, vol)
	}

	return volumes, result
}

// MatchK8sBinding tries to find a K8s binding by dataset path first (new volumes),
// then falls back to csi_volume_name (old volumes).
func MatchK8sBinding(bindings map[string]*K8sVolumeBinding, dataset, volumeID string) *K8sVolumeBinding {
	if b, ok := bindings[dataset]; ok {
		return b
	}
	if volumeID != "" && volumeID != dataset {
		if b, ok := bindings[volumeID]; ok {
			return b
		}
	}
	return nil
}

// EnrichWithK8sData fetches K8s PV/PVC data and optionally pod data.
// When running in-cluster, uses the service account token.
// Returns best-effort results — if K8s is unavailable, Available will be false.
func EnrichWithK8sData(ctx context.Context, includePods bool) *K8sEnrichmentResult {
	result := &K8sEnrichmentResult{
		Bindings: make(map[string]*K8sVolumeBinding),
	}

	enrichCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	config, err := rest.InClusterConfig()
	if err != nil {
		klog.V(4).Infof("K8s enrichment unavailable (not in cluster): %v", err)
		return result
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.V(4).Infof("K8s enrichment failed to create client: %v", err)
		return result
	}

	// List PVs
	pvList, err := clientset.CoreV1().PersistentVolumes().List(enrichCtx, metav1.ListOptions{})
	if err != nil {
		klog.V(4).Infof("K8s enrichment failed to list PVs: %v", err)
		return result
	}

	result.Available = true

	for i := range pvList.Items {
		pv := &pvList.Items[i]

		// Only include PVs from our driver
		if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != "tns.csi.io" {
			continue
		}

		binding := &K8sVolumeBinding{
			PVName:   pv.Name,
			PVStatus: string(pv.Status.Phase),
		}

		if pv.Spec.ClaimRef != nil {
			binding.PVCName = pv.Spec.ClaimRef.Name
			binding.PVCNamespace = pv.Spec.ClaimRef.Namespace
		}

		result.Bindings[pv.Spec.CSI.VolumeHandle] = binding
	}

	if includePods {
		pods, podErr := clientset.CoreV1().Pods("").List(enrichCtx, metav1.ListOptions{})
		if podErr != nil {
			klog.V(4).Infof("K8s enrichment failed to list pods: %v", podErr)
			return result
		}

		pvcToPods := make(map[string][]string)
		for i := range pods.Items {
			pod := &pods.Items[i]
			for j := range pod.Spec.Volumes {
				pvc := pod.Spec.Volumes[j].PersistentVolumeClaim
				if pvc != nil {
					key := pod.Namespace + "/" + pvc.ClaimName
					podRef := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
					pvcToPods[key] = append(pvcToPods[key], podRef)
				}
			}
		}

		for _, binding := range result.Bindings {
			if binding.PVCName != "" && binding.PVCNamespace != "" {
				key := binding.PVCNamespace + "/" + binding.PVCName
				if podRefs, ok := pvcToPods[key]; ok {
					binding.Pods = podRefs
				}
			}
		}
	}

	return result
}
