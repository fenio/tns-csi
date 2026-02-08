package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/fenio/tns-csi/pkg/tnsapi"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// Static errors for connection.
var (
	errURLNotConfigured    = errors.New("TrueNAS URL not configured (use --url, --secret, or TRUENAS_URL env var)")
	errAPIKeyNotConfigured = errors.New("TrueNAS API key not configured (use --api-key, --secret, or TRUENAS_API_KEY env var)")
	errInvalidSecretRef    = errors.New("invalid secret reference format, expected 'namespace/name'")
)

// Constants for auto-discovery.
const (
	defaultDriverNamespace = "kube-system"
	driverLabelSelector    = "app.kubernetes.io/name=tns-csi-driver"
)

// connectionConfig holds TrueNAS connection parameters.
type connectionConfig struct {
	URL           string
	APIKey        string
	SkipTLSVerify bool
}

// getConnectionConfig resolves TrueNAS connection config from various sources.
// Priority: flags > explicit secret > auto-discovered secret > environment.
func getConnectionConfig(ctx context.Context, url, apiKey, secretRef *string, skipTLSVerify *bool) (*connectionConfig, error) {
	cfg := &connectionConfig{
		SkipTLSVerify: true, // Default to skip for self-signed certs
	}

	if skipTLSVerify != nil {
		cfg.SkipTLSVerify = *skipTLSVerify
	}

	// Try flags first
	if url != nil && *url != "" {
		cfg.URL = *url
	}
	if apiKey != nil && *apiKey != "" {
		cfg.APIKey = *apiKey
	}

	// If we have both from flags, we're done
	if cfg.URL != "" && cfg.APIKey != "" {
		return cfg, nil
	}

	// Try explicitly provided Kubernetes secret
	if secretRef != nil && *secretRef != "" {
		secretCfg, err := getConfigFromSecret(ctx, *secretRef)
		if err != nil {
			return nil, fmt.Errorf("failed to read secret %s: %w", *secretRef, err)
		}
		if cfg.URL == "" {
			cfg.URL = secretCfg.URL
		}
		if cfg.APIKey == "" {
			cfg.APIKey = secretCfg.APIKey
		}
	}

	// If still missing config, try auto-discovery from installed driver
	if cfg.URL == "" || cfg.APIKey == "" {
		if discoveredCfg := autoDiscoverDriverSecret(ctx); discoveredCfg != nil {
			if cfg.URL == "" {
				cfg.URL = discoveredCfg.URL
			}
			if cfg.APIKey == "" {
				cfg.APIKey = discoveredCfg.APIKey
			}
		}
	}

	// Try environment variables as fallback
	if cfg.URL == "" {
		cfg.URL = os.Getenv("TRUENAS_URL")
	}
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("TRUENAS_API_KEY")
	}

	// Validate we have required config
	if cfg.URL == "" {
		return nil, errURLNotConfigured
	}
	if cfg.APIKey == "" {
		return nil, errAPIKeyNotConfigured
	}

	return cfg, nil
}

// getConfigFromSecret reads TrueNAS config from a Kubernetes secret.
// secretRef format: "namespace/name".
func getConfigFromSecret(ctx context.Context, secretRef string) (*connectionConfig, error) {
	parts := strings.SplitN(secretRef, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("%w: %q", errInvalidSecretRef, secretRef)
	}
	namespace, name := parts[0], parts[1]

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

	// Get the secret
	secret, err := clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get secret: %w", err)
	}

	cfg := &connectionConfig{}

	// Try common key names for URL
	for _, key := range []string{"url", "truenas-url", "TRUENAS_URL"} {
		if val, ok := secret.Data[key]; ok {
			cfg.URL = string(val)
			break
		}
	}

	// Try common key names for API key
	for _, key := range []string{"api-key", "apiKey", "truenas-api-key", "TRUENAS_API_KEY"} {
		if val, ok := secret.Data[key]; ok {
			cfg.APIKey = string(val)
			break
		}
	}

	return cfg, nil
}

// TrueNASClient wraps the tnsapi.Client to provide ClientInterface with Close.
type TrueNASClient struct {
	*tnsapi.Client
}

// connectToTrueNAS creates a TrueNAS API client with the given config.
// The client auto-connects on first API call.
func connectToTrueNAS(_ context.Context, cfg *connectionConfig) (*TrueNASClient, error) {
	//nolint:contextcheck // NewClient doesn't require context, connection is lazy
	client, err := tnsapi.NewClient(cfg.URL, cfg.APIKey, cfg.SkipTLSVerify)
	if err != nil {
		return nil, fmt.Errorf("failed to create TrueNAS client: %w", err)
	}

	return &TrueNASClient{Client: client}, nil
}

// autoDiscoverDriverSecret attempts to find the tns-csi driver secret automatically.
// It searches in the current kubectl context namespace first, then kube-system,
// then all namespaces as a fallback.
func autoDiscoverDriverSecret(ctx context.Context) *connectionConfig {
	// Build Kubernetes client
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	config, err := kubeConfig.ClientConfig()
	if err != nil {
		return nil // Can't connect to cluster, skip auto-discovery
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil
	}

	// Determine which namespaces to search and in what order.
	// Current context namespace first, then kube-system, then all namespaces.
	contextNamespace, _, nsErr := kubeConfig.Namespace()
	if nsErr != nil {
		contextNamespace = ""
	}
	namespacesToSearch := buildNamespaceSearchOrder(contextNamespace)

	// Search for secrets with tns-csi-driver labels in each namespace
	for _, ns := range namespacesToSearch {
		secrets, listErr := clientset.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{
			LabelSelector: driverLabelSelector,
		})
		if listErr == nil && len(secrets.Items) > 0 {
			return extractConfigFromSecretData(secrets.Items[0].Data)
		}
	}

	// Label search failed — try common secret name patterns in each namespace
	if cfg := tryCommonSecretNames(ctx, clientset, namespacesToSearch); cfg != nil {
		return cfg
	}

	// Final fallback: search all namespaces by label
	allSecrets, err := clientset.CoreV1().Secrets("").List(ctx, metav1.ListOptions{
		LabelSelector: driverLabelSelector,
	})
	if err == nil && len(allSecrets.Items) > 0 {
		return extractConfigFromSecretData(allSecrets.Items[0].Data)
	}

	return nil
}

// buildNamespaceSearchOrder returns deduplicated namespaces to search, prioritizing
// the current kubectl context namespace, then the default driver namespace.
func buildNamespaceSearchOrder(contextNamespace string) []string {
	namespaces := []string{}
	seen := map[string]bool{}
	for _, ns := range []string{contextNamespace, defaultDriverNamespace} {
		if ns != "" && !seen[ns] {
			namespaces = append(namespaces, ns)
			seen[ns] = true
		}
	}
	return namespaces
}

// tryCommonSecretNames tries to find secrets with common naming patterns
// across the given namespaces.
func tryCommonSecretNames(ctx context.Context, clientset *kubernetes.Clientset, namespaces []string) *connectionConfig {
	commonNames := []string{
		"tns-csi-driver-secret",
		"tns-csi-secret",
		"truenas-csi-secret",
	}

	for _, ns := range namespaces {
		for _, name := range commonNames {
			secret, err := clientset.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
			if err == nil {
				cfg := extractConfigFromSecretData(secret.Data)
				if cfg != nil && cfg.URL != "" && cfg.APIKey != "" {
					return cfg
				}
			}
		}
	}

	return nil
}

// discoverDriverNamespace finds the namespace where the tns-csi controller is running.
// It searches the current kubectl context namespace first, then kube-system,
// then all namespaces. Returns defaultDriverNamespace if nothing is found.
func discoverDriverNamespace(ctx context.Context) string {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	config, err := kubeConfig.ClientConfig()
	if err != nil {
		return defaultDriverNamespace
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return defaultDriverNamespace
	}

	contextNamespace, _, nsErr := kubeConfig.Namespace()
	if nsErr != nil {
		contextNamespace = ""
	}

	// Search candidate namespaces first
	for _, ns := range buildNamespaceSearchOrder(contextNamespace) {
		pods, listErr := clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
			LabelSelector: driverLabelSelector,
		})
		if listErr == nil && len(pods.Items) > 0 {
			return ns
		}
	}

	// All-namespace fallback
	pods, err := clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		LabelSelector: driverLabelSelector,
	})
	if err == nil && len(pods.Items) > 0 {
		return pods.Items[0].Namespace
	}

	return defaultDriverNamespace
}

// extractConfigFromSecretData extracts connection config from secret data.
func extractConfigFromSecretData(data map[string][]byte) *connectionConfig {
	cfg := &connectionConfig{}

	// Try common key names for URL
	for _, key := range []string{"url", "truenas-url", "TRUENAS_URL"} {
		if val, ok := data[key]; ok && len(val) > 0 {
			cfg.URL = string(val)
			break
		}
	}

	// Try common key names for API key
	for _, key := range []string{"api-key", "apiKey", "truenas-api-key", "TRUENAS_API_KEY"} {
		if val, ok := data[key]; ok && len(val) > 0 {
			cfg.APIKey = string(val)
			break
		}
	}

	if cfg.URL == "" && cfg.APIKey == "" {
		return nil
	}

	return cfg
}
