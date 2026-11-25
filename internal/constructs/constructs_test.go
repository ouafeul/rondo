package constructs

import (
	"strings"
	"testing"

	"rondo/internal/synth"
)

func TestPKIStack_Manifests(t *testing.T) {
	pki := &PKIStack{
		CommonName:      "RONDO Cluster Internal CA",
		RancherHostname: "rancher.acme.example.com",
	}
	renderSnapshot(t, "pki.yaml", pki)
}

func TestPKIStack_Validate_BadHostname(t *testing.T) {
	pki := &PKIStack{CommonName: "CA", RancherHostname: "not a host"}
	if err := pki.Validate(); err == nil {
		t.Fatal("want fqdn error, got nil")
	}
}

func TestMetalLBConfig_Manifests(t *testing.T) {
	mlb := &MetalLBConfig{IngressIP: "10.0.0.50"}
	renderSnapshot(t, "metallb_config.yaml", mlb)
}

func TestMetalLBConfig_Validate_BadIP(t *testing.T) {
	mlb := &MetalLBConfig{IngressIP: "not-an-ip"}
	if err := mlb.Validate(); err == nil {
		t.Fatal("want ip error, got nil")
	}
}

func TestRender_PropagatesValidationError(t *testing.T) {
	bad := &MetalLBConfig{IngressIP: "not-an-ip"}
	_, err := synth.Render(bad)
	if err == nil || !strings.Contains(err.Error(), "validation") {
		t.Fatalf("want propagated validation error, got: %v", err)
	}
}
