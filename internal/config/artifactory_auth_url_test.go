package config

import (
	"net/url"
	"strings"
	"testing"
)

const hostilePassword = `p@ss:w/o;rd?#&=+ é`

func TestAuthenticatedArtifactoryURLSurvivesParsing(t *testing.T) {
	const user = `svc acme@example`
	c := &Config{
		Artifactory: Artifactory{URL: "https://arti.example.com/artifactory", Username: user},
		Secrets:     Secrets{ArtifactoryPassword: hostilePassword},
	}

	got, err := c.ArtifactoryAuthURL()
	if err != nil {
		t.Fatalf("ArtifactoryAuthURL: %v", err)
	}

	t.Run("round trip through url.Parse", func(t *testing.T) {
		u, err := url.Parse(got + "/rondo-generic-local/plugin.zip")
		if err != nil {
			t.Fatalf("unparsable URL (%v): Grafana would fail to parse it before even "+
				"reaching Artifactory", err)
		}
		if u.Host != "arti.example.com" {
			t.Errorf("host = %q, want arti.example.com: the credentials spilled "+
				"into the authority, so the request goes to a host that does not exist", u.Host)
		}
		if u.Path != "/artifactory/rondo-generic-local/plugin.zip" {
			t.Errorf("path = %q: the Artifactory context path is lost", u.Path)
		}
		if got := u.User.Username(); got != user {
			t.Errorf("decoded user = %q, want %q: Artifactory would receive another account "+
				"and answer 401", got, user)
		}
		pass, ok := u.User.Password()
		if !ok {
			t.Fatal("no password in the userinfo: the request would go out " +
				"unauthenticated, the very failure this guards against")
		}
		if pass != hostilePassword {
			t.Errorf("decoded password = %q, want %q", pass, hostilePassword)
		}
	})

	t.Run("<url>;<pluginID> split preserved", func(t *testing.T) {
		const pluginID = "victoriametrics-logs-datasource"
		rawURL, id, found := strings.Cut(got+"/plugin.zip;"+pluginID, ";")
		if !found {
			t.Fatal("no `;` at all: the rest of the test is meaningless")
		}
		if id != pluginID {
			t.Errorf("plugin ID = %q, want %q: a `;` in the credentials "+
				"cut the URL before its own, and Grafana would install a plugin whose name "+
				"is a password fragment", id, pluginID)
		}
		if !strings.HasSuffix(rawURL, "/plugin.zip") {
			t.Errorf("split URL = %q: it does not reach the archive", rawURL)
		}
	})

	t.Run("no reserved character left", func(t *testing.T) {
		userinfo, _, found := strings.Cut(strings.TrimPrefix(got, "https://"), "@")
		if !found {
			t.Fatal("no `@`: the URL carries no userinfo")
		}
		u, p, found := strings.Cut(userinfo, ":")
		if !found {
			t.Fatal("no `:` separator: the userinfo does not carry two fields")
		}
		for _, part := range []string{u, p} {
			if i := strings.IndexAny(part, "@/?#:;&=+ "); i >= 0 {
				t.Errorf("%q carries the reserved character %q unescaped", part, part[i])
			}
		}
	})
}

func TestArtifactoryURLWithoutSchemeFailsClosed(t *testing.T) {
	c := &Config{
		Artifactory: Artifactory{URL: "arti.example.com/artifactory", Username: "u"},
		Secrets:     Secrets{ArtifactoryPassword: hostilePassword},
	}
	got, err := c.ArtifactoryAuthURL()
	if err == nil {
		t.Fatalf("URL without a scheme accepted, rendered %q: pods would go out anonymously "+
			"and Artifactory would answer \"401 Authentication is required\"", got)
	}
	if !strings.Contains(err.Error(), "arti.example.com") {
		t.Errorf("the error does not name the faulty value, so it does not help "+
			"fix it: %v", err)
	}
	if strings.Contains(err.Error(), hostilePassword) {
		t.Error("the error copies the Artifactory password, it would end up in " +
			"the `rondo deploy` logs")
	}
}
