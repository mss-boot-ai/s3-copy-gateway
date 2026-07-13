package main

import "testing"

func TestIsVersionCommand(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "long flag", args: []string{"--version"}, want: true},
		{name: "short flag", args: []string{"-version"}, want: true},
		{name: "command", args: []string{"version"}, want: true},
		{name: "no args", args: nil, want: false},
		{name: "unknown", args: []string{"serve"}, want: false},
		{name: "extra args", args: []string{"--version", "extra"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isVersionCommand(tt.args); got != tt.want {
				t.Fatalf("isVersionCommand(%q) = %t, want %t", tt.args, got, tt.want)
			}
		})
	}
}

func TestVersionString(t *testing.T) {
	got := versionString("v1.2.3", "abc123", "2026-07-13T00:00:00Z")
	want := "s3-copy-gateway version=v1.2.3 commit=abc123 build_date=2026-07-13T00:00:00Z"
	if got != want {
		t.Fatalf("versionString() = %q, want %q", got, want)
	}
}
