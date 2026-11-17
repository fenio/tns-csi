package driver

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRaiseISCSIDiskTimeout(t *testing.T) {
	tests := []struct {
		name        string
		current     string
		want        string
		replacement int
	}{
		{"raised to cover replacement_timeout", "30\n", "60", 300},
		{"rounded up", "30\n", "61", 301},
		{"higher host value kept", "90\n", "90\n", 300},
		{"disabled leaves it alone", "30\n", "30\n", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := t.TempDir()
			orig := sysBlockPath
			sysBlockPath = block
			t.Cleanup(func() { sysBlockPath = orig })

			attr := filepath.Join(block, "sdb", "device", "timeout")
			if err := os.MkdirAll(filepath.Dir(attr), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(attr, []byte(tt.current), 0o600); err != nil {
				t.Fatal(err)
			}

			s := &NodeService{iscsiReplacementTimeout: tt.replacement}
			if err := s.raiseISCSIDiskTimeout("/dev/sdb"); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got, err := os.ReadFile(attr)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("timeout = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("missing attribute fails", func(t *testing.T) {
		orig := sysBlockPath
		sysBlockPath = t.TempDir()
		t.Cleanup(func() { sysBlockPath = orig })
		s := &NodeService{iscsiReplacementTimeout: 300}
		if err := s.raiseISCSIDiskTimeout("/dev/sdb"); err == nil {
			t.Fatal("expected an error when the sysfs attribute is missing")
		}
	})
}
