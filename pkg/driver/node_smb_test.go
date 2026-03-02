package driver

import (
	"os"
	"strings"
	"testing"
)

func TestWriteSMBCredentialsFile(t *testing.T) {
	t.Run("all fields", func(t *testing.T) {
		secrets := map[string]string{
			"username": "admin",
			"password": "s3cret",
			"domain":   "WORKGROUP",
		}
		path, err := writeSMBCredentialsFile(secrets)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer os.Remove(path)

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("failed to read credentials file: %v", err)
		}
		content := string(data)

		if !strings.Contains(content, "username=admin\n") {
			t.Errorf("expected username=admin, got: %s", content)
		}
		if !strings.Contains(content, "password=s3cret\n") {
			t.Errorf("expected password=s3cret, got: %s", content)
		}
		if !strings.Contains(content, "domain=WORKGROUP\n") {
			t.Errorf("expected domain=WORKGROUP, got: %s", content)
		}

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("failed to stat credentials file: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("expected file permissions 0600, got %o", perm)
		}
	})

	t.Run("no domain", func(t *testing.T) {
		secrets := map[string]string{
			"username": "user1",
			"password": "pass1",
		}
		path, err := writeSMBCredentialsFile(secrets)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer os.Remove(path)

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("failed to read credentials file: %v", err)
		}
		content := string(data)

		if strings.Contains(content, "domain=") {
			t.Errorf("expected no domain line, got: %s", content)
		}
		if !strings.Contains(content, "username=user1\n") {
			t.Errorf("expected username=user1, got: %s", content)
		}
	})

	t.Run("empty username error", func(t *testing.T) {
		secrets := map[string]string{
			"password": "pass1",
		}
		_, err := writeSMBCredentialsFile(secrets)
		if err == nil {
			t.Fatal("expected error for empty username")
		}
		if !strings.Contains(err.Error(), "username") {
			t.Errorf("expected error about username, got: %v", err)
		}
	})

	t.Run("empty domain is omitted", func(t *testing.T) {
		secrets := map[string]string{
			"username": "user1",
			"password": "pass1",
			"domain":   "",
		}
		path, err := writeSMBCredentialsFile(secrets)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer os.Remove(path)

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("failed to read credentials file: %v", err)
		}
		if strings.Contains(string(data), "domain=") {
			t.Errorf("expected no domain line for empty domain, got: %s", string(data))
		}
	})
}

func TestIsSMBKerberosAuth(t *testing.T) {
	tests := []struct {
		name     string
		options  []string
		expected bool
	}{
		{"krb5", []string{"vers=3.0", "sec=krb5"}, true},
		{"krb5i", []string{"sec=krb5i"}, true},
		{"krb5p", []string{"sec=krb5p", "seal"}, true},
		{"ntlmssp", []string{"sec=ntlmssp"}, false},
		{"no sec option", []string{"vers=3.0"}, false},
		{"empty options", []string{}, false},
		{"nil options", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSMBKerberosAuth(tt.options)
			if got != tt.expected {
				t.Errorf("isSMBKerberosAuth(%v) = %v, want %v", tt.options, got, tt.expected)
			}
		})
	}
}
