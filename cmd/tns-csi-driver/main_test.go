package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAPIKeyFromFlag(t *testing.T) {
	key, err := loadAPIKey("  from-flag\n", "")
	if err != nil {
		t.Fatalf("loadAPIKey returned error: %v", err)
	}
	if key != "from-flag" {
		t.Fatalf("loadAPIKey returned %q, want trimmed flag value", key)
	}
}

func TestLoadAPIKeyFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api-key")
	if err := os.WriteFile(path, []byte("\nfrom-file\n"), 0o600); err != nil {
		t.Fatalf("write api key file: %v", err)
	}

	key, err := loadAPIKey("from-flag", path)
	if err != nil {
		t.Fatalf("loadAPIKey returned error: %v", err)
	}
	if key != "from-file" {
		t.Fatalf("loadAPIKey returned %q, want file value to take precedence", key)
	}
}

func TestLoadAPIKeyFromEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api-key")
	if err := os.WriteFile(path, []byte("\n\t"), 0o600); err != nil {
		t.Fatalf("write api key file: %v", err)
	}

	if _, err := loadAPIKey("", path); err == nil {
		t.Fatal("loadAPIKey returned nil error for empty file")
	}
}

func TestLoadAPIKeyFromMissingFile(t *testing.T) {
	if _, err := loadAPIKey("", filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("loadAPIKey returned nil error for missing file")
	}
}
