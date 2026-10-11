package main

import "testing"

func TestResolveAPIKey(t *testing.T) {
	env := func(v string) func(string) string {
		return func(name string) string {
			if name == truenasKeyEnvName {
				return v
			}
			return ""
		}
	}
	tests := []struct {
		name    string
		flagVal string
		envVal  string
		want    string
	}{
		{name: "env only (chart default: keeps the key off the command line)", envVal: "from-env", want: "from-env"},
		{name: "flag wins for backward compatibility", flagVal: "from-flag", envVal: "from-env", want: "from-flag"},
		{name: "flag only", flagVal: "from-flag", want: "from-flag"},
		{name: "neither", want: ""},
		{name: "whitespace from a secret is trimmed", envVal: "  key\n", want: "key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveAPIKey(tt.flagVal, env(tt.envVal)); got != tt.want {
				t.Errorf("resolveAPIKey(%q, env=%q) = %q, want %q", tt.flagVal, tt.envVal, got, tt.want)
			}
		})
	}
}
