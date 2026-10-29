package framework

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fenio/tns-csi/pkg/tnsapi"
)

// The fencing probe (#299) records what TrueNAS does to a node that is already logged in
// when its access to a volume is revoked: does the session end, can it keep writing, is a
// new login refused. It changes ACLs on the TrueNAS, so it runs only when asked for
// (E2E_FENCING_PROBE=1), and it logs what it sees rather than asserting an outcome.

// probeMountPath is where the probe pods mount the volume.
const probeMountPath = "/data"

// errNoID is returned when an API result has no numeric id.
var errNoID = errors.New("API result has no numeric id")

// FencingProbeEnabled reports whether the manual fencing probe should run.
func FencingProbeEnabled() bool { return os.Getenv("E2E_FENCING_PROBE") != "" }

// ProbeCall calls a TrueNAS API method and logs the result or error as JSON.
func ProbeCall(ctx context.Context, c *tnsapi.Client, method string, params ...any) (any, error) {
	if params == nil {
		params = []any{}
	}
	var out any
	err := c.Call(ctx, method, params, &out)
	b, marshalErr := json.Marshal(out)
	if marshalErr != nil {
		b = []byte(fmt.Sprint(out))
	}
	ginkgo.GinkgoWriter.Printf("PROBE API %s %v -> %s (err: %v)\n", method, params, b, err)
	return out, err
}

// ProbeID returns the "id" field of an API result.
func ProbeID(result any) (int, error) {
	if m, ok := result.(map[string]any); ok {
		if id, ok := m["id"].(float64); ok {
			return int(id), nil
		}
	}
	return 0, fmt.Errorf("%w: %v", errNoID, result)
}

// ProbeMethods logs the API methods under the given prefixes, plus any whose names suggest
// they end sessions, so the run shows what this TrueNAS release offers.
func ProbeMethods(ctx context.Context, c *tnsapi.Client, prefixes ...string) {
	var methods map[string]any
	if err := c.Call(ctx, "core.get_methods", []any{}, &methods); err != nil {
		ginkgo.GinkgoWriter.Printf("PROBE core.get_methods failed: %v\n", err)
		return
	}
	var names []string
	for name := range methods {
		keep := false
		for _, word := range []string{"session", "logout", "terminate", "kick", "disconnect"} {
			keep = keep || strings.Contains(strings.ToLower(name), word)
		}
		for _, p := range prefixes {
			keep = keep || strings.HasPrefix(name, p)
		}
		if keep {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	ginkgo.GinkgoWriter.Printf("PROBE methods: %s\n", strings.Join(names, " "))
}

// ProbeMount creates an RWO PVC and a pod that mounts it at /data, waits until the pod is
// ready, and returns the pod and the PV's volume attributes.
func ProbeMount(ctx context.Context, f *Framework, storageClass, name string) (*corev1.Pod, map[string]string, error) {
	pvc, err := f.CreatePVC(ctx, PVCOptions{
		Name: name, StorageClassName: storageClass, Size: "1Gi",
		AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
	})
	if err != nil {
		return nil, nil, err
	}
	if bindErr := f.K8s.WaitForPVCBound(ctx, pvc.Name, 3*time.Minute); bindErr != nil {
		return nil, nil, bindErr
	}
	pod, err := f.CreatePod(ctx, PodOptions{Name: name + "-pod", PVCName: pvc.Name, MountPath: probeMountPath})
	if err != nil {
		return nil, nil, err
	}
	if readyErr := f.K8s.WaitForPodReady(ctx, pod.Name, 3*time.Minute); readyErr != nil {
		return nil, nil, readyErr
	}
	if pvc, err = f.K8s.GetPVC(ctx, pvc.Name); err != nil {
		return nil, nil, err
	}
	pv, err := f.K8s.GetPV(ctx, pvc.Spec.VolumeName)
	if err != nil {
		return nil, nil, err
	}
	ginkgo.GinkgoWriter.Printf("PROBE volume %s attributes: %v\n", pv.Name, pv.Spec.CSI.VolumeAttributes)
	return pod, pv.Spec.CSI.VolumeAttributes, nil
}

// ProbeWrite writes 4 MiB to the pod's volume with O_DIRECT and fsync, so the data has to
// reach TrueNAS, and logs whether that finished within a minute. It reports success.
func ProbeWrite(ctx context.Context, f *Framework, pod, label string) bool {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	start := time.Now()
	out, err := f.K8s.ExecInPod(ctx, pod, []string{"sh", "-c", fmt.Sprintf(
		"timeout 60 dd if=/dev/urandom of=/data/probe-%d bs=1M count=4 oflag=direct conv=fsync 2>&1", start.UnixNano())})
	ginkgo.GinkgoWriter.Printf("PROBE write [%s] took %s, err: %v, output: %s\n",
		label, time.Since(start).Round(time.Second), err, strings.TrimSpace(out))
	return err == nil
}

// ProbeRemount deletes the pod, which unstages the volume and logs the node out of
// TrueNAS, creates the pod again on the same PVC, and logs whether it becomes ready within
// timeout, with the pod's events if not. It reports whether the pod became ready.
func ProbeRemount(ctx context.Context, f *Framework, pod *corev1.Pod, label string, timeout time.Duration) (bool, error) {
	if err := f.K8s.DeletePod(ctx, pod.Name); err != nil {
		return false, err
	}
	if err := f.K8s.WaitForPodDeleted(ctx, pod.Name, 3*time.Minute); err != nil {
		return false, err
	}
	claim := pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName
	if _, err := f.CreatePod(ctx, PodOptions{Name: pod.Name, PVCName: claim, MountPath: probeMountPath}); err != nil {
		return false, err
	}
	err := f.K8s.WaitForPodReady(ctx, pod.Name, timeout)
	ginkgo.GinkgoWriter.Printf("PROBE remount [%s]: ready=%t (err: %v)\n", label, err == nil, err)
	if err != nil {
		events, listErr := f.K8s.Clientset().CoreV1().Events(f.Namespace()).List(ctx, metav1.ListOptions{
			FieldSelector: "involvedObject.name=" + pod.Name,
		})
		if listErr == nil {
			for i := range events.Items {
				e := &events.Items[i]
				ginkgo.GinkgoWriter.Printf("PROBE event %s %s: %s\n", e.Type, e.Reason, e.Message)
			}
		}
	}
	return err == nil, nil
}
