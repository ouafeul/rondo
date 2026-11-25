package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"rondo/internal/config"
	"rondo/internal/steps"
	"rondo/internal/version"
)

func (d *deps) deployPlan() []steps.Step {
	return []steps.Step{
		{Name: "host prerequisites preflight", Run: steps.NewPreflightStage(false, flagDryRun).AssertHostTools},
		{Name: "tool download", Run: d.ensureTools},
		{Name: "OpenTofu registry setup", Run: d.tofu.ConfigureRegistry},
		{Name: "k3s VM provisioning (tofu)", Run: d.tofu.ApplyManagement},
		{Name: "cloud-init wait", Run: d.mgmt.WaitCloudInit},
		{Name: "k3s HA cluster formation", Run: d.mgmt.SetupK3s},
		{Name: "kubeconfig install", Run: d.mgmt.InstallKubeconfig},
		{Name: "bootstrap manifest rendering", Run: d.renderBootstrap},
		{Name: "Helm chart deployment", Run: d.helm.Deploy},
		{Name: "operator install", Run: d.platform.Install},
		{Name: "platform credentials", Run: d.platform.ApplyCredentials},
		{Name: "network_mirror token", Run: d.platform.ApplyMirrorToken},
		{Name: "internal DNS resolution", Run: d.platform.ApplyCoreDNSHosts},
		{Name: "Targets and site values", Run: d.platform.ApplySite},
		{Name: "operator startup wait", Run: d.platform.WaitReady},
		{Name: "RONDO components", Run: d.applyRondoComponents},
	}
}

func (d *deps) applyRondoComponents(ctx context.Context) error {
	return d.platform.ApplyTree(ctx, "rondo", filepath.Join(d.ws.ProjectDir, "platform", "components"),
		version.Version)
}

func (d *deps) downstreamsPlan(targets []config.DownstreamTarget) []steps.Step {
	plan := []steps.Step{
		{Name: "host prerequisites preflight", Run: steps.NewPreflightStage(true, flagDryRun).AssertHostTools},
		{Name: "tool download", Run: d.ensureTools},
		{Name: "OpenTofu registry setup", Run: d.tofu.ConfigureRegistry},
		{Name: "Rancher API token", Run: d.boot.BootstrapToken},
		{Name: "Rancher server-url setting", Run: d.boot.EnsureServerURL},
		{Name: "secret regcred (fleet-default)", Run: d.boot.EnsureRegcred},
	}
	for i := range targets {
		t := &targets[i]
		plan = append(plan, steps.Step{Name: "cluster " + t.Name, Run: func(ctx context.Context) error {
			access, secret := d.boot.APIToken()
			server, kubeconfig, err := d.tofu.ApplyRKE2(ctx, t, access, secret)
			if err != nil {
				return err
			}
			if !flagDryRun {
				if err := d.platform.ApplyRKE2Target(ctx, t, server, kubeconfig); err != nil {
					return err
				}
			}
			return d.platform.ApplyBaseEnrollments(ctx, t.Name,
				filepath.Join(d.ws.ProjectDir, "platform", "enrollments"), version.Version)
		}})
	}
	return plan
}

func (d *deps) destroyDownstreamsPlan(targets []config.DownstreamTarget) []steps.Step {
	plan := []steps.Step{
		{Name: "tool download", Run: d.ensureTools},
		{Name: "OpenTofu registry setup", Run: d.tofu.ConfigureRegistry},
		{Name: "Rancher API token", Run: d.boot.BootstrapToken},
	}
	for i := range targets {
		t := &targets[i]
		plan = append(plan, steps.Step{Name: "destroying " + t.Name, Run: func(ctx context.Context) error {
			if err := d.platform.UninstallTree(ctx, steps.BaseTreeName(t.Name)); err != nil {
				return err
			}
			access, secret := d.boot.APIToken()
			if err := d.tofu.DestroyRKE2(ctx, t, access, secret); err != nil {
				return err
			}
			return d.platform.DeleteTarget(ctx, t.Name)
		}})
	}
	return plan
}

func (d *deps) destroyPlan() []steps.Step {
	return []steps.Step{
		{Name: "tool download", Run: d.ensureTools},
		{Name: "destroying management (tofu destroy)", Run: d.tofu.DestroyManagement},
	}
}

// selectDownstreams returns the downstreams named, or all of them when none is.
func selectDownstreams(c *config.Config, names []string) ([]config.DownstreamTarget, error) {
	all := c.Targets.Downstreams
	if len(names) == 0 {
		if len(all) == 0 {
			return nil, fmt.Errorf("no rke2 target in the manifest")
		}
		return all, nil
	}
	byName := map[string]config.DownstreamTarget{}
	known := make([]string, 0, len(all))
	for _, t := range all {
		byName[t.Name] = t
		known = append(known, t.Name)
	}
	out := make([]config.DownstreamTarget, 0, len(names))
	for _, n := range names {
		t, ok := byName[n]
		if !ok {
			return nil, fmt.Errorf("unknown rke2 target %q (declared: %s)", n, strings.Join(known, ", "))
		}
		out = append(out, t)
	}
	return out, nil
}
