package steps

import (
	"strings"
	"testing"

	"rondo/internal/kobj"
)

const hostilePassword = `p@ss:w/o;rd?#&=+ é`

func TestValuesCarryTheAuthenticatedArtifactoryURL(t *testing.T) {
	cfg := siteCfg()
	cfg.Artifactory.Username = "svc-acme"
	cfg.Secrets.ArtifactoryPassword = hostilePassword

	p, objs, _ := capture(t, cfg)
	if err := p.applyPlatformValues(t.Context()); err != nil {
		t.Fatal(err)
	}
	sd := nested(t, objs.find(t, "Secret", kobj.PlatformValuesSecret), "stringData")

	raw, ok := sd["artifactoryAuthURL"]
	if !ok {
		t.Fatalf("key artifactoryAuthURL missing from %s: `${secrets.artifactoryAuthURL}` "+
			"in platform/components/monitoring.yaml would not resolve and the helm Job "+
			"would fail closed on \"unresolved secret tokens\"", kobj.PlatformValuesSecret)
	}
	got, _ := raw.(string)

	if !strings.Contains(got, "@") {
		t.Errorf("artifactoryAuthURL = %q carries no userinfo: Grafana would pull its "+
			"plugin ANONYMOUSLY and Artifactory would answer \"401 Authentication is "+
			"required\"", got)
	}
	if strings.Contains(got, hostilePassword) {
		t.Errorf("artifactoryAuthURL = %q: the password is projected RAW, so its "+
			"`@` moves the userinfo/host boundary and the plugin URL points at a "+
			"host that does not exist", got)
	}
	want, err := cfg.ArtifactoryAuthURL()
	if err != nil {
		t.Fatalf("ArtifactoryAuthURL: %v", err)
	}
	if got != want {
		t.Errorf("artifactoryAuthURL = %q, want %q", got, want)
	}
}
