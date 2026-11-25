package sandbox

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeRegistry is the part of the OCI distribution API the provider push uses.
type fakeRegistry struct {
	mu        sync.Mutex
	blobs     map[string][]byte
	manifests map[string]struct {
		mediaType string
		raw       []byte
	}
}

func newFakeRegistry(t *testing.T) (*fakeRegistry, *httptest.Server) {
	t.Helper()
	r := &fakeRegistry{blobs: map[string][]byte{}, manifests: map[string]struct {
		mediaType string
		raw       []byte
	}{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		p := strings.TrimPrefix(req.URL.Path, "/v2/")
		switch {
		case strings.HasSuffix(p, "/tags/list"):
			repo := strings.TrimSuffix(p, "/tags/list")
			var tags []string
			for k := range r.manifests {
				if repo2, tag, _ := strings.Cut(k, ":"); repo2 == repo && !strings.HasPrefix(tag, "sha256") {
					tags = append(tags, tag)
				}
			}
			if tags == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tags": tags})
		case req.Method == http.MethodHead && strings.Contains(p, "/blobs/"):
			if _, ok := r.blobs[p[strings.LastIndex(p, "/")+1:]]; ok {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case req.Method == http.MethodPost && strings.HasSuffix(p, "/blobs/uploads/"):
			w.Header().Set("Location", "/v2/"+p+"session-1?state=x")
			w.WriteHeader(http.StatusAccepted)
		case req.Method == http.MethodPut && strings.Contains(p, "/blobs/uploads/"):
			body, _ := io.ReadAll(req.Body)
			d := req.URL.Query().Get("digest")
			if d != digestOf(body) || req.URL.Query().Get("state") != "x" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			r.blobs[d] = body
			w.WriteHeader(http.StatusCreated)
		case req.Method == http.MethodPut && strings.Contains(p, "/manifests/"):
			repo, ref, _ := strings.Cut(p, "/manifests/")
			body, _ := io.ReadAll(req.Body)
			r.manifests[repo+":"+ref] = struct {
				mediaType string
				raw       []byte
			}{req.Header.Get("Content-Type"), body}
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return r, srv
}

func TestRequiredProvidersAreTerraformVMs(t *testing.T) {
	block, err := terraformBlock("../..")
	if err != nil {
		t.Fatal(err)
	}
	if got := requiredProviders(block); !slices.Equal(got, []string{"hashicorp/vsphere", "hashicorp/cloudinit"}) {
		t.Errorf("required providers = %v", got)
	}
}

func TestProviderArchiveURLFollowsThePlatform(t *testing.T) {
	d := genericDep{Name: "hashicorp/cloudinit", Version: "2.3.7",
		SourceURL: "https://releases.example/cloudinit/{version}/terraform-provider-cloudinit_{version}_linux_amd64.zip"}
	got, err := providerArchiveURL(d, "linux_arm64")
	if err != nil || got != "https://releases.example/cloudinit/2.3.7/terraform-provider-cloudinit_2.3.7_linux_arm64.zip" {
		t.Errorf("arm64 archive = %q, %v", got, err)
	}
	d.SourceURL = "https://releases.example/cloudinit.tar.gz"
	if _, err := providerArchiveURL(d, "linux_amd64"); err == nil {
		t.Error("a source_url without the platform suffix accepted")
	}
}

// The layout is the one OpenTofu's oci_mirror reads: an index tagged with the version, of
// artifactType provider, pointing at one provider-target manifest per platform whose single
// layer is the zip as archive/zip.
func TestProvidersPushesOpenTofusOCILayout(t *testing.T) {
	zip := []byte("PK\x03\x04 provider")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/cloudinit/2.3.7/terraform-provider-cloudinit_2.3.7_linux_arm64.zip" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(zip)
	}))
	defer upstream.Close()
	reg, srv := newFakeRegistry(t)
	pub, _ := testPublisher(t, strings.TrimPrefix(srv.URL, "http://"))
	deps := &depsFile{TofuProviders: []genericDep{{Name: "hashicorp/cloudinit", Version: "2.3.7",
		SourceURL: upstream.URL + "/cloudinit/{version}/terraform-provider-cloudinit_{version}_linux_amd64.zip"}}}
	ctx := context.Background()
	if err := pub.providers(ctx, "tofu", []string{"hashicorp/cloudinit"}, deps, "linux_arm64"); err != nil {
		t.Fatal(err)
	}
	idx, ok := reg.manifests["tofu/hashicorp/cloudinit:2.3.7"]
	if !ok || idx.mediaType != mediaIndex {
		t.Fatalf("index tagged 2.3.7: %+v (manifests %v)", idx, len(reg.manifests))
	}
	var index struct {
		ArtifactType string          `json:"artifactType"`
		Manifests    []ociDescriptor `json:"manifests"`
	}
	if err := json.Unmarshal(idx.raw, &index); err != nil {
		t.Fatal(err)
	}
	if index.ArtifactType != "application/vnd.opentofu.provider" || len(index.Manifests) != 1 {
		t.Fatalf("index = %s", idx.raw)
	}
	m := index.Manifests[0]
	if m.ArtifactType != "application/vnd.opentofu.provider-target" || m.Platform == nil ||
		m.Platform.OS != "linux" || m.Platform.Architecture != "arm64" {
		t.Errorf("platform descriptor = %+v", m)
	}
	pm, ok := reg.manifests["tofu/hashicorp/cloudinit:"+m.Digest]
	if !ok || pm.mediaType != mediaManifest || digestOf(pm.raw) != m.Digest {
		t.Fatalf("platform manifest %s not pushed under its digest", m.Digest)
	}
	var manifest struct {
		ArtifactType string          `json:"artifactType"`
		Config       ociDescriptor   `json:"config"`
		Layers       []ociDescriptor `json:"layers"`
	}
	if err := json.Unmarshal(pm.raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.ArtifactType != "application/vnd.opentofu.provider-target" || len(manifest.Layers) != 1 ||
		manifest.Layers[0].MediaType != "archive/zip" || manifest.Layers[0].Digest != digestOf(zip) {
		t.Errorf("platform manifest = %s", pm.raw)
	}
	if string(reg.blobs[digestOf(zip)]) != string(zip) || reg.blobs[manifest.Config.Digest] == nil {
		t.Error("zip or config blob missing")
	}

	// Already there: nothing downloaded again.
	upstream.Close()
	if err := pub.providers(ctx, "tofu", []string{"hashicorp/cloudinit"}, deps, "linux_arm64"); err != nil {
		t.Errorf("second run: %v", err)
	}
	if err := pub.providers(ctx, "tofu", []string{"hashicorp/vsphere"}, deps, "linux_arm64"); err == nil ||
		!strings.Contains(err.Error(), "absent from the tofu_providers") {
		t.Errorf("a provider dependencies.yaml does not pin: %v", err)
	}
}
