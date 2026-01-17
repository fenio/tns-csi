package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// Static errors for list-orphaned command.
var errOrphanedUnknownOutputFormat = errors.New("unknown output format")

// OrphanedVolumeInfo represents a volume that exists on TrueNAS but has no matching PVC.
type OrphanedVolumeInfo struct {
	PVCName    string `json:"pvcName,omitempty"   yaml:"pvcName,omitempty"`
	Namespace  string `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	Reason     string `json:"reason"              yaml:"reason"`
	VolumeInfo `json:",inline"             yaml:",inline"`
}

func newListOrphanedCmd(url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool) *cobra.Command {
	var allNamespaces bool

	cmd := &cobra.Command{
		Use:   "list-orphaned",
		Short: "Find volumes on TrueNAS without matching PVCs in the cluster",
		Long: `Find orphaned volumes - volumes that exist on TrueNAS but have no
corresponding PVC in the current Kubernetes cluster.

This is useful for:
  - Disaster recovery: finding volumes after cluster recreation
  - Cleanup: identifying volumes that can be deleted
  - Adoption: preparing to adopt volumes into a new cluster

Examples:
  # Find orphaned volumes
  kubectl tns-csi list-orphaned

  # Output in YAML for scripting
  kubectl tns-csi list-orphaned -o yaml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runListOrphaned(cmd.Context(), url, apiKey, secretRef, outputFormat, skipTLSVerify, allNamespaces)
		},
	}

	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", true, "Search all namespaces for PVCs")

	return cmd
}

func runListOrphaned(ctx context.Context, url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool, allNamespaces bool) error {
	// Get connection config
	cfg, err := getConnectionConfig(ctx, url, apiKey, secretRef, skipTLSVerify)
	if err != nil {
		return err
	}

	// Connect to TrueNAS
	client, err := connectToTrueNAS(ctx, cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	// Get Kubernetes client
	k8sClient, err := getK8sClient()
	if err != nil {
		return fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	// Query all managed volumes from TrueNAS
	volumes, err := findManagedVolumes(ctx, client)
	if err != nil {
		return fmt.Errorf("failed to query volumes: %w", err)
	}

	// Get all PVs and PVCs from Kubernetes
	pvMap, pvcMap, err := getK8sVolumeInfo(ctx, k8sClient, allNamespaces)
	if err != nil {
		return fmt.Errorf("failed to query Kubernetes volumes: %w", err)
	}

	// Find orphaned volumes
	orphaned := findOrphanedVolumes(volumes, pvMap, pvcMap)

	// Output
	return outputOrphanedVolumes(orphaned, *outputFormat)
}

func getK8sClient() (*kubernetes.Clientset, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	config, err := kubeConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	return kubernetes.NewForConfig(config)
}

// pvInfo holds PV information.
type pvInfo struct {
	Name     string
	VolumeID string // CSI volume handle
	PVCName  string
	PVCNs    string
}

// pvcInfo holds PVC information.
type pvcInfo struct {
	Name      string
	Namespace string
	PVName    string
}

func getK8sVolumeInfo(ctx context.Context, client *kubernetes.Clientset, allNamespaces bool) (pvMap map[string]pvInfo, pvcMap map[string]pvcInfo, err error) {
	// Get all PVs
	var pvs *corev1.PersistentVolumeList
	pvs, err = client.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list PVs: %w", err)
	}

	pvMap = make(map[string]pvInfo)
	for i := range pvs.Items {
		pv := &pvs.Items[i]
		// Only consider CSI volumes from our driver
		if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != "tns.csi.io" {
			continue
		}

		info := pvInfo{
			Name:     pv.Name,
			VolumeID: pv.Spec.CSI.VolumeHandle,
		}

		if pv.Spec.ClaimRef != nil {
			info.PVCName = pv.Spec.ClaimRef.Name
			info.PVCNs = pv.Spec.ClaimRef.Namespace
		}

		pvMap[info.VolumeID] = info
	}

	// Get all PVCs
	pvcMap = make(map[string]pvcInfo)

	if allNamespaces {
		var pvcs *corev1.PersistentVolumeClaimList
		pvcs, err = client.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, nil, fmt.Errorf("failed to list PVCs: %w", err)
		}

		for i := range pvcs.Items {
			pvc := &pvcs.Items[i]
			key := pvc.Namespace + "/" + pvc.Name
			pvcMap[key] = pvcInfo{
				Name:      pvc.Name,
				Namespace: pvc.Namespace,
				PVName:    pvc.Spec.VolumeName,
			}
		}
	}

	return pvMap, pvcMap, nil
}

func findOrphanedVolumes(volumes []VolumeInfo, pvMap map[string]pvInfo, pvcMap map[string]pvcInfo) []OrphanedVolumeInfo {
	var orphaned []OrphanedVolumeInfo

	for _, vol := range volumes {
		// Check if there's a PV with this volume ID
		pv, hasPV := pvMap[vol.VolumeID]

		if !hasPV {
			// No PV exists - definitely orphaned
			orphaned = append(orphaned, OrphanedVolumeInfo{
				VolumeInfo: vol,
				Reason:     "no PV in cluster",
			})
			continue
		}

		// PV exists - check if it has a bound PVC
		if pv.PVCName == "" {
			orphaned = append(orphaned, OrphanedVolumeInfo{
				VolumeInfo: vol,
				Reason:     "PV exists but not bound",
			})
			continue
		}

		// Check if the PVC actually exists
		pvcKey := pv.PVCNs + "/" + pv.PVCName
		if _, hasPVC := pvcMap[pvcKey]; !hasPVC {
			orphaned = append(orphaned, OrphanedVolumeInfo{
				VolumeInfo: vol,
				PVCName:    pv.PVCName,
				Namespace:  pv.PVCNs,
				Reason:     "PVC deleted but PV remains",
			})
		}

		// If we get here, the volume has both PV and PVC - not orphaned
	}

	return orphaned
}

func outputOrphanedVolumes(volumes []OrphanedVolumeInfo, format string) error {
	if len(volumes) == 0 {
		fmt.Println("No orphaned volumes found")
		return nil
	}

	switch format {
	case outputFormatJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(volumes)

	case outputFormatYAML:
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		return enc.Encode(volumes)

	case outputFormatTable, "":
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		//nolint:errcheck // writing to tabwriter for stdout
		_, _ = fmt.Fprintln(w, "DATASET\tVOLUME_ID\tPROTOCOL\tCAPACITY\tADOPTABLE\tREASON")
		for i := range volumes {
			v := &volumes[i]
			adoptable := ""
			if v.Adoptable {
				adoptable = valueTrue
			}
			//nolint:errcheck // writing to tabwriter for stdout
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				v.Dataset, v.VolumeID, v.Protocol, v.CapacityHuman, adoptable, v.Reason)
		}
		return w.Flush()

	default:
		return fmt.Errorf("%w: %s", errOrphanedUnknownOutputFormat, format)
	}
}
