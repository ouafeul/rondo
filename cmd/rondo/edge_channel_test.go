package main

import (
	"testing"

	"rondo/internal/config"
	"rondo/internal/version"
)

func TestEdgeChannelOfBinary(t *testing.T) {
	for _, tc := range []struct {
		name, version, want string
	}{
		{"edge binary", "edge-feat-example-bbee98b9", "edge-feat-example"},
		{"slug without a dash", "edge-main-0123abcd", "edge-main"},
		{"tag stable", "v1.0.0", ""},
		{"pre-release", "v1.0.0-rc.1", ""},
		{"local build", "dev", ""},
		{"sha too short", "edge-main-abc", ""},
		{"last segment not hexadecimal", "edge-main-zzzzzzzz", ""},
		{"prefix alone", "edge-", ""},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := edgeChannelOfBinary(tc.version); got != tc.want {
				t.Errorf("edgeChannelOfBinary(%q) = %q, want %q", tc.version, got, tc.want)
			}
		})
	}
}

func TestIacChannelPrecedence(t *testing.T) {
	t.Cleanup(func() { flagIacChannel = "" })
	cfgEmpty := &config.Config{}

	flagIacChannel = "stable"
	if got := iacChannel(cfgEmpty); got != "stable" {
		t.Errorf("the flag does not take precedence: %q", got)
	}

	flagIacChannel = ""
	cfg := &config.Config{}
	cfg.Artifacts.ReleaseChannel = "edge-other"
	if got := iacChannel(cfg); got != "edge-other" {
		t.Errorf("artifacts.releaseChannel does not take precedence over the derivation: %q", got)
	}
}

func TestIacChannelFollowsTheEdgeBinaryByDefault(t *testing.T) {
	orig := version.Version
	t.Cleanup(func() { version.Version = orig; flagIacChannel = "" })
	flagIacChannel = ""

	version.Version = "edge-feat-example-bbee98b9"
	if got := iacChannel(&config.Config{}); got != "edge-feat-example" {
		t.Errorf("channel = %q, want edge-feat-example: an edge binary would "+
			"fetch a `stable` IaC bundle, hence the IaC of the latest tag", got)
	}

	version.Version = "v1.0.0"
	if got := iacChannel(&config.Config{}); got != "" {
		t.Errorf("channel = %q for a stable binary, want empty", got)
	}
}
