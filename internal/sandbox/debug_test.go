package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"rondo/api/v1alpha1"
)

func TestCheckPassesTheTestComponent(t *testing.T) {
	if err := RunCheck(testSettings(t), "testdata/component", "testdata/component/rondo-sandbox.yaml"); err != nil {
		t.Errorf("test component: %v", err)
	}
}

func TestCheckRefusesWhatDeployWouldRefuse(t *testing.T) {
	for name, c := range map[string]struct{ rel, content, want string }{
		"apps/ chart out of line": {"apps/web/Chart.yaml", "apiVersion: v2\nname: web\nversion: 1.1.0\nappVersion: \"2.1.0\"\n", "1 error"},
		"image without Dockerfile": {"rondo-sandbox.yaml", "name: acme\nimages: [{name: acme/web, context: absent, tag: \"1\"}]\n" +
			"targets:\n  downstreams: [{name: obs, roles: [monitoring]}]\n" +
			"  vsphere: [{name: dc1, roles: [dmz], componentValues: {helloMessage: x}}]\n" +
			"artifacts: [{path: components/vm/1.0.0/payload.tar.gz, dir: vm/payload}]\n", "1 error"},
		"role without Target": {"rondo-sandbox.yaml", "name: acme\n", "which no Target holds"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := tree(t, c.rel, c.content)
			err := RunCheck(testSettings(t), dir, filepath.Join(dir, sandboxFileName))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %v, want %q", err, c.want)
			}
		})
	}
}

func TestBelongsFindsAReleasesPods(t *testing.T) {
	ws := map[string]bool{"Deployment/web": true, "CronJob/web-report": true, "DaemonSet/agent": true}
	for name, c := range map[string]struct {
		pod  string
		want bool
	}{
		"instance label":         {`{"metadata":{"name":"x","labels":{"app.kubernetes.io/instance":"web"}}}`, true},
		"release label":          {`{"metadata":{"name":"x","labels":{"release":"web"}}}`, true},
		"shipped ReplicaSet":     {`{"metadata":{"name":"web-5d8-abc","ownerReferences":[{"kind":"ReplicaSet","name":"web-5d8"}]}}`, true},
		"Job of a CronJob":       {`{"metadata":{"name":"r","ownerReferences":[{"kind":"Job","name":"web-report-29899"}]}}`, true},
		"shipped DaemonSet":      {`{"metadata":{"name":"a","ownerReferences":[{"kind":"DaemonSet","name":"agent"}]}}`, true},
		"created by an operator": {`{"metadata":{"name":"prometheus-web-kube-prometheus-0"}}`, true},
		"other release":          {`{"metadata":{"name":"webhook-1","ownerReferences":[{"kind":"ReplicaSet","name":"webhook-9f"}]}}`, false},
	} {
		var p pod
		if err := json.Unmarshal([]byte(c.pod), &p); err != nil {
			t.Fatal(err)
		}
		if got := belongs(p, ws, "web"); got != c.want {
			t.Errorf("%s: belongs = %v", name, got)
		}
	}
}

// debugSandbox is a sandbox whose Enrollment "web" delivered a Deployment and a CronJob.
func debugSandbox(t *testing.T) (*sandbox, *bytes.Buffer, func(string) bool) {
	t.Helper()
	w, enr := verdictWorld(t)
	enr.Status.Inventory = []v1alpha1.InventoryEntry{
		{Group: "apps", Version: "v1", Kind: "Deployment", Namespace: "web", Name: "web"},
		{Group: "batch", Version: "v1", Kind: "CronJob", Namespace: "web", Name: "web-report"},
		{Version: "v1", Kind: "ConfigMap", Namespace: "web", Name: "web"},
	}
	w.components["web"].Spec.Chart.OCI = "oci://" + registryHost + "/" + helmRepo + "/web"
	sb, fake := oneCluster(t, "127.0.0.1:5100")
	sb.p.Dir, sb.p.Images = "testdata/component", []imageBuild{{Name: "acme/web", TagFromChart: "apps/web"}}
	sb.p = nil
	program(t, fake, enr, w, `{"items":[]}`)
	fake.Outputs[kubectl+" get pods -o json -n web"] = `{"items":[
		{"metadata":{"name":"web-5d8-abc","ownerReferences":[{"kind":"ReplicaSet","name":"web-5d8"}]},
		 "status":{"phase":"Running","containerStatuses":[{"ready":true,"restartCount":1}]}},
		{"metadata":{"name":"other-1"},"status":{"phase":"Running"}}]}`
	called := func(sub string) bool {
		for _, c := range fake.Calls {
			if strings.Contains(c.String(), sub) {
				return true
			}
		}
		return false
	}
	return sb, &bytes.Buffer{}, called
}

func TestValuesPrintsWhatRONDOApplies(t *testing.T) {
	sb, out, _ := debugSandbox(t)
	if err := sb.values(context.Background(), "web", out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"image: reg/web", "release web in web", "Target management (cluster rondo-sbx)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("values without %q:\n%s", want, out)
		}
	}
	if err := sb.values(context.Background(), "absent", out); err == nil || !strings.Contains(err.Error(), "web") {
		t.Errorf("unknown Enrollment: %v, want the component's listed", err)
	}
}

func TestTemplateRendersFromZotWithTheToolboxHelm(t *testing.T) {
	sb, _, _ := debugSandbox(t)
	l, err := sb.locate(context.Background(), "web")
	if err != nil {
		t.Fatal(err)
	}
	box := sb.templateCmd(l, "/tmp/d", true).String()
	for _, want := range []string{"docker run --rm --network host --entrypoint helm", toolboxImage,
		"template web oci://127.0.0.1:5100/rondo-helm-local/web --version 1.0.0 --namespace web --plain-http -f /w/values.json"} {
		if !strings.Contains(box, want) {
			t.Errorf("command without %q: %s", want, box)
		}
	}
	if host := sb.templateCmd(l, "/tmp/d", false).String(); !strings.HasPrefix(host, "helm template web") ||
		!strings.HasSuffix(host, "-f /tmp/d/values.json") {
		t.Errorf("workstation helm: %s", host)
	}
}

func TestPodsListsOnlyTheReleasesPods(t *testing.T) {
	sb, out, _ := debugSandbox(t)
	if err := sb.listPods(context.Background(), "web", out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "web-5d8-abc") || !strings.Contains(out.String(), "1/1") ||
		strings.Contains(out.String(), "other-1") {
		t.Errorf("pods:\n%s", out)
	}
}

func TestRestartReloadsImagesAndRunsCronJobs(t *testing.T) {
	sb, _, called := debugSandbox(t)
	if err := sb.restart(context.Background(), "web"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"docker exec k3d-rondo-sbx-server-0 crictl rmi mirror.sandbox.test/acme/web:2.1.0",
		"-n web rollout restart deployment/web",
		"--from=cronjob/web-report",
	} {
		if !called(want) {
			t.Errorf("restart did not run %q", want)
		}
	}
	if called("configmap") {
		t.Error("a ConfigMap has nothing to restart")
	}
}

func TestWhyFailsOnlyWhenSomethingBlocks(t *testing.T) {
	sb, _, _ := debugSandbox(t)
	if err := sb.why(context.Background(), ""); err != nil {
		t.Errorf("everything is Ready: %v", err)
	}
	if err := sb.why(context.Background(), "absent"); err == nil {
		t.Error("unknown Enrollment accepted")
	}
	w, enr := verdictWorld(t)
	enr.Status.Conditions = nil
	sb2, fake := oneCluster(t, "")
	program(t, fake, enr, w, `{"items":[]}`)
	if err := sb2.why(context.Background(), "web"); err == nil || !strings.Contains(err.Error(), "blocking") {
		t.Errorf("Enrollment not Ready: %v", err)
	}
}
