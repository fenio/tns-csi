package driver

import (
	"testing"
)

func TestParseNameTemplateConfig(t *testing.T) {
	tests := []struct {
		params      map[string]string
		name        string
		errContains string
		wantNil     bool
		wantErr     bool
	}{
		{
			name:    "no templating configured",
			params:  map[string]string{},
			wantNil: true,
		},
		{
			name: "only prefix",
			params: map[string]string{
				ParamNamePrefix: "prod-",
			},
			wantNil: false,
		},
		{
			name: "only suffix",
			params: map[string]string{
				ParamNameSuffix: "-data",
			},
			wantNil: false,
		},
		{
			name: "prefix and suffix",
			params: map[string]string{
				ParamNamePrefix: "prod-",
				ParamNameSuffix: "-data",
			},
			wantNil: false,
		},
		{
			name: "valid template",
			params: map[string]string{
				ParamNameTemplate: "{{ .PVCNamespace }}-{{ .PVCName }}",
			},
			wantNil: false,
		},
		{
			name: "invalid template syntax",
			params: map[string]string{
				ParamNameTemplate: "{{ .PVCNamespace }-{{ .PVCName }}", // Missing closing brace
			},
			wantErr:     true,
			errContains: "invalid nameTemplate",
		},
		{
			name: "template with prefix/suffix (template takes precedence)",
			params: map[string]string{
				ParamNameTemplate: "{{ .PVCName }}",
				ParamNamePrefix:   "ignored-",
				ParamNameSuffix:   "-ignored",
			},
			wantNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := parseNameTemplateConfig(tt.params)

			if tt.wantErr {
				if err == nil {
					t.Errorf("parseNameTemplateConfig() expected error, got nil")
					return
				}
				if tt.errContains != "" && !stringContains(err.Error(), tt.errContains) {
					t.Errorf("parseNameTemplateConfig() error = %v, want error containing %q", err, tt.errContains)
				}
				return
			}

			if err != nil {
				t.Errorf("parseNameTemplateConfig() unexpected error: %v", err)
				return
			}

			if tt.wantNil && config != nil {
				t.Errorf("parseNameTemplateConfig() expected nil config, got %+v", config)
			}
			if !tt.wantNil && config == nil {
				t.Errorf("parseNameTemplateConfig() expected non-nil config, got nil")
			}
		})
	}
}

func TestExtractVolumeNameContext(t *testing.T) {
	params := map[string]string{
		CSIPVCName:      "my-pvc",
		CSIPVCNamespace: "my-namespace",
		CSIPVName:       "pvc-12345",
	}
	pvName := "pvc-abcdef-12345"

	ctx := extractVolumeNameContext(params, pvName)

	if ctx.PVName != pvName {
		t.Errorf("PVName = %q, want %q", ctx.PVName, pvName)
	}
	if ctx.PVCName != "my-pvc" {
		t.Errorf("PVCName = %q, want %q", ctx.PVCName, "my-pvc")
	}
	if ctx.PVCNamespace != "my-namespace" {
		t.Errorf("PVCNamespace = %q, want %q", ctx.PVCNamespace, "my-namespace")
	}
}

func TestExtractVolumeNameContextMissingValues(t *testing.T) {
	// Test with empty params - PVCName and PVCNamespace should be empty
	params := map[string]string{}
	pvName := "pvc-abcdef-12345"

	ctx := extractVolumeNameContext(params, pvName)

	if ctx.PVName != pvName {
		t.Errorf("PVName = %q, want %q", ctx.PVName, pvName)
	}
	if ctx.PVCName != "" {
		t.Errorf("PVCName = %q, want empty string", ctx.PVCName)
	}
	if ctx.PVCNamespace != "" {
		t.Errorf("PVCNamespace = %q, want empty string", ctx.PVCNamespace)
	}
}

func TestSanitizeVolumeName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "already valid",
			input: "my-volume-name",
			want:  "my-volume-name",
		},
		{
			name:  "with spaces",
			input: "my volume name",
			want:  "my-volume-name",
		},
		{
			name:  "with slashes",
			input: "namespace/pvc-name",
			want:  "namespace-pvc-name",
		},
		{
			name:  "with special characters",
			input: "vol@name#test$data",
			want:  "vol-name-test-data",
		},
		{
			name:  "leading hyphen",
			input: "-my-volume",
			want:  "my-volume",
		},
		{
			name:  "multiple leading hyphens",
			input: "---my-volume",
			want:  "my-volume",
		},
		{
			name:  "trailing hyphen",
			input: "my-volume-",
			want:  "my-volume",
		},
		{
			name:  "multiple consecutive hyphens",
			input: "my---volume---name",
			want:  "my-volume-name",
		},
		{
			name:  "very long name truncated",
			input: "this-is-a-very-long-volume-name-that-exceeds-the-sixty-three-character-limit-for-kubernetes-labels",
			want:  "this-is-a-very-long-volume-name-that-exceeds-the-sixty-three-ch",
		},
		{
			name:  "truncation removes trailing hyphen",
			input: "this-is-a-very-long-volume-name-that-exceeds-sixty-three-char--",
			want:  "this-is-a-very-long-volume-name-that-exceeds-sixty-three-char",
		},
		{
			name:  "underscore preserved",
			input: "my_volume_name",
			want:  "my_volume_name",
		},
		{
			name:  "colon preserved",
			input: "my:volume:name",
			want:  "my:volume:name",
		},
		{
			name:  "period preserved",
			input: "my.volume.name",
			want:  "my.volume.name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeVolumeName(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeVolumeName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestValidateVolumeName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:    "valid simple name",
			input:   "my-volume",
			wantErr: false,
		},
		{
			name:    "valid with underscore",
			input:   "my_volume",
			wantErr: false,
		},
		{
			name:    "valid with colon",
			input:   "my:volume",
			wantErr: false,
		},
		{
			name:    "valid with period",
			input:   "my.volume",
			wantErr: false,
		},
		{
			name:    "valid starting with number",
			input:   "123volume",
			wantErr: false,
		},
		{
			name:    "empty name",
			input:   "",
			wantErr: true,
		},
		{
			name:    "starting with hyphen",
			input:   "-volume",
			wantErr: true,
		},
		{
			name:    "contains space",
			input:   "my volume",
			wantErr: true,
		},
		{
			name:    "contains slash",
			input:   "my/volume",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateVolumeName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateVolumeName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestRenderVolumeName(t *testing.T) {
	tests := []struct {
		name    string
		config  *nameTemplateConfig
		ctx     VolumeNameContext
		want    string
		wantErr bool
	}{
		{
			name:   "nil config returns PVName",
			config: nil,
			ctx: VolumeNameContext{
				PVName:       "pvc-12345",
				PVCName:      "my-pvc",
				PVCNamespace: "default",
			},
			want: "pvc-12345",
		},
		{
			name: "prefix only",
			config: &nameTemplateConfig{
				prefix: "prod-",
			},
			ctx: VolumeNameContext{
				PVName: "pvc-12345",
			},
			want: "prod-pvc-12345",
		},
		{
			name: "suffix only",
			config: &nameTemplateConfig{
				suffix: "-data",
			},
			ctx: VolumeNameContext{
				PVName: "pvc-12345",
			},
			want: "pvc-12345-data",
		},
		{
			name: "prefix and suffix",
			config: &nameTemplateConfig{
				prefix: "prod-",
				suffix: "-data",
			},
			ctx: VolumeNameContext{
				PVName: "pvc-12345",
			},
			want: "prod-pvc-12345-data",
		},
		{
			name: "template with PVCName",
			config: func() *nameTemplateConfig {
				c, _ := parseNameTemplateConfig(map[string]string{
					ParamNameTemplate: "{{ .PVCName }}",
				})
				return c
			}(),
			ctx: VolumeNameContext{
				PVName:  "pvc-12345",
				PVCName: "my-app-data",
			},
			want: "my-app-data",
		},
		{
			name: "template with PVCNamespace and PVCName",
			config: func() *nameTemplateConfig {
				c, _ := parseNameTemplateConfig(map[string]string{
					ParamNameTemplate: "{{ .PVCNamespace }}-{{ .PVCName }}",
				})
				return c
			}(),
			ctx: VolumeNameContext{
				PVName:       "pvc-12345",
				PVCName:      "my-pvc",
				PVCNamespace: "production",
			},
			want: "production-my-pvc",
		},
		{
			name: "template sanitizes output",
			config: func() *nameTemplateConfig {
				c, _ := parseNameTemplateConfig(map[string]string{
					ParamNameTemplate: "{{ .PVCNamespace }}/{{ .PVCName }}",
				})
				return c
			}(),
			ctx: VolumeNameContext{
				PVName:       "pvc-12345",
				PVCName:      "my-pvc",
				PVCNamespace: "my-namespace",
			},
			want: "my-namespace-my-pvc", // Slash replaced with hyphen
		},
		{
			name: "template with missing field uses empty string",
			config: func() *nameTemplateConfig {
				c, _ := parseNameTemplateConfig(map[string]string{
					ParamNameTemplate: "{{ .PVCNamespace }}-{{ .PVCName }}",
				})
				return c
			}(),
			ctx: VolumeNameContext{
				PVName:       "pvc-12345",
				PVCName:      "my-pvc",
				PVCNamespace: "", // Empty namespace
			},
			want: "my-pvc", // Leading hyphen removed by sanitization
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := renderVolumeName(tt.config, tt.ctx)
			if (err != nil) != tt.wantErr {
				t.Errorf("renderVolumeName() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("renderVolumeName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveVolumeName(t *testing.T) {
	tests := []struct {
		name    string
		params  map[string]string
		pvName  string
		want    string
		wantErr bool
	}{
		{
			name:   "no templating returns pvName",
			params: map[string]string{},
			pvName: "pvc-abcdef-12345",
			want:   "pvc-abcdef-12345",
		},
		{
			name: "simple prefix",
			params: map[string]string{
				ParamNamePrefix: "k8s-",
			},
			pvName: "pvc-12345",
			want:   "k8s-pvc-12345",
		},
		{
			name: "simple suffix",
			params: map[string]string{
				ParamNameSuffix: "-vol",
			},
			pvName: "pvc-12345",
			want:   "pvc-12345-vol",
		},
		{
			name: "full template with PVC info",
			params: map[string]string{
				ParamNameTemplate: "{{ .PVCNamespace }}-{{ .PVCName }}",
				CSIPVCName:        "postgres-data",
				CSIPVCNamespace:   "database",
			},
			pvName: "pvc-abcdef-12345",
			want:   "database-postgres-data",
		},
		{
			name: "template using PVName fallback",
			params: map[string]string{
				ParamNameTemplate: "vol-{{ .PVName }}",
			},
			pvName: "pvc-12345",
			want:   "vol-pvc-12345",
		},
		{
			name: "invalid template returns error",
			params: map[string]string{
				ParamNameTemplate: "{{ .Invalid",
			},
			pvName:  "pvc-12345",
			wantErr: true,
		},
		{
			name: "real-world example: namespace-app-component",
			params: map[string]string{
				ParamNameTemplate: "{{ .PVCNamespace }}-{{ .PVCName }}",
				CSIPVCName:        "redis-master-0",
				CSIPVCNamespace:   "cache",
			},
			pvName: "pvc-abc123",
			want:   "cache-redis-master-0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveVolumeName(tt.params, tt.pvName)
			if (err != nil) != tt.wantErr {
				t.Errorf("ResolveVolumeName() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ResolveVolumeName() = %q, want %q", got, tt.want)
			}
		})
	}
}

// stringContains is a helper function for string contains check in tests.
func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
