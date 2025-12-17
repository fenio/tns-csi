package mount

import (
	"testing"
)

func TestJoinMountOptions(t *testing.T) {
	//nolint:govet // Field alignment not critical for test structs
	tests := []struct {
		name    string
		options []string
		want    string
	}{
		{
			name:    "empty options",
			options: []string{},
			want:    "",
		},
		{
			name:    "nil options",
			options: nil,
			want:    "",
		},
		{
			name:    "single option",
			options: []string{"ro"},
			want:    "ro",
		},
		{
			name:    "two options",
			options: []string{"ro", "noexec"},
			want:    "ro,noexec",
		},
		{
			name:    "multiple options",
			options: []string{"ro", "noexec", "nosuid", "nodev"},
			want:    "ro,noexec,nosuid,nodev",
		},
		{
			name:    "options with values",
			options: []string{"vers=4.1", "rsize=1048576", "wsize=1048576"},
			want:    "vers=4.1,rsize=1048576,wsize=1048576",
		},
		{
			name:    "NFS typical options",
			options: []string{"nfsvers=4.1", "hard", "timeo=600", "retrans=2"},
			want:    "nfsvers=4.1,hard,timeo=600,retrans=2",
		},
		{
			name:    "empty string in options",
			options: []string{"ro", "", "noexec"},
			want:    "ro,,noexec",
		},
		{
			name:    "options with spaces (unusual but valid)",
			options: []string{"option with space", "normal"},
			want:    "option with space,normal",
		},
		{
			name:    "options with commas (edge case)",
			options: []string{"opt1", "opt2,opt3"}, // Would result in opt1,opt2,opt3
			want:    "opt1,opt2,opt3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := JoinMountOptions(tt.options)
			if got != tt.want {
				t.Errorf("JoinMountOptions(%v) = %q, want %q", tt.options, got, tt.want)
			}
		})
	}
}

func TestJoinMountOptions_LargeList(t *testing.T) {
	// Test with a large number of options to ensure no performance issues
	options := make([]string, 100)
	for i := range options {
		options[i] = "opt"
	}

	result := JoinMountOptions(options)

	// Should have 99 commas
	commaCount := 0
	for _, c := range result {
		if c == ',' {
			commaCount++
		}
	}

	if commaCount != 99 {
		t.Errorf("Expected 99 commas for 100 options, got %d", commaCount)
	}
}
