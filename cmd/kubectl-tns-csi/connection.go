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

// connectionConfig holds TrueNAS connection parameters.
type connectionConfig struct {
	URL           string
	APIKey        string
	SkipTLSVerify bool
}

// getConnectionConfig resolves TrueNAS connection config from various sources.
// Priority: flags > secret > environment.
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

	// Try Kubernetes secret
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
