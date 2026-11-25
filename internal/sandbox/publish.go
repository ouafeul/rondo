package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"rondo/internal/log"
	"rondo/internal/platform"
	"rondo/internal/runner"
)

type depsFile struct {
	HelmCharts []struct {
		Name       string `json:"name"`
		Version    string `json:"version"`
		SourceRepo string `json:"source_repo"`
	} `json:"helm_charts"`
	GrafanaPlugins []genericDep `json:"grafana_plugins"`
	Binaries       []genericDep `json:"binaries"`
}

type genericDep struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	SourceURL  string `json:"source_url"`
	TargetRepo string `json:"target_repo"`
	TargetPath string `json:"target_path"`
}

func readDeps(p string) (*depsFile, error) {
	var d depsFile
	if p == "" {
		return &d, nil
	}
	raw, err := os.ReadFile(p) //nolint:gosec // repo manifest
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("%s unreadable: %w", p, err)
	}
	return &d, nil
}

type appChart struct {
	dir, version string
	deps         bool
}

// firstPartyCharts are the charts of an apps/ directory, by name.
func firstPartyCharts(dir string) (map[string]appChart, error) {
	out := map[string]appChart{}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		chartDir := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(filepath.Join(chartDir, "Chart.yaml")) //nolint:gosec // repo chart
		if err != nil {
			continue
		}
		var meta struct {
			Name         string `json:"name"`
			Version      string `json:"version"`
			AppVersion   string `json:"appVersion"`
			Dependencies []any  `json:"dependencies"`
		}
		if err := yaml.Unmarshal(raw, &meta); err != nil {
			return nil, fmt.Errorf("%s/Chart.yaml unreadable: %w", chartDir, err)
		}
		out[meta.Name] = appChart{dir: chartDir, version: meta.Version, deps: len(meta.Dependencies) > 0}
	}
	return out, nil
}

type publisher struct {
	registry string
	run      runner.CommandRunner
	http     *http.Client
	work     string
}

func newPublisher(registry string) (*publisher, func(), error) {
	work, err := os.MkdirTemp("", "rondo-sandbox-")
	if err != nil {
		return nil, nil, err
	}
	return &publisher{registry: registry, run: runner.NewExec(nil), work: work,
		http: &http.Client{Timeout: 10 * time.Minute}}, func() { os.RemoveAll(work) }, nil
}

// RunPublishRondo puts RONDO on the stand-in, as its CI publishes it: its charts, and what its
// machine Jobs download (the sandbox's machine module, its playbook).
func RunPublishRondo(ctx context.Context, s Settings, registry, rondoVersion string) error {
	pub, done, err := newPublisher(registry)
	if err != nil {
		return err
	}
	defer done()
	rondoApps, err := firstPartyCharts(filepath.Join(s.RondoDir, "apps"))
	if err != nil {
		return err
	}
	rondoDeps, err := readDeps(filepath.Join(s.RondoDir, "manifests", "dependencies.yaml"))
	if err != nil {
		return err
	}
	log.Step("RONDO charts")
	rondoTree, err := platform.Load(filepath.Join(s.RondoDir, "platform"))
	if err != nil {
		return err
	}
	for _, c := range chartsOf(rondoTree) {
		if err := pub.chart(ctx, c, rondoApps, rondoDeps, nil); err != nil {
			return err
		}
	}
	return rondoMachineFiles(s, rondoVersion)
}

// RunPublish puts a planned component on the stand-in, as its CI and the site's mirror would:
// its charts, generic files, images and machine files, then writes the tree the sandbox applies.
func RunPublish(ctx context.Context, s Settings, component, registry string, noBuild bool) error {
	p, err := loadPlan(s, component)
	if err != nil {
		return err
	}
	pub, done, err := newPublisher(registry)
	if err != nil {
		return err
	}
	defer done()
	compApps, err := firstPartyCharts(p.Apps)
	if err != nil {
		return err
	}
	compDeps, err := readDeps(p.Dependencies)
	if err != nil {
		return err
	}

	log.Step("component charts", "component", p.Component)
	tree, err := platform.Load(p.Platform)
	if err != nil {
		return err
	}
	rw := treeRewrites{charts: map[string]string{}, digests: map[string]string{}, ansible: map[string]string{}}
	for _, c := range chartsOf(tree) {
		if err := pub.chart(ctx, c, compApps, compDeps, rw.charts); err != nil {
			return err
		}
	}

	log.Step("component generic files")
	for _, a := range append(compDeps.GrafanaPlugins, compDeps.Binaries...) {
		if err := pub.generic(ctx, filepath.Join(s.StateDir, "generic"), a); err != nil {
			return err
		}
	}

	log.Step("component images")
	if noBuild && len(p.Images) > 0 {
		log.Warn("--no-build: component images not built, their pods will stay in ImagePullBackOff "+
			"unless the images are already on the sandbox", "images", len(p.Images))
	}
	for _, img := range p.Images {
		if noBuild {
			continue
		}
		if err := pub.image(ctx, p.Dir, img); err != nil {
			return err
		}
	}

	if len(p.Machines) > 0 {
		log.Step("component machines")
		if err := pub.machineFiles(ctx, s, p, tree, compDeps, &rw); err != nil {
			return err
		}
	}

	log.Step("sandbox tree")
	p.Registry, p.NoBuild = registry, noBuild
	if p.Tree, err = prepareTree(s, p, tree, rw); err != nil {
		return err
	}
	return savePlan(s, p)
}

type chartRef struct {
	component, name, version string
}

func chartsOf(tree *platform.Tree) []chartRef {
	var out []chartRef
	for _, o := range tree.Objects {
		if o.Kind != "Component" {
			continue
		}
		c := componentOf(o)
		if c.vm || c.oci == "" {
			continue
		}
		out = append(out, chartRef{component: o.Name, name: path.Base(c.oci), version: c.version})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].component < out[j].component })
	return out
}

// chart puts one chart on the registry. A first-party chart is packaged from its sources;
// with rewrites set (the component's own charts), under a version derived from its content,
// so that a template changed without a version bump is redeployed all the same.
func (pub *publisher) chart(ctx context.Context, c chartRef, apps map[string]appChart, deps *depsFile,
	rewrites map[string]string) error {
	src, err := sourceOf(c, apps, deps)
	if err != nil {
		return err
	}
	if src.app != nil {
		app := *src.app
		version := c.version
		if rewrites != nil {
			h, err := hashDir(app.dir)
			if err != nil {
				return err
			}
			version = sandboxVersion(c.version, h)
			rewrites[c.name+"@"+c.version] = version
		}
		return pub.pushApp(ctx, c.name, app, version)
	}
	return pub.mirrorChart(ctx, c.name, src.version, src.repo)
}

// chartSource is where a chart comes from: a first-party chart of apps/, or an upstream one
// of helm_charts.
type chartSource struct {
	app           *appChart
	version, repo string
}

// sourceOf finds a Component's chart as the site would get it: published by the component's
// CI from apps/, under the pinned version, or mirrored from dependencies.yaml.
func sourceOf(c chartRef, apps map[string]appChart, deps *depsFile) (chartSource, error) {
	if app, ok := apps[c.name]; ok {
		if app.version != c.version {
			return chartSource{}, fmt.Errorf("Component %s pins %s %s, but %s is at %s: on a site, "+
				"that version exists only if an earlier CI run published it; align the two",
				c.component, c.name, c.version, app.dir, app.version)
		}
		return chartSource{app: &app}, nil
	}
	for _, d := range deps.HelmCharts {
		if d.Name == c.name && strings.TrimPrefix(d.Version, "v") == strings.TrimPrefix(c.version, "v") {
			return chartSource{version: d.Version, repo: d.SourceRepo}, nil
		}
	}
	return chartSource{}, fmt.Errorf("Component %s: chart %s %s is neither in apps/ nor in the helm_charts of "+
		"dependencies.yaml, the site would never have it", c.component, c.name, c.version)
}

func sandboxVersion(version, hash string) string {
	if strings.Contains(version, "-") {
		return version + ".sbx" + hash
	}
	return version + "-sbx" + hash
}

func (pub *publisher) pushApp(ctx context.Context, name string, app appChart, version string) error {
	args := []string{"package", app.dir, "--version", version, "--destination", pub.work}
	if app.deps {
		args = append(args, "--dependency-update")
	}
	if err := pub.run.Run(ctx, runner.Cmd{Name: "helm", Args: args}); err != nil {
		return err
	}
	pkg := filepath.Join(pub.work, name+"-"+version+".tgz")
	if err := pub.push(ctx, pkg); err != nil {
		return err
	}
	log.Info("first-party chart published", "chart", name, "version", version)
	return nil
}

func (pub *publisher) mirrorChart(ctx context.Context, name, version, repo string) error {
	present, err := pub.hasTag(ctx, helmRepo+"/"+name, strings.ReplaceAll(version, "+", "_"))
	if err != nil {
		return err
	}
	if present {
		log.Info("upstream chart already on the sandbox", "chart", name, "version", version)
		return nil
	}
	args := []string{"pull", "--version", version, "--destination", pub.work}
	if strings.HasPrefix(repo, "oci://") {
		args = append(args, strings.TrimSuffix(repo, "/")+"/"+name)
	} else {
		args = append(args, "--repo", repo, name)
	}
	if err := pub.run.Run(ctx, runner.Cmd{Name: "helm", Args: args}); err != nil {
		return fmt.Errorf("upstream chart %s %s from %s: %w", name, version, repo, err)
	}
	matches, _ := filepath.Glob(filepath.Join(pub.work, name+"-*.tgz"))
	if len(matches) != 1 {
		return fmt.Errorf("upstream chart %s: archive not found after helm pull", name)
	}
	if err := pub.push(ctx, matches[0]); err != nil {
		return err
	}
	log.Info("upstream chart mirrored", "chart", name, "version", version, "source", repo)
	return os.Remove(matches[0])
}

func (pub *publisher) push(ctx context.Context, pkg string) error {
	return pub.run.Run(ctx, runner.Cmd{Name: "helm",
		Args: []string{"push", pkg, "oci://" + pub.registry + "/" + helmRepo, "--plain-http"}})
}

func (pub *publisher) hasTag(ctx context.Context, repo, tag string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://"+pub.registry+"/v2/"+repo+"/tags/list", nil)
	if err != nil {
		return false, err
	}
	resp, err := pub.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("zot unreachable (%s): %w", pub.registry, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	var list struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return false, fmt.Errorf("tags of %s: %w", repo, err)
	}
	for _, t := range list.Tags {
		if t == tag {
			return true, nil
		}
	}
	return false, nil
}

// generic lays a file where the site's generic repository has it, under the stand-in's root.
func (pub *publisher) generic(ctx context.Context, root string, a genericDep) error {
	if a.SourceURL == "" || a.TargetRepo == "" || a.TargetPath == "" {
		return nil
	}
	dest := filepath.Join(root, a.TargetRepo, strings.ReplaceAll(a.TargetPath, "{version}", a.Version))
	if fileExists(dest) {
		log.Info("generic file already on the sandbox", "name", a.Name, "version", a.Version)
		return nil
	}
	src := strings.ReplaceAll(a.SourceURL, "{version}", a.Version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return err
	}
	resp, err := pub.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", src, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", src, resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".part"
	f, err := os.Create(tmp) //nolint:gosec // under the sandbox state
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	log.Info("generic file published", "name", a.Name, "version", a.Version, "path", a.TargetRepo+"/"+
		strings.ReplaceAll(a.TargetPath, "{version}", a.Version))
	return os.Rename(tmp, dest)
}

// imageTag is the tag an image of rondo-sandbox.yaml gets: its own, or its chart's appVersion,
// as the component's CI does.
func imageTag(dir string, img imageBuild) (string, error) {
	tag := img.Tag
	if img.TagFromChart != "" {
		raw, err := os.ReadFile(filepath.Join(dir, img.TagFromChart, "Chart.yaml")) //nolint:gosec // repo chart
		if err != nil {
			return "", fmt.Errorf("image %s, tagFromChart: %w", img.Name, err)
		}
		var meta struct {
			AppVersion string `json:"appVersion"`
		}
		if err := yaml.Unmarshal(raw, &meta); err != nil {
			return "", err
		}
		tag = meta.AppVersion
	}
	if tag == "" {
		return "", fmt.Errorf("image %s: neither tag nor tagFromChart in rondo-sandbox.yaml", img.Name)
	}
	return tag, nil
}

func dockerfileOf(dir string, img imageBuild) string {
	if img.Dockerfile != "" {
		return filepath.Join(dir, img.Dockerfile)
	}
	return filepath.Join(dir, img.Context, "Dockerfile")
}

func (pub *publisher) image(ctx context.Context, dir string, img imageBuild) error {
	tag, err := imageTag(dir, img)
	if err != nil {
		return err
	}
	ref := pub.registry + "/" + img.Name + ":" + tag
	context := filepath.Join(dir, img.Context)
	dockerfile := dockerfileOf(dir, img)
	log.Info("building the image", "image", img.Name+":"+tag)
	if err := pub.run.Run(ctx, runner.Cmd{Name: "docker",
		Args: []string{"build", "--progress=plain", "-t", ref, "-f", dockerfile, context}}); err != nil {
		return fmt.Errorf("image %s: %w", img.Name, err)
	}
	return pub.run.Run(ctx, runner.Cmd{Name: "docker", Args: []string{"push", "-q", ref}})
}

// treeRewrites are what the sandbox changes in the component's tree for what it made itself:
// a first-party chart's version (by name@version), the digest of a machine file (by
// component/path), a playbook's version (by component). A change of sources then replays the
// Job, as a new version published by the CI would.
type treeRewrites struct {
	charts, digests, ansible map[string]string
}

// prepareTree writes the tree the sandbox applies: the component's, with rewrites.
func prepareTree(s Settings, p *plan, tree *platform.Tree, rw treeRewrites) (string, error) {
	dst := filepath.Join(s.StateDir, "trees", p.Component)
	if err := os.RemoveAll(dst); err != nil {
		return "", err
	}
	for _, o := range tree.Objects {
		if o.Kind == "Component" {
			c := componentOf(o)
			spec, _ := o.Raw["spec"].(map[string]any)
			if v, ok := rw.charts[path.Base(c.oci)+"@"+c.version]; ok {
				chart, _ := spec["chart"].(map[string]any)
				chart["version"] = v
			}
			if vm, _ := spec["vm"].(map[string]any); vm != nil {
				rewriteVM(o.Name, vm, rw)
			}
		}
		raw, err := yaml.Marshal(o.Raw)
		if err != nil {
			return "", err
		}
		file := filepath.Join(dst, strings.ToLower(o.Kind)+"s", o.Name+".yaml")
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(file, raw, 0o600); err != nil {
			return "", err
		}
	}
	return dst, nil
}

// hashDir is a short digest of a directory's files, names and contents.
func hashDir(dir string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(p) //nolint:gosec // repo chart
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", rel, len(raw))
		h.Write(raw)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:8], nil
}
