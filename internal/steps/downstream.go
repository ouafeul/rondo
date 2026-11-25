package steps

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"

	"rondo/internal/config"
	"rondo/internal/kobj"
	"rondo/internal/log"
	"rondo/internal/state"
)

type RancherBootstrap struct {
	cfg     *config.Config
	ws      Workspace
	kube    *KubeClient
	rancher *RancherClient
	dryRun  bool

	rancherEnv map[string]string
}

func NewRancherBootstrap(cfg *config.Config, ws Workspace, kube *KubeClient, rancher *RancherClient, dryRun bool) *RancherBootstrap {
	return &RancherBootstrap{cfg: cfg, ws: ws, kube: kube, rancher: rancher, dryRun: dryRun,
		rancherEnv: map[string]string{}}
}

func (b *RancherBootstrap) APIToken() (string, string) {
	return b.rancherEnv["TF_VAR_rancher_access_key"], b.rancherEnv["TF_VAR_rancher_secret_key"]
}

func (b *RancherBootstrap) BootstrapToken(ctx context.Context) error {
	log.Step("Rancher API token")
	if err := b.exportClusterCA(ctx); err != nil {
		return err
	}
	if b.dryRun {
		log.Info("DRY-RUN Rancher login + token generation")
		return nil
	}
	st, err := state.Load(b.ws.StatePath())
	if err != nil {
		return err
	}
	if st.RancherAccessKey != "" {
		if b.rancher.ValidToken(ctx, st.RancherAccessKey, st.RancherSecretKey) {
			log.Info("Rancher token already present and valid (state)")
			b.rancherEnv["TF_VAR_rancher_access_key"] = st.RancherAccessKey
			b.rancherEnv["TF_VAR_rancher_secret_key"] = st.RancherSecretKey
			return nil
		}
		log.Warn("Rancher token in state is invalid (Rancher reprovisioned?), regenerating")
	}
	session, err := b.rancher.Login(ctx, "admin", b.cfg.Secrets.RancherBootstrapPassword)
	if err != nil {
		return err
	}
	access, secret, err := b.rancher.CreateToken(ctx, session, "rondo-downstream")
	if err != nil {
		return err
	}
	b.rancherEnv["TF_VAR_rancher_access_key"] = access
	b.rancherEnv["TF_VAR_rancher_secret_key"] = secret
	st.RancherAccessKey, st.RancherSecretKey = access, secret
	if err := st.Save(b.ws.StatePath()); err != nil {
		return err
	}
	log.Info("Rancher API token created")
	return nil
}

const RancherSettingsResource = "settings.management.cattle.io"

const RancherServerURLSetting = "server-url"

func (b *RancherBootstrap) EnsureServerURL(ctx context.Context) error {
	log.Step("Rancher server-url setting")
	want := "https://" + b.cfg.RancherHostname()

	current, err := b.kube.Output(ctx, b.ws.Kubeconfig(), "get", RancherSettingsResource,
		RancherServerURLSetting, "-o", "jsonpath={.value}")
	if err != nil {
		log.Warn("setting "+RancherServerURLSetting+" UNREADABLE, check POSTPONED, and this "+
			"preview says nothing about it (Rancher not started yet? cluster unreachable?). "+
			"If it stays empty, downstream nodes will start without CATTLE_SERVER and stay "+
			"silent", "err", err)
		return nil
	}
	switch cur := strings.TrimSpace(current); {
	case cur == want:
		log.Info("setting "+RancherServerURLSetting+" already correct", "url", want)
		return nil
	case cur != "":
		log.Warn("setting "+RancherServerURLSetting+" DIFFERS from the site hostname, left "+
			"as is: a value set by hand may be deliberate (DNS alias, external load "+
			"balancer). If it is wrong, downstream agents will reach the wrong address; "+
			"fix it with `kubectl patch "+RancherSettingsResource+" "+RancherServerURLSetting+
			" --type=merge -p '{\"value\":\""+want+"\"}'`",
			"current", cur, "site hostname", want)
		return nil
	}

	if b.dryRun {
		log.Info("DRY-RUN would set "+RancherServerURLSetting+" (currently EMPTY)", "url", want)
		return nil
	}
	if err := b.kube.Run(ctx, b.ws.Kubeconfig(), "patch", RancherSettingsResource,
		RancherServerURLSetting, "--type=merge", "-p", fmt.Sprintf(`{"value":%q}`, want)); err != nil {
		log.Warn("setting "+RancherServerURLSetting+" FAILED (non-blocking), set it by "+
			"hand with `kubectl patch "+RancherSettingsResource+" "+RancherServerURLSetting+
			" --type=merge -p '{\"value\":\""+want+"\"}'`, otherwise no downstream node "+
			"will register", "err", err)
		return nil
	}
	log.Info("setting "+RancherServerURLSetting+" set (it was EMPTY)", "url", want)
	return nil
}

func (b *RancherBootstrap) exportClusterCA(ctx context.Context) error {
	if b.dryRun {
		log.Info("DRY-RUN export cluster CA -> certs/rancher-ca.pem")
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(b.ws.RancherCA()), 0o755); err != nil {
		return err
	}
	out, err := b.kube.Output(ctx, b.ws.Kubeconfig(),
		"-n", "cert-manager", "get", "secret", "cluster-ca-keypair", "-o", dataPath("ca.crt"))
	if err != nil {
		return fmt.Errorf("cert-manager CA extraction (cluster-ca-keypair missing?): %w", err)
	}
	dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out))
	if err != nil {
		return err
	}
	return os.WriteFile(b.ws.RancherCA(), dec, 0o644)
}

func (b *RancherBootstrap) EnsureRegcred(ctx context.Context) error {
	log.Step("secret regcred (fleet-default)")
	m := b.cfg.Mirror()
	if m.Anonymous() {
		log.Info("anonymous mirror: the rke2 nodes pull without credentials, no regcred")
		return nil
	}
	if b.dryRun {
		log.Info("DRY-RUN create secret regcred (fleet-default)")
		return nil
	}
	secret := kobj.Regcred(m.Username, m.Password)
	raw, err := yaml.Marshal(secret)
	if err != nil {
		return err
	}
	return b.kube.ApplyStdin(ctx, b.ws.Kubeconfig(), string(raw))
}
