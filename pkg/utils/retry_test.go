//nolint:revive // Package name 'utils' is intentional for grouping utility functions.
package utils

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDefaultRetryConfig(t *testing.T) {
	config := DefaultRetryConfig()

	if config.MaxAttempts != 3 {
		t.Errorf("Expected MaxAttempts=3, got %d", config.MaxAttempts)
	}
	if config.InitialBackoff != 1*time.Second {
		t.Errorf("Expected InitialBackoff=1s, got %v", config.InitialBackoff)
	}
	if config.MaxBackoff != 30*time.Second {
		t.Errorf("Expected MaxBackoff=30s, got %v", config.MaxBackoff)
	}
	if config.BackoffMultiplier != 2.0 {
		t.Errorf("Expected BackoffMultiplier=2.0, got %v", config.BackoffMultiplier)
	}
	if config.RetryableFunc != nil {
		t.Error("Expected RetryableFunc to be nil by default")
	}
	if config.OperationName != "operation" {
		t.Errorf("Expected OperationName='operation', got %q", config.OperationName)
	}
}

func TestDeletionRetryConfig(t *testing.T) {
	config := DeletionRetryConfig("delete-dataset")

	if config.MaxAttempts != 12 {
		t.Errorf("Expected MaxAttempts=12, got %d", config.MaxAttempts)
	}
	if config.InitialBackoff != 5*time.Second {
		t.Errorf("Expected InitialBackoff=5s, got %v", config.InitialBackoff)
	}
	if config.MaxBackoff != 5*time.Second {
		t.Errorf("Expected MaxBackoff=5s (fixed interval), got %v", config.MaxBackoff)
	}
	if config.BackoffMultiplier != 1.0 {
		t.Errorf("Expected BackoffMultiplier=1.0 (no increase), got %v", config.BackoffMultiplier)
	}
	if config.RetryableFunc == nil {
		t.Error("Expected RetryableFunc to be set for deletion config")
	}
	if config.OperationName != "delete-dataset" {
		t.Errorf("Expected OperationName='delete-dataset', got %q", config.OperationName)
	}
}

func TestWithRetry_Success(t *testing.T) {
	config := RetryConfig{
		MaxAttempts:       3,
		InitialBackoff:    1 * time.Millisecond,
		MaxBackoff:        10 * time.Millisecond,
		BackoffMultiplier: 2.0,
		OperationName:     "test-op",
	}

	callCount := 0
	result, err := WithRetry(context.Background(), config, func() (string, error) {
		callCount++
		return "success", nil
	})

	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if result != "success" {
		t.Errorf("Expected result='success', got %q", result)
	}
	if callCount != 1 {
		t.Errorf("Expected 1 call, got %d", callCount)
	}
}

func TestWithRetry_EventualSuccess(t *testing.T) {
	config := RetryConfig{
		MaxAttempts:       5,
		InitialBackoff:    1 * time.Millisecond,
		MaxBackoff:        10 * time.Millisecond,
		BackoffMultiplier: 2.0,
		OperationName:     "test-op",
	}

	callCount := 0
	result, err := WithRetry(context.Background(), config, func() (int, error) {
		callCount++
		if callCount < 3 {
			return 0, errors.New("transient error")
		}
		return 42, nil
	})

	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if result != 42 {
		t.Errorf("Expected result=42, got %d", result)
	}
	if callCount != 3 {
		t.Errorf("Expected 3 calls, got %d", callCount)
	}
}

func TestWithRetry_AllAttemptsFail(t *testing.T) {
	config := RetryConfig{
		MaxAttempts:       3,
		InitialBackoff:    1 * time.Millisecond,
		MaxBackoff:        10 * time.Millisecond,
		BackoffMultiplier: 2.0,
		OperationName:     "failing-op",
	}

	callCount := 0
	_, err := WithRetry(context.Background(), config, func() (string, error) {
		callCount++
		return "", errors.New("persistent error")
	})

	if err == nil {
		t.Error("Expected error, got nil")
	}
	if !errors.Is(err, ErrMaxRetriesExceeded) {
		t.Errorf("Expected ErrMaxRetriesExceeded, got %v", err)
	}
	if callCount != 3 {
		t.Errorf("Expected 3 calls, got %d", callCount)
	}
}

func TestWithRetry_NonRetryableError(t *testing.T) {
	config := RetryConfig{
		MaxAttempts:       5,
		InitialBackoff:    1 * time.Millisecond,
		MaxBackoff:        10 * time.Millisecond,
		BackoffMultiplier: 2.0,
		OperationName:     "test-op",
		RetryableFunc: func(err error) bool {
			return err.Error() != "non-retryable"
		},
	}

	callCount := 0
	_, err := WithRetry(context.Background(), config, func() (string, error) {
		callCount++
		return "", errors.New("non-retryable")
	})

	if err == nil {
		t.Error("Expected error, got nil")
	}
	if err.Error() != "non-retryable" {
		t.Errorf("Expected 'non-retryable' error, got %v", err)
	}
	// Should not retry non-retryable errors
	if callCount != 1 {
		t.Errorf("Expected 1 call (no retries), got %d", callCount)
	}
}

func TestWithRetry_ContextCanceled(t *testing.T) {
	config := RetryConfig{
		MaxAttempts:       10,
		InitialBackoff:    100 * time.Millisecond,
		MaxBackoff:        1 * time.Second,
		BackoffMultiplier: 2.0,
		OperationName:     "test-op",
	}

	ctx, cancel := context.WithCancel(context.Background())

	callCount := 0
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := WithRetry(ctx, config, func() (string, error) {
		callCount++
		return "", errors.New("error")
	})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Expected context.Canceled, got %v", err)
	}
}

func TestWithRetry_ContextCanceledBeforeStart(t *testing.T) {
	config := RetryConfig{
		MaxAttempts:       3,
		InitialBackoff:    1 * time.Millisecond,
		MaxBackoff:        10 * time.Millisecond,
		BackoffMultiplier: 2.0,
		OperationName:     "test-op",
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel before starting

	callCount := 0
	_, err := WithRetry(ctx, config, func() (string, error) {
		callCount++
		return "success", nil
	})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Expected context.Canceled, got %v", err)
	}
	if callCount != 0 {
		t.Errorf("Expected 0 calls (context already canceled), got %d", callCount)
	}
}

func TestWithRetry_DefaultsApplied(t *testing.T) {
	// Use zero values to test defaults
	config := RetryConfig{}

	callCount := 0
	_, err := WithRetry(context.Background(), config, func() (string, error) {
		callCount++
		return "", errors.New("error")
	})

	// Default MaxAttempts is 3
	if callCount != 3 {
		t.Errorf("Expected 3 calls (default MaxAttempts), got %d", callCount)
	}
	if !errors.Is(err, ErrMaxRetriesExceeded) {
		t.Errorf("Expected ErrMaxRetriesExceeded, got %v", err)
	}
}

func TestWithRetryNoResult_Success(t *testing.T) {
	config := RetryConfig{
		MaxAttempts:       3,
		InitialBackoff:    1 * time.Millisecond,
		MaxBackoff:        10 * time.Millisecond,
		BackoffMultiplier: 2.0,
		OperationName:     "test-op",
	}

	callCount := 0
	err := WithRetryNoResult(context.Background(), config, func() error {
		callCount++
		return nil
	})

	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if callCount != 1 {
		t.Errorf("Expected 1 call, got %d", callCount)
	}
}

func TestWithRetryNoResult_EventualSuccess(t *testing.T) {
	config := RetryConfig{
		MaxAttempts:       5,
		InitialBackoff:    1 * time.Millisecond,
		MaxBackoff:        10 * time.Millisecond,
		BackoffMultiplier: 2.0,
		OperationName:     "test-op",
	}

	callCount := 0
	err := WithRetryNoResult(context.Background(), config, func() error {
		callCount++
		if callCount < 3 {
			return errors.New("transient error")
		}
		return nil
	})

	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if callCount != 3 {
		t.Errorf("Expected 3 calls, got %d", callCount)
	}
}

func TestWithRetryNoResult_AllAttemptsFail(t *testing.T) {
	config := RetryConfig{
		MaxAttempts:       3,
		InitialBackoff:    1 * time.Millisecond,
		MaxBackoff:        10 * time.Millisecond,
		BackoffMultiplier: 2.0,
		OperationName:     "failing-op",
	}

	callCount := 0
	err := WithRetryNoResult(context.Background(), config, func() error {
		callCount++
		return errors.New("persistent error")
	})

	if err == nil {
		t.Error("Expected error, got nil")
	}
	if !errors.Is(err, ErrMaxRetriesExceeded) {
		t.Errorf("Expected ErrMaxRetriesExceeded, got %v", err)
	}
	if callCount != 3 {
		t.Errorf("Expected 3 calls, got %d", callCount)
	}
}

func TestIsRetryableNetworkError(t *testing.T) {
	//nolint:govet // Field alignment not critical for test structs
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "connection refused",
			err:  errors.New("dial tcp: connection refused"),
			want: true,
		},
		{
			name: "connection reset",
			err:  errors.New("read tcp: connection reset by peer"),
			want: true,
		},
		{
			name: "broken pipe",
			err:  errors.New("write tcp: broken pipe"),
			want: true,
		},
		{
			name: "i/o timeout",
			err:  errors.New("read: i/o timeout"),
			want: true,
		},
		{
			name: "network unreachable",
			err:  errors.New("connect: network is unreachable"),
			want: true,
		},
		{
			name: "no route to host",
			err:  errors.New("connect: no route to host"),
			want: true,
		},
		{
			name: "connection timed out",
			err:  errors.New("dial tcp: connection timed out"),
			want: true,
		},
		{
			name: "EOF",
			err:  errors.New("unexpected EOF"),
			want: true,
		},
		{
			name: "closed connection",
			err:  errors.New("use of closed network connection"),
			want: true,
		},
		{
			name: "generic error",
			err:  errors.New("some random error"),
			want: false,
		},
		{
			name: "validation error",
			err:  errors.New("invalid input parameter"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsRetryableNetworkError(tt.err)
			if got != tt.want {
				t.Errorf("IsRetryableNetworkError(%q) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsRetryableAPIError(t *testing.T) {
	//nolint:govet // Field alignment not critical for test structs
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "500 internal server error",
			err:  errors.New("API returned 500: Internal Server Error"),
			want: true,
		},
		{
			name: "502 bad gateway",
			err:  errors.New("API returned 502: Bad Gateway"),
			want: true,
		},
		{
			name: "503 service unavailable",
			err:  errors.New("API returned 503: Service Unavailable"),
			want: true,
		},
		{
			name: "504 gateway timeout",
			err:  errors.New("API returned 504: Gateway Timeout"),
			want: true,
		},
		{
			name: "server busy",
			err:  errors.New("server is busy, try again later"),
			want: true,
		},
		{
			name: "temporarily unavailable",
			err:  errors.New("service is temporarily unavailable"),
			want: true,
		},
		{
			name: "try again",
			err:  errors.New("operation failed, please try again"),
			want: true,
		},
		{
			name: "400 bad request",
			err:  errors.New("API returned 400: Bad Request"),
			want: false,
		},
		{
			name: "404 not found",
			err:  errors.New("API returned 404: Not Found"),
			want: false,
		},
		{
			name: "generic error",
			err:  errors.New("some random error"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsRetryableAPIError(tt.err)
			if got != tt.want {
				t.Errorf("IsRetryableAPIError(%q) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsRetryableError(t *testing.T) {
	//nolint:govet // Field alignment not critical for test structs
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "network error - connection refused",
			err:  errors.New("dial tcp: connection refused"),
			want: true,
		},
		{
			name: "API error - 503",
			err:  errors.New("API returned 503: Service Unavailable"),
			want: true,
		},
		{
			name: "non-retryable error",
			err:  errors.New("invalid volume name"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsRetryableError(tt.err)
			if got != tt.want {
				t.Errorf("IsRetryableError(%q) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsBusyResourceError(t *testing.T) {
	//nolint:govet // Field alignment not critical for test structs
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "dataset is busy",
			err:  errors.New("cannot destroy 'tank/dataset': dataset is busy"),
			want: true,
		},
		{
			name: "target is busy",
			err:  errors.New("cannot remove target: target is busy"),
			want: true,
		},
		{
			name: "resource busy",
			err:  errors.New("resource busy"),
			want: true,
		},
		{
			name: "EBUSY",
			err:  errors.New("operation failed: EBUSY"),
			want: true,
		},
		{
			name: "Device or resource busy",
			err:  errors.New("Device or resource busy"),
			want: true,
		},
		{
			name: "pool is busy",
			err:  errors.New("cannot export pool: pool is busy"),
			want: true,
		},
		{
			name: "filesystem is busy",
			err:  errors.New("cannot unmount: filesystem is busy"),
			want: true,
		},
		{
			name: "generic error",
			err:  errors.New("some other error"),
			want: false,
		},
		{
			name: "not found error",
			err:  errors.New("dataset not found"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsBusyResourceError(tt.err)
			if got != tt.want {
				t.Errorf("IsBusyResourceError(%q) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsRetryableDeletionError(t *testing.T) {
	//nolint:govet // Field alignment not critical for test structs
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "busy resource error",
			err:  errors.New("dataset is busy"),
			want: true,
		},
		{
			name: "network error",
			err:  errors.New("connection refused"),
			want: true,
		},
		{
			name: "API 503 error",
			err:  errors.New("API returned 503"),
			want: true,
		},
		{
			name: "non-retryable error",
			err:  errors.New("permission denied"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsRetryableDeletionError(tt.err)
			if got != tt.want {
				t.Errorf("IsRetryableDeletionError(%q) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestContains(t *testing.T) {
	tests := []struct {
		name       string
		s          string
		substrings []string
		want       bool
	}{
		{
			name:       "empty string and empty substrings",
			s:          "",
			substrings: []string{},
			want:       false,
		},
		{
			name:       "empty string with substrings",
			s:          "",
			substrings: []string{"test"},
			want:       false,
		},
		{
			name:       "string with empty substrings",
			s:          "test string",
			substrings: []string{},
			want:       false,
		},
		{
			name:       "single match",
			s:          "hello world",
			substrings: []string{"world"},
			want:       true,
		},
		{
			name:       "no match",
			s:          "hello world",
			substrings: []string{"foo", "bar"},
			want:       false,
		},
		{
			name:       "multiple substrings one match",
			s:          "connection refused by server",
			substrings: []string{"timeout", "refused", "closed"},
			want:       true,
		},
		{
			name:       "substring longer than string",
			s:          "short",
			substrings: []string{"this is much longer"},
			want:       false,
		},
		{
			name:       "exact match",
			s:          "exact",
			substrings: []string{"exact"},
			want:       true,
		},
		{
			name:       "case sensitive no match",
			s:          "Hello World",
			substrings: []string{"hello"},
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := contains(tt.s, tt.substrings...)
			if got != tt.want {
				t.Errorf("contains(%q, %v) = %v, want %v", tt.s, tt.substrings, got, tt.want)
			}
		})
	}
}

func TestWithRetry_BackoffCapping(t *testing.T) {
	config := RetryConfig{
		MaxAttempts:       5,
		InitialBackoff:    10 * time.Millisecond,
		MaxBackoff:        15 * time.Millisecond, // Low cap to test capping
		BackoffMultiplier: 10.0,                  // Aggressive multiplier
		OperationName:     "test-op",
	}

	callCount := 0
	start := time.Now()
	_, _ = WithRetry(context.Background(), config, func() (string, error) {
		callCount++
		return "", errors.New("error")
	})
	elapsed := time.Since(start)

	// With aggressive multiplier but capped at 15ms:
	// Attempt 1: fail, wait 10ms
	// Attempt 2: fail, wait 15ms (capped from 100ms)
	// Attempt 3: fail, wait 15ms (capped)
	// Attempt 4: fail, wait 15ms (capped)
	// Attempt 5: fail, done
	// Total wait: ~55ms (10 + 15 + 15 + 15)

	// We should have called the function 5 times
	if callCount != 5 {
		t.Errorf("Expected 5 calls, got %d", callCount)
	}

	// Total time should be less than if backoff wasn't capped
	// Without cap: 10 + 100 + 1000 + 10000 = 11110ms
	// With cap: 10 + 15 + 15 + 15 = 55ms
	// Allow some buffer for test execution
	if elapsed > 200*time.Millisecond {
		t.Errorf("Expected elapsed time ~55ms (with capped backoff), got %v", elapsed)
	}
}

func TestWithRetry_GenericTypes(t *testing.T) {
	config := RetryConfig{
		MaxAttempts:       2,
		InitialBackoff:    1 * time.Millisecond,
		MaxBackoff:        10 * time.Millisecond,
		BackoffMultiplier: 2.0,
		OperationName:     "test-op",
	}

	t.Run("struct return type", func(t *testing.T) {
		//nolint:govet // Field alignment not critical for test structs
		type Result struct {
			Value int
			Name  string
		}

		result, err := WithRetry(context.Background(), config, func() (Result, error) {
			return Result{Value: 42, Name: "test"}, nil
		})

		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
		if result.Value != 42 || result.Name != "test" {
			t.Errorf("Expected Result{42, test}, got %+v", result)
		}
	})

	t.Run("slice return type", func(t *testing.T) {
		result, err := WithRetry(context.Background(), config, func() ([]string, error) {
			return []string{"a", "b", "c"}, nil
		})

		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
		if len(result) != 3 {
			t.Errorf("Expected slice of length 3, got %d", len(result))
		}
	})

	t.Run("pointer return type", func(t *testing.T) {
		result, err := WithRetry(context.Background(), config, func() (*int, error) {
			v := 123
			return &v, nil
		})

		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
		if result == nil || *result != 123 {
			t.Errorf("Expected pointer to 123, got %v", result)
		}
	})

	t.Run("nil pointer on failure", func(t *testing.T) {
		result, err := WithRetry(context.Background(), config, func() (*int, error) {
			return nil, errors.New("error")
		})

		if err == nil {
			t.Error("Expected error, got nil")
		}
		if result != nil {
			t.Errorf("Expected nil result on failure, got %v", result)
		}
	})
}
