// Package framework provides utilities for E2E testing of the TrueNAS CSI driver.
package framework

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Configuration errors.
var (
	ErrMissingTrueNASHost   = errors.New("TRUENAS_HOST environment variable is required")
	ErrMissingTrueNASAPIKey = errors.New("TRUENAS_API_KEY environment variable is required")
)

// Config holds the configuration for E2E tests.
type Config struct {
	// TrueNAS connection settings
	TrueNASHost   string
	TrueNASAPIKey string
	TrueNASPool   string

	// CSI driver image settings
	CSIImageRepo string
	CSIImageTag  string

	// Kubernetes settings
	Kubeconfig string

	// Test settings
	Timeout time.Duration
}

// NewConfig creates a Config from environment variables.
func NewConfig() (*Config, error) {
	cfg := &Config{
		TrueNASHost:   os.Getenv("TRUENAS_HOST"),
		TrueNASAPIKey: os.Getenv("TRUENAS_API_KEY"),
		TrueNASPool:   getEnvOrDefault("TRUENAS_POOL", "csi"),
		CSIImageRepo:  getEnvOrDefault("CSI_IMAGE_REPO", "ghcr.io/fenio/tns-csi"),
		CSIImageTag:   getEnvOrDefault("CSI_IMAGE_TAG", "latest"),
		Kubeconfig:    getEnvOrDefault("KUBECONFIG", defaultKubeconfig()),
		Timeout:       parseDurationOrDefault(os.Getenv("TEST_TIMEOUT"), 5*time.Minute),
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Validate checks that all required configuration is present.
func (c *Config) Validate() error {
	if c.TrueNASHost == "" {
		return ErrMissingTrueNASHost
	}
	if c.TrueNASAPIKey == "" {
		return ErrMissingTrueNASAPIKey
	}
	return nil
}

// TrueNASURL returns the WebSocket URL for the TrueNAS API.
func (c *Config) TrueNASURL() string {
	return fmt.Sprintf("wss://%s/api/current", c.TrueNASHost)
}

// CSIImage returns the full Docker image reference.
func (c *Config) CSIImage() string {
	return fmt.Sprintf("%s:%s", c.CSIImageRepo, c.CSIImageTag)
}

// getEnvOrDefault returns the environment variable value or a default.
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// defaultKubeconfig returns the default kubeconfig path.
func defaultKubeconfig() string {
	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".kube", "config")
	}
	return ""
}

// parseDurationOrDefault parses a duration string or returns a default.
func parseDurationOrDefault(s string, defaultValue time.Duration) time.Duration {
	if s == "" {
		return defaultValue
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return defaultValue
	}
	return d
}
