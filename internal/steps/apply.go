package steps

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"rondo/internal/kobj"
	"rondo/internal/log"
	"rondo/internal/subst"
)

const CLIFieldManager = "rondo-cli"

// A tree only declares what to install and where: Targets come from rondo.yaml.
var treeKinds = map[string]bool{"Component": true, "Enrollment": true}

type treeObject struct{ Kind, Name string }

func (o treeObject) String() string { return o.Kind + "/" + o.Name }

func (o treeObject) resource() string { return strings.ToLower(o.Kind) + "s." + kobj.APIGroup }

func (p *PlatformStage) platformValues() map[string]string {
	env := p.cfg.Env()
	return subst.Values(func(name string) string { return env[name] })
}

// ApplyTree applies a tree of Components and Enrollments, deletes what its previous
// apply delivered and this one does not, then records the result.
func (p *PlatformStage) ApplyTree(ctx context.Context, name, src, version string) error {
	log.Step("applying tree", "tree", name, "version", version, "source", src)
	objs, err := treeObjects(src)
	if p.dryRun {
		if err != nil {
			log.Warn("DRY-RUN unreadable tree, nothing to preview", "err", err)
		}
		for _, o := range objs {
			log.Info("DRY-RUN would apply", "object", o)
		}
		return nil
	}
	if err != nil {
		return err
	}
	values := p.platformValues()
	if err := subst.Check(src, values); err != nil {
		return fmt.Errorf("unresolvable platform token in %s, the site does not "+
			"provide its value (rondo.yaml): %w", src, err)
	}

	work, err := os.MkdirTemp("", "rondo-apply-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	dst := filepath.Join(work, name)
	if err := copyTree(src, dst); err != nil {
		return fmt.Errorf("copying tree %s: %w", name, err)
	}
	if _, err := subst.Apply(dst, values); err != nil {
		return fmt.Errorf("resolving platform tokens: %w", err)
	}
	if err := p.kube.ApplyDirServerSide(ctx, ServerSideApply{
		Kubeconfig:   p.ws.Kubeconfig(),
		Dir:          dst,
		Namespace:    kobj.PlatformNamespace,
		FieldManager: CLIFieldManager,
	}); err != nil {
		return fmt.Errorf("applying tree %s: %w", name, err)
	}

	previous, err := p.readRecord(ctx, name)
	if err != nil {
		return err
	}
	if err := p.deleteObjects(ctx, missingFrom(previous, objs)); err != nil {
		return err
	}
	return p.writeRecord(ctx, name, version, objs)
}

// UninstallTree deletes everything the last apply of a tree delivered; each
// Enrollment's finalizer tears its workload down on its Target before the Components go.
func (p *PlatformStage) UninstallTree(ctx context.Context, name string) error {
	log.Step("uninstalling tree", "tree", name)
	if p.dryRun {
		log.Info("DRY-RUN would delete what the tree applied", "tree", name)
		return nil
	}
	objs, err := p.readRecord(ctx, name)
	if err != nil {
		return err
	}
	if len(objs) == 0 {
		log.Info("nothing installed under this name", "tree", name)
		return nil
	}
	if err := p.deleteObjects(ctx, objs); err != nil {
		return err
	}
	if err := p.kube.Run(ctx, p.ws.Kubeconfig(), "-n", kobj.PlatformNamespace, "delete",
		"configmap", kobj.RecordName(name), "--ignore-not-found"); err != nil {
		return err
	}
	log.Info("tree uninstalled; an Enrollment still present finishes removing its workload "+
		"(`kubectl -n "+kobj.PlatformNamespace+" get enrollments`)", "tree", name)
	return nil
}

// EnrollmentRemovalTimeout bounds the wait for the Enrollments' finalizers: their uninstall
// Job runs within its policy.timeout, 10 min by default.
const EnrollmentRemovalTimeout = 30 * time.Minute

// deleteObjects deletes the Enrollments first and, when Components go too, waits for the
// Enrollments to be gone before deleting them: an Enrollment's finalizer builds its
// uninstall Job (helm uninstall, vm-destroy) from its Component, and without it keeps the
// Enrollment and its workload. Enrollments alone (a base tree) are not waited for.
func (p *PlatformStage) deleteObjects(ctx context.Context, objs []treeObject) error {
	var enrollments, rest []treeObject
	for _, o := range objs {
		if o.Kind == "Enrollment" {
			enrollments = append(enrollments, o)
		} else {
			rest = append(rest, o)
		}
	}
	for _, o := range enrollments {
		if err := p.deleteObject(ctx, o); err != nil {
			return err
		}
	}
	if len(enrollments) > 0 && len(rest) > 0 {
		if err := p.waitEnrollmentsGone(ctx, enrollments); err != nil {
			return err
		}
	}
	for _, o := range rest {
		if err := p.deleteObject(ctx, o); err != nil {
			return err
		}
	}
	return nil
}

func (p *PlatformStage) deleteObject(ctx context.Context, o treeObject) error {
	if err := p.kube.Run(ctx, p.ws.Kubeconfig(), "-n", kobj.PlatformNamespace,
		"delete", o.resource(), o.Name, "--ignore-not-found", "--wait=false"); err != nil {
		return fmt.Errorf("deleting %s: %w", o, err)
	}
	log.Info("object removed", "object", o)
	return nil
}

func (p *PlatformStage) waitEnrollmentsGone(ctx context.Context, enrollments []treeObject) error {
	log.Info("waiting for the Enrollments' finalizers to remove their workloads",
		"enrollments", len(enrollments), "timeout", EnrollmentRemovalTimeout)
	args := []string{"-n", kobj.PlatformNamespace, "wait", "--for=delete",
		"--timeout=" + EnrollmentRemovalTimeout.String()}
	for _, o := range enrollments {
		args = append(args, o.resource()+"/"+o.Name)
	}
	if err := p.kube.Run(ctx, p.ws.Kubeconfig(), args...); err != nil {
		return fmt.Errorf("the Enrollments were not gone within %s, their Components are "+
			"kept so their finalizers can finish (`kubectl -n %s get enrollments`), "+
			"run the command again afterwards: %w", EnrollmentRemovalTimeout, kobj.PlatformNamespace, err)
	}
	return nil
}

func (p *PlatformStage) readRecord(ctx context.Context, name string) ([]treeObject, error) {
	out, err := p.kube.Output(ctx, p.ws.Kubeconfig(), "-n", kobj.PlatformNamespace, "get",
		"configmap", kobj.RecordName(name), "--ignore-not-found", "-o", dataPath("objects"))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", kobj.RecordName(name), err)
	}
	var objs []treeObject
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		kind, objName, ok := strings.Cut(strings.TrimSpace(line), "/")
		if ok {
			objs = append(objs, treeObject{Kind: kind, Name: objName})
		}
	}
	return objs, nil
}

func (p *PlatformStage) writeRecord(ctx context.Context, name, version string, objs []treeObject) error {
	lines := make([]string, len(objs))
	for i, o := range objs {
		lines[i] = o.String()
	}
	return p.applyObject(ctx, kobj.ComponentRecord(name, version, lines))
}

func missingFrom(previous, current []treeObject) []treeObject {
	keep := map[treeObject]bool{}
	for _, o := range current {
		keep[o] = true
	}
	var out []treeObject
	for _, o := range previous {
		if !keep[o] {
			out = append(out, o)
		}
	}
	return out
}

// treeObjects lists the objects of a tree, refusing any kind a tree may not carry.
func treeObjects(root string) ([]treeObject, error) {
	files, err := manifestFiles(root)
	if err != nil {
		return nil, fmt.Errorf("tree %s: %w", root, err)
	}
	var out []treeObject
	for _, f := range files {
		raw, err := os.ReadFile(f) //nolint:gosec // component tree
		if err != nil {
			return nil, err
		}
		rel, _ := filepath.Rel(root, f)
		for _, doc := range splitDocs(string(raw)) {
			obj, ok, err := decodeDoc(doc)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", rel, err)
			}
			if !ok {
				continue
			}
			kind, _ := obj["kind"].(string)
			api, _ := obj["apiVersion"].(string)
			if !treeKinds[kind] || !strings.HasPrefix(api, kobj.APIGroup+"/") {
				return nil, fmt.Errorf("%s: %s %s refused, a tree only carries Components "+
					"and Enrollments (Targets come from rondo.yaml)", rel, api, kind)
			}
			out = append(out, treeObject{Kind: kind, Name: docName(obj)})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("tree %s is empty: no Component or Enrollment", root)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}

func decodeDoc(doc string) (map[string]any, bool, error) {
	if strings.TrimSpace(doc) == "" {
		return nil, false, nil
	}
	var m map[string]any
	if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
		return nil, false, fmt.Errorf("invalid YAML: %w", err)
	}
	if _, ok := m["kind"].(string); !ok {
		return nil, false, nil
	}
	return m, true, nil
}

func docName(obj map[string]any) string {
	meta, _ := obj["metadata"].(map[string]any)
	name, _ := meta["name"].(string)
	return name
}

func splitDocs(content string) []string {
	var docs []string
	var cur strings.Builder
	for _, l := range strings.Split(content, "\n") {
		if strings.TrimRight(l, " \t") == "---" {
			docs = append(docs, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteString(l)
		cur.WriteString("\n")
	}
	return append(docs, cur.String())
}

func manifestFiles(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch filepath.Ext(d.Name()) {
		case ".yaml", ".yml", ".json":
			if !d.IsDir() {
				out = append(out, path)
			}
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		raw, err := os.ReadFile(path) //nolint:gosec // bundle tree
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0o644) //nolint:gosec // manifest, not a secret
	})
}

// BaseTreeName records the base Enrollments of one rke2 target.
func BaseTreeName(target string) string { return "base-" + target }

// ApplyBaseEnrollments places every Enrollment template of templates/ on one target, named
// <template>-<target>.
func (p *PlatformStage) ApplyBaseEnrollments(ctx context.Context, target, templates, version string) error {
	if p.dryRun {
		log.Info("DRY-RUN base Enrollments", "target", target, "templates", templates)
		return nil
	}
	files, err := manifestFiles(templates)
	if err != nil {
		return fmt.Errorf("Enrollment templates %s: %w", templates, err)
	}
	work, err := os.MkdirTemp("", "rondo-base-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	for _, f := range files {
		raw, err := os.ReadFile(f) //nolint:gosec // bundle tree
		if err != nil {
			return err
		}
		obj, ok, err := decodeDoc(string(raw))
		if err != nil || !ok {
			return fmt.Errorf("%s: unreadable Enrollment template: %v", f, err)
		}
		name := docName(obj) + "-" + target
		obj["metadata"] = map[string]any{"name": name}
		spec, _ := obj["spec"].(map[string]any)
		spec["target"] = target
		out, err := yaml.Marshal(obj)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(work, name+".yaml"), out, 0o600); err != nil {
			return err
		}
	}
	return p.ApplyTree(ctx, BaseTreeName(target), work, version)
}

func (p *PlatformStage) DeleteTarget(ctx context.Context, name string) error {
	if p.dryRun {
		log.Info("DRY-RUN would delete the Target", "target", name)
		return nil
	}
	for _, args := range [][]string{
		{"delete", "targets." + kobj.APIGroup, name},
		{"delete", "secret", kobj.KubeconfigSecretName(name)},
	} {
		if err := p.kube.Run(ctx, p.ws.Kubeconfig(), append(args, "-n", kobj.PlatformNamespace,
			"--ignore-not-found")...); err != nil {
			return err
		}
	}
	return nil
}
