package bundle

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"rondo/internal/config"
	"rondo/internal/httpx"
	"rondo/internal/log"
)

func componentBase(c *config.Config, name string) string {
	return c.Mirror().FilesRepoURL() + "/" + name + "/platform"
}

const ComponentChannel = "latest"

func ResolveComponent(ctx context.Context, client httpx.Doer, c *config.Config,
	cacheDir, name, channel, version string) (dir, resolved string, err error) {
	if err := checkSegment("component", name); err != nil {
		return "", "", err
	}
	base := componentBase(c, name)
	m := c.Mirror()
	user, pass := m.Username, m.Password

	if version == "" {
		if channel == "" {
			channel = ComponentChannel
		}
		if err := checkSegment("channel", channel); err != nil {
			return "", "", err
		}
		v, err := resolveChannel(ctx, client, user, pass, base, channel)
		if err != nil {
			return "", "", fmt.Errorf("resolve channel %s of %s: %w; does the component "+
				"publish a %s.json pointer?", channel, name, err, channel)
		}
		log.Info("component channel resolved", "component", name, "channel", channel,
			"version", v)
		version = v
	}
	if err := checkSegment("version", version); err != nil {
		return "", "", err
	}
	archive := "platform-" + version + ".tar.gz"

	dir = filepath.Join(cacheDir, "components", name, version)
	marker := filepath.Join(dir, ".rondo-component-ok")
	if _, err := os.Stat(marker); err == nil {
		log.Info("component tree already present, reused", "component", name,
			"version", version, "dir", dir)
		return dir, version, nil
	}

	log.Step("downloading component tree", "component", name, "version", version)
	wantSum, err := httpx.GetText(ctx, client, user, pass, base+"/"+archive+".sha256")
	if err != nil {
		return "", "", fmt.Errorf("checksum %s.sha256: %w; does the component publish "+
			"its checksum next to its archive?", archive, err)
	}
	body, err := httpx.Get(ctx, client, user, pass, base+"/"+archive)
	if err != nil {
		return "", "", err
	}
	defer body.Close()

	if err := os.RemoveAll(dir); err != nil {
		return "", "", err
	}
	if err := extractVerified(body, firstField(wantSum), dir); err != nil {
		os.RemoveAll(dir)
		return "", "", err
	}
	if err := os.WriteFile(marker, []byte(version+"\n"), 0o644); err != nil {
		return "", "", err
	}
	log.Info("component tree extracted", "component", name, "dir", dir)
	return dir, version, nil
}
