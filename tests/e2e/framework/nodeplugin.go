package framework

import (
	"context"
	"errors"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ErrNodePluginPod is returned when a node does not run exactly one tns-csi node pod.
var ErrNodePluginPod = errors.New("expected exactly one tns-csi node pod")

// nodePluginSelector matches the chart's node DaemonSet pods.
const nodePluginSelector = "app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node"

// NodePluginLogs returns the tns-csi-plugin container log of the driver's node pod on node,
// from since onward.
func (k *KubernetesClient) NodePluginLogs(ctx context.Context, node string, since metav1.Time) (string, error) {
	pods, err := k.clientset.CoreV1().Pods(helmNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: nodePluginSelector,
		FieldSelector: "spec.nodeName=" + node,
	})
	if err != nil {
		return "", err
	}
	if len(pods.Items) != 1 {
		return "", fmt.Errorf("%w on %s, found %d", ErrNodePluginPod, node, len(pods.Items))
	}
	req := k.clientset.CoreV1().Pods(helmNamespace).GetLogs(pods.Items[0].Name, &corev1.PodLogOptions{
		Container: "tns-csi-plugin",
		SinceTime: &since,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = stream.Close() }()
	out, err := io.ReadAll(stream)
	return string(out), err
}
