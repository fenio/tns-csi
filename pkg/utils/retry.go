// Package utils provides utility functions for the CSI driver.
//
//nolint:revive // Package name 'utils' is intentional for grouping utility functions.
package utils

import (
	"context"
	"errors"
	"fmt"
	"time"

	"k8s.io/klog/v2"
)

// RetryConfig configures retry behavior.
//
//nolint:govet // fieldalignment: field order prioritizes readability over memory optimization.
type RetryConfig struct {
	// MaxAttempts is the maximum number of attempts (including the first try).
	// Default: 3
	MaxAttempts int

	// InitialBackoff is the initial backoff duration.
	// Default: 1 second
	InitialBackoff time.Duration

	// MaxBackoff is the maximum backoff duration.
	// Default: 30 seconds
	MaxBackoff time.Duration

	// BackoffMultiplier is the multiplier for exponential backoff.
	// Default: 2.0
	BackoffMultiplier float64

	// RetryableFunc determines if an error is retryable.
	// If nil, all errors are considered retryable.
	RetryableFunc func(error) bool

	// OperationName is used for logging purposes.
	OperationName string
}

// DefaultRetryConfig returns a RetryConfig with sensible defaults.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:       3,
		InitialBackoff:    1 * time.Second,
		MaxBackoff:        30 * time.Second,
		BackoffMultiplier: 2.0,
		RetryableFunc:     nil, // Retry all errors by default
		OperationName:     "operation",
	}
}

// ErrMaxRetriesExceeded is returned when all retry attempts have been exhausted.
var ErrMaxRetriesExceeded = errors.New("max retries exceeded")

// WithRetry executes a function with retry logic and exponential backoff.
// It uses Go generics to support any return type.
//
// Usage:
//
//	result, err := WithRetry(ctx, config, func() (*MyType, error) {
//	    return client.DoSomething()
//	})
func WithRetry[T any](ctx context.Context, config RetryConfig, fn func() (T, error)) (T, error) {
	var zero T

	// Apply defaults if not set
	if config.MaxAttempts <= 0 {
		config.MaxAttempts = 3
	}
	if config.InitialBackoff <= 0 {
		config.InitialBackoff = 1 * time.Second
	}
	if config.MaxBackoff <= 0 {
		config.MaxBackoff = 30 * time.Second
	}
	if config.BackoffMultiplier <= 0 {
		config.BackoffMultiplier = 2.0
	}
	if config.OperationName == "" {
		config.OperationName = "operation"
	}

	var lastErr error
	backoff := config.InitialBackoff

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		// Check context before each attempt
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}

		result, err := fn()
		if err == nil {
			if attempt > 1 {
				klog.V(4).Infof("Retry: %s succeeded on attempt %d", config.OperationName, attempt)
			}
			return result, nil
		}

		lastErr = err

		// Check if error is retryable
		if config.RetryableFunc != nil && !config.RetryableFunc(err) {
			klog.V(4).Infof("Retry: %s failed with non-retryable error: %v", config.OperationName, err)
			return zero, err
		}

		// Don't wait after the last attempt
		if attempt < config.MaxAttempts {
			klog.V(4).Infof("Retry: %s failed on attempt %d/%d: %v, retrying in %v",
				config.OperationName, attempt, config.MaxAttempts, err, backoff)

			select {
			case <-time.After(backoff):
				// Continue to next attempt
			case <-ctx.Done():
				return zero, ctx.Err()
			}

			// Calculate next backoff with exponential increase
			backoff = time.Duration(float64(backoff) * config.BackoffMultiplier)
			if backoff > config.MaxBackoff {
				backoff = config.MaxBackoff
			}
		}
	}

	return zero, fmt.Errorf("%w: %s failed after %d attempts: %w",
		ErrMaxRetriesExceeded, config.OperationName, config.MaxAttempts, lastErr)
}

// WithRetryNoResult executes a function that returns only an error with retry logic.
//
// Usage:
//
//	err := WithRetryNoResult(ctx, config, func() error {
//	    return client.DeleteSomething()
//	})
func WithRetryNoResult(ctx context.Context, config RetryConfig, fn func() error) error {
	_, err := WithRetry(ctx, config, func() (struct{}, error) {
		return struct{}{}, fn()
	})
	return err
}

// IsRetryableNetworkError returns true if the error is a network-related error
// that should be retried (connection refused, timeout, etc.).
func IsRetryableNetworkError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return contains(errStr,
		"connection refused",
		"connection reset",
		"broken pipe",
		"i/o timeout",
		"network is unreachable",
		"no route to host",
		"connection timed out",
		"EOF",
		"use of closed network connection",
	)
}

// IsRetryableAPIError returns true if the error is a TrueNAS API error
// that should be retried (server busy, temporary failure, etc.).
func IsRetryableAPIError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return contains(errStr,
		"500", // Internal server error
		"502", // Bad gateway
		"503", // Service unavailable
		"504", // Gateway timeout
		"busy",
		"temporarily unavailable",
		"try again",
	)
}

// IsRetryableError combines network and API error checks.
// Use this as the default RetryableFunc for most operations.
func IsRetryableError(err error) bool {
	return IsRetryableNetworkError(err) || IsRetryableAPIError(err)
}

// contains checks if any of the substrings are present in the string.
func contains(s string, substrings ...string) bool {
	for _, sub := range substrings {
		if len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
