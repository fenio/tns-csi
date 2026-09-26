//go:build linux

package mount

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func TestIsDeviceInMountInfo(t *testing.T) {
	tests := []struct {
		name      string
		mountInfo string
		major     uint32
		minor     uint32
		want      bool
		wantErr   bool
	}{
		{
			name:      "matching device",
			mountInfo: "36 25 259:2 / /var/lib/kubelet/plugins rw,relatime - ext4 /dev/nvme0n1 rw\n",
			major:     259,
			minor:     2,
			want:      true,
		},
		{
			name:      "different device",
			mountInfo: "36 25 8:1 / /var/lib/kubelet/plugins rw,relatime - ext4 /dev/sda1 rw\n",
			major:     259,
			minor:     2,
		},
		{
			name:      "malformed entry fails closed",
			mountInfo: "malformed\n",
			major:     259,
			minor:     2,
			wantErr:   true,
		},
		{
			name:      "malformed device number fails closed",
			mountInfo: "36 25 bad / /target rw - ext4 /dev/test rw\n",
			major:     259,
			minor:     2,
			wantErr:   true,
		},
		{
			name:      "invalid major number fails closed",
			mountInfo: "36 25 bad:2 / /target rw - ext4 /dev/test rw\n",
			major:     259,
			minor:     2,
			wantErr:   true,
		},
		{
			name:      "invalid minor number fails closed",
			mountInfo: "36 25 259:bad / /target rw - ext4 /dev/test rw\n",
			major:     259,
			minor:     2,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := isDeviceInMountInfo(tt.major, tt.minor, strings.NewReader(tt.mountInfo))
			if got != tt.want {
				t.Fatalf("isDeviceInMountInfo() = %v, want %v", got, tt.want)
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("isDeviceInMountInfo() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestIsDeviceInMountInfoReadError(t *testing.T) {
	if _, err := isDeviceInMountInfo(259, 2, errorReader{}); err == nil {
		t.Fatal("isDeviceInMountInfo() error = nil, want read error")
	}
}

func TestIsSourceMountedValidation(t *testing.T) {
	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := IsSourceMounted(ctx, "/dev/null"); !errors.Is(err, context.Canceled) {
			t.Fatalf("IsSourceMounted() error = %v, want context canceled", err)
		}
	})

	t.Run("missing source", func(t *testing.T) {
		if _, err := IsSourceMounted(context.Background(), "/dev/tns-csi-does-not-exist"); err == nil {
			t.Fatal("IsSourceMounted() error = nil, want stat error")
		}
	})

	t.Run("regular file", func(t *testing.T) {
		file, err := os.CreateTemp(t.TempDir(), "source")
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := IsSourceMounted(context.Background(), file.Name()); !errors.Is(err, errInvalidSourceDevice) {
			t.Fatalf("IsSourceMounted() error = %v, want invalid source device", err)
		}
	})

	t.Run("unmounted device", func(t *testing.T) {
		mounted, err := IsSourceMounted(context.Background(), "/dev/null")
		if err != nil {
			t.Fatal(err)
		}
		if mounted {
			t.Fatal("/dev/null unexpectedly reported as mounted")
		}
	})
}
