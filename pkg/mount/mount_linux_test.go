//go:build linux

package mount

import (
	"strings"
	"testing"
)

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
