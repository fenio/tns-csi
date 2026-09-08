//go:build !darwin

package driver

import (
	"slices"
	"testing"
)

func TestGetNFSMountOptionsSELinux(t *testing.T) {
	const contextOption = `context="system_u:object_r:container_file_t:s0:c123,c456"`

	tests := []struct {
		name        string
		userOptions []string
		want        []string
	}{
		{
			name:        "context adds nosharecache",
			userOptions: []string{contextOption},
			want:        []string{contextOption, "vers=4.2", mountOptNolock, nfsMountOptNoShareCache},
		},
		{
			name:        "existing nosharecache is preserved",
			userOptions: []string{contextOption, nfsMountOptNoShareCache},
			want:        []string{contextOption, nfsMountOptNoShareCache, "vers=4.2", mountOptNolock},
		},
		{
			name:        "context overrides sharecache",
			userOptions: []string{contextOption, nfsMountOptShareCache},
			want:        []string{contextOption, "vers=4.2", mountOptNolock, nfsMountOptNoShareCache},
		},
		{
			name:        "sharecache without context is preserved",
			userOptions: []string{nfsMountOptShareCache},
			want:        []string{nfsMountOptShareCache, "vers=4.2", mountOptNolock},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getNFSMountOptions(tt.userOptions)
			if !slices.Equal(got, tt.want) {
				t.Errorf("getNFSMountOptions(%v) = %v, want %v", tt.userOptions, got, tt.want)
			}
		})
	}
}
