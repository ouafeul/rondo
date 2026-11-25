package steps

import (
	"strings"
	"testing"

	"rondo/internal/kobj"
)

const hostilePassword = `p@ss:w/o;rd?#&=+ é`

func TestValuesCarryTheAuthenticatedMirrorURL(t *testing.T) {
	cfg := siteCfg()
	cfg.Artifacts.Username = "svc-acme"
	cfg.Secrets.MirrorPassword = hostilePassword

	p, objs, _ := capture(t, cfg)
	if err := p.applyPlatformValues(t.Context()); err != nil {
		t.Fatal(err)
	}
	sd := nested(t, objs.find(t, "Secret", kobj.PlatformValuesSecret), "stringData")

	raw, ok := sd["mirrorAuthURL"]
	if !ok {
		t.Fatalf("key mirrorAuthURL missing from %s: `${secrets.mirrorAuthURL}` "+
			"in platform/components/monitoring.yaml would not resolve and the helm Job "+
			"would fail closed on \"unresolved secret tokens\"", kobj.PlatformValuesSecret)
	}
	got, _ := raw.(string)

	if !strings.Contains(got, "@") {
		t.Errorf("mirrorAuthURL = %q carries no userinfo: Grafana would pull its "+
			"plugin ANONYMOUSLY and the mirror would answer \"401 Authentication is "+
			"required\"", got)
	}
	if strings.Contains(got, hostilePassword) {
		t.Errorf("mirrorAuthURL = %q: the password is projected RAW, so its "+
			"`@` moves the userinfo/host boundary and the plugin URL points at a "+
			"host that does not exist", got)
	}
	want, err := cfg.Mirror().FilesAuthURL()
	if err != nil {
		t.Fatalf("FilesAuthURL: %v", err)
	}
	if got != want {
		t.Errorf("mirrorAuthURL = %q, want %q", got, want)
	}
}
