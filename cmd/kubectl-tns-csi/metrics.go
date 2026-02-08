package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// Static errors for metrics fetching.
var (
	errNoControllerPod        = errors.New("no controller pod found")
	errNoRunningControllerPod = errors.New("no running controller pod found")
)

// MetricsSummary contains parsed metrics for dashboard display.
//
//nolint:govet // field alignment not critical for display struct
type MetricsSummary struct {
	// Connection status
	WebSocketConnected     bool    `json:"websocketConnected"`
	WebSocketReconnects    int64   `json:"websocketReconnects"`
	ConnectionDurationSecs float64 `json:"connectionDurationSecs"`

	// Operation counts
	TotalOperations   int64 `json:"totalOperations"`
	SuccessOperations int64 `json:"successOperations"`
	ErrorOperations   int64 `json:"errorOperations"`

	// Volume operations by protocol
	NFSOperations    int64 `json:"nfsOperations"`
	NVMeOFOperations int64 `json:"nvmeofOperations"`
	ISCSIOperations  int64 `json:"iscsiOperations"`

	// Volume operations by type
	CreateOperations int64 `json:"createOperations"`
	DeleteOperations int64 `json:"deleteOperations"`
	ExpandOperations int64 `json:"expandOperations"`

	// Messages
	MessagesSent     int64 `json:"messagesSent"`
	MessagesReceived int64 `json:"messagesReceived"`

	// Raw metrics text (for raw view)
	RawMetrics string `json:"rawMetrics,omitempty"`

	// Error if metrics couldn't be fetched
	Error string `json:"error,omitempty"`
}

// Controller pod discovery constants.
const (
	controllerLabelSelector = "app.kubernetes.io/component=controller,app.kubernetes.io/name=tns-csi-driver"
	metricsPort             = "8080"
	metricsPath             = "/metrics"
)

// fetchControllerMetrics fetches metrics from the tns-csi controller pod.
func fetchControllerMetrics(ctx context.Context) (*MetricsSummary, error) {
	// Build Kubernetes client
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	config, err := kubeConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	// Discover which namespace the driver is running in
	driverNamespace := discoverDriverNamespace(ctx)

	// Find controller pod
	pods, err := clientset.CoreV1().Pods(driverNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: controllerLabelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list controller pods: %w", err)
	}

	if len(pods.Items) == 0 {
		// Try alternative label selector
		pods, err = clientset.CoreV1().Pods(driverNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: "app=tns-csi-controller",
		})
		if err != nil || len(pods.Items) == 0 {
			return nil, fmt.Errorf("%w in namespace %s", errNoControllerPod, driverNamespace)
		}
	}

	// Use first running pod
	var podName string
	for i := range pods.Items {
		if pods.Items[i].Status.Phase == "Running" {
			podName = pods.Items[i].Name
			break
		}
	}
	if podName == "" {
		return nil, errNoRunningControllerPod
	}

	// Fetch metrics via pod proxy
	result := clientset.CoreV1().Pods(driverNamespace).ProxyGet("http", podName, metricsPort, metricsPath, nil)
	rawData, err := result.DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch metrics from pod %s: %w", podName, err)
	}

	// Parse metrics
	summary := parsePrometheusMetrics(string(rawData))
	summary.RawMetrics = string(rawData)

	return summary, nil
}

// parsePrometheusMetrics parses Prometheus text format into MetricsSummary.
func parsePrometheusMetrics(data string) *MetricsSummary {
	summary := &MetricsSummary{}

	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()

		// Skip comments and empty lines
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}

		// Parse metric line
		parseMetricLine(line, summary)
	}

	// Calculate totals
	summary.TotalOperations = summary.SuccessOperations + summary.ErrorOperations

	return summary
}

// Regex patterns for parsing metrics.
var (
	metricValueRegex = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)\{?([^}]*)\}?\s+(.+)$`)
)

// parseMetricLine parses a single Prometheus metric line.
func parseMetricLine(line string, summary *MetricsSummary) {
	matches := metricValueRegex.FindStringSubmatch(line)
	if len(matches) != 4 {
		return
	}

	metricName := matches[1]
	labels := matches[2]
	valueStr := matches[3]

	value, err := strconv.ParseFloat(valueStr, 64)
	if err != nil {
		return
	}

	switch metricName {
	case "tns_csi_websocket_connection_status":
		summary.WebSocketConnected = value == 1

	case "tns_csi_websocket_reconnections_total":
		summary.WebSocketReconnects = int64(value)

	case "tns_csi_websocket_connection_duration_seconds":
		summary.ConnectionDurationSecs = value

	case "tns_csi_websocket_messages_total":
		if strings.Contains(labels, `direction="sent"`) {
			summary.MessagesSent = int64(value)
		} else if strings.Contains(labels, `direction="received"`) {
			summary.MessagesReceived = int64(value)
		}

	case "tns_csi_volume_operations_total":
		intVal := int64(value)

		// Count by status
		if strings.Contains(labels, `status="success"`) {
			summary.SuccessOperations += intVal
		} else if strings.Contains(labels, `status="error"`) {
			summary.ErrorOperations += intVal
		}

		// Count by protocol
		//nolint:gocritic // if-else chain clearer for substring matching
		if strings.Contains(labels, `protocol="nfs"`) {
			summary.NFSOperations += intVal
		} else if strings.Contains(labels, `protocol="nvmeof"`) {
			summary.NVMeOFOperations += intVal
		} else if strings.Contains(labels, `protocol="iscsi"`) {
			summary.ISCSIOperations += intVal
		}

		// Count by operation type
		//nolint:gocritic // if-else chain clearer for substring matching
		if strings.Contains(labels, `operation="create"`) {
			summary.CreateOperations += intVal
		} else if strings.Contains(labels, `operation="delete"`) {
			summary.DeleteOperations += intVal
		} else if strings.Contains(labels, `operation="expand"`) {
			summary.ExpandOperations += intVal
		}
	}
}

// fetchRawMetrics fetches raw metrics text from the controller.
func fetchRawMetrics(ctx context.Context) (string, error) {
	summary, err := fetchControllerMetrics(ctx)
	if err != nil {
		return "", err
	}
	return summary.RawMetrics, nil
}

// Ensure io import is used (for interface compliance).
var _ io.Reader = (*strings.Reader)(nil)
