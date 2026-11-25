package sandbox

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rondo/internal/platform"
	"rondo/internal/runner"
)

func testPublisher(t *testing.T, registry string) (*publisher, *runner.Fake) {
	t.Helper()
	fake := &runner.Fake{Errs: map[string]error{}}
	return &publisher{registry: registry, run: fake, work: t.TempDir(),
		http: &http.Client{Timeout: 5 * time.Second}}, fake
}

func TestSandboxVersionKeepsSemver(t *testing.T) {
	if got := sandboxVersion("1.0.0", "ab12"); got != "1.0.0-sbxab12" {
		t.Errorf("version = %q", got)
	}
	if got := sandboxVersion("1.0.0-rc1", "ab12"); got != "1.0.0-rc1.sbxab12" {
		t.Errorf("pre-release = %q", got)
	}
}

func TestHashDirFollowsContent(t *testing.T) {
	dir := t.TempDir()
	if err := copyDir("testdata/component/apps/web", dir); err != nil {
		t.Fatal(err)
	}
	a, _ := hashDir(dir)
	b, _ := hashDir(dir)
	if err := os.WriteFile(filepath.Join(dir, "templates", "cm.yaml"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, _ := hashDir(dir)
	if a != b || a == c || len(a) != 8 {
		t.Errorf("hashes %q %q %q: want stable, then changed by a template", a, b, c)
	}
}

func TestChartPublishesFirstPartyUnderAContentVersion(t *testing.T) {
	pub, fake := testPublisher(t, "127.0.0.1:5100")
	apps, err := firstPartyCharts("testdata/component/apps")
	if err != nil {
		t.Fatal(err)
	}
	rewrites := map[string]string{}
	if err := pub.chart(context.Background(), chartRef{component: "web", name: "web", version: "1.0.0"},
		apps, &depsFile{}, rewrites); err != nil {
		t.Fatal(err)
	}
	v := rewrites["web@1.0.0"]
	if !strings.HasPrefix(v, "1.0.0-sbx") {
		t.Fatalf("rewrites = %v", rewrites)
	}
	if len(fake.Calls) != 2 || fake.Calls[0].Args[0] != "package" || fake.Calls[1].Args[0] != "push" ||
		!strings.Contains(fake.Calls[1].String(), "web-"+v+".tgz oci://127.0.0.1:5100/rondo-helm-local --plain-http") {
		t.Errorf("calls = %v", fake.Calls)
	}
}

func TestChartRefusesWhatTheSiteWouldNotHave(t *testing.T) {
	pub, _ := testPublisher(t, "r")
	apps, _ := firstPartyCharts("testdata/component/apps")
	err := pub.chart(context.Background(), chartRef{component: "web", name: "web", version: "0.9.0"},
		apps, &depsFile{}, map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "align the two") {
		t.Errorf("pinned version differs from the apps/ chart: %v", err)
	}
	err = pub.chart(context.Background(), chartRef{component: "x", name: "absent", version: "1.0.0"},
		apps, &depsFile{}, nil)
	if err == nil || !strings.Contains(err.Error(), "the site would never have it") {
		t.Errorf("chart not found: %v", err)
	}
}

func TestMirrorChartSkipsWhatTheRegistryHas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/rondo-helm-local/fluent-bit/tags/list" {
			_, _ = w.Write([]byte(`{"tags":["0.57.2"]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	pub, fake := testPublisher(t, strings.TrimPrefix(srv.URL, "http://"))
	deps, err := readDeps("testdata/component/manifests/dependencies.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := pub.chart(context.Background(), chartRef{component: "agent", name: "fluent-bit", version: "0.57.2"},
		nil, deps, nil); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("chart already present downloaded again: %v", fake.Calls)
	}
	err = pub.mirrorChart(context.Background(), "absent", "1.0.0", "https://charts.example")
	if err == nil || len(fake.Calls) != 1 || !strings.Contains(fake.Calls[0].String(), "--repo https://charts.example absent") {
		t.Errorf("missing chart: %v, calls %v", err, fake.Calls)
	}
}

func TestGenericDownloadsOnce(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/v1/p-v1.zip" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("zip"))
	}))
	defer srv.Close()
	pub, _ := testPublisher(t, "r")
	root := t.TempDir()
	dep := genericDep{Name: "p", Version: "v1", SourceURL: srv.URL + "/{version}/p-{version}.zip",
		TargetRepo: "rondo-generic-local", TargetPath: "plugins/{version}/p.zip"}
	for i := 0; i < 2; i++ {
		if err := pub.generic(context.Background(), root, dep); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(root, "rondo-generic-local", "plugins", "v1", "p.zip"))
	if err != nil || string(raw) != "zip" || hits != 1 {
		t.Errorf("file %q (%v), %d download(s)", raw, err, hits)
	}
	dep.Version = "v2"
	if err := pub.generic(context.Background(), root, dep); err == nil {
		t.Error("source answering 404 accepted")
	}
}

func TestImageTagsFromItsChart(t *testing.T) {
	pub, fake := testPublisher(t, "127.0.0.1:5100")
	img := imageBuild{Name: "acme/web", Context: "build/web", TagFromChart: "apps/web"}
	if err := pub.image(context.Background(), "testdata/component", img); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 2 || !strings.Contains(fake.Calls[0].String(), "-t 127.0.0.1:5100/acme/web:2.1.0") ||
		!strings.Contains(fake.Calls[1].String(), "push -q 127.0.0.1:5100/acme/web:2.1.0") {
		t.Errorf("calls = %v", fake.Calls)
	}
	if err := pub.image(context.Background(), "testdata/component", imageBuild{Name: "x"}); err == nil {
		t.Error("image without a tag accepted")
	}
}

func TestPrepareTreePinsWhatTheSandboxBuilt(t *testing.T) {
	s, p := planned(t)
	tr, err := platform.Load(p.Platform)
	if err != nil {
		t.Fatal(err)
	}
	rw := treeRewrites{charts: map[string]string{"web@1.0.0": "1.0.0-sbxab12"},
		digests: map[string]string{"vm/components/vm/1.0.0/payload.tar.gz": "sha256:" + strings.Repeat("a", 64)},
		ansible: map[string]string{}}
	dst, err := prepareTree(s, p, tr, rw)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dst, "components", "web.yaml"))
	if err != nil || !strings.Contains(string(raw), "version: 1.0.0-sbxab12") {
		t.Errorf("web.yaml = %s (%v)", raw, err)
	}
	raw, err = os.ReadFile(filepath.Join(dst, "components", "vm.yaml"))
	if err != nil || !strings.Contains(string(raw), "digest: sha256:aaaa") {
		t.Errorf("vm.yaml = %s (%v)", raw, err)
	}
	if got := chartsOf(tr); len(got) != 3 {
		t.Errorf("charts = %v", got)
	}
}
