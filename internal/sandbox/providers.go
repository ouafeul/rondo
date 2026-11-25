package sandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"

	"rondo/internal/log"
	"rondo/internal/runner"
)

// The OCI layout OpenTofu's oci_mirror reads (OpenTofu docs, "Provider mirrors in OCI
// registries"): one image index per provider version, tagged with the version, whose manifests
// are one per platform, each with the provider's zip as its single layer.
const (
	mediaIndex           = "application/vnd.oci.image.index.v1+json"
	mediaManifest        = "application/vnd.oci.image.manifest.v1+json"
	mediaEmpty           = "application/vnd.oci.empty.v1+json"
	mediaZip             = "archive/zip"
	artifactProvider     = "application/vnd.opentofu.provider"
	artifactProviderTgt  = "application/vnd.opentofu.provider-target"
	annotationTitle      = "org.opencontainers.image.title"
	providerArchiveRegex = `_linux_amd64\.zip$`
)

var (
	emptyConfig = []byte("{}")
	// source = "hashicorp/vsphere" in a required_providers block
	providerSourceRe = regexp.MustCompile(`(?m)^\s*source\s*=\s*"([^"]+)"`)
	linuxAMD64Zip    = regexp.MustCompile(providerArchiveRegex)
)

// requiredProviders are the provider sources (<namespace>/<type>) a module's terraform block
// requires.
func requiredProviders(terraformBlock string) []string {
	var out []string
	for _, m := range providerSourceRe.FindAllStringSubmatch(terraformBlock, -1) {
		out = append(out, strings.TrimPrefix(m[1], "registry.opentofu.org/"))
	}
	return out
}

// providerArchiveURL is the upstream zip of a dependencies.yaml provider for platform
// (linux_amd64, linux_arm64): the manifest pins the linux_amd64 one, the other platforms sit
// next to it under the same name.
func providerArchiveURL(d genericDep, platform string) (string, error) {
	src := strings.ReplaceAll(d.SourceURL, "{version}", d.Version)
	if !linuxAMD64Zip.MatchString(src) {
		return "", fmt.Errorf("provider %s: source_url %q does not end in _linux_amd64.zip", d.Name, d.SourceURL)
	}
	return linuxAMD64Zip.ReplaceAllString(src, "_"+platform+".zip"), nil
}

// providers puts the providers a module requires on the registry as an OpenTofu OCI provider
// mirror, <repository>/<namespace>/<type>:<version>, from the versions dependencies.yaml pins:
// what a site pushes with oras for artifacts.tofu.providerOCI. platform is the Jobs' (linux_arm64
// on an ARM workstation).
func (pub *publisher) providers(ctx context.Context, repository string, required []string, deps *depsFile,
	platform string) error {
	goos, arch, ok := strings.Cut(platform, "_")
	if !ok {
		return fmt.Errorf("provider platform %q is not <os>_<arch>", platform)
	}
	for _, name := range required {
		i := -1
		for j, d := range deps.TofuProviders {
			if d.Name == name {
				i = j
			}
		}
		if i < 0 {
			return fmt.Errorf("provider %s: required by RONDO's machine module, absent from the tofu_providers "+
				"of dependencies.yaml: a site would not mirror it", name)
		}
		d := deps.TofuProviders[i]
		repo := repository + "/" + name
		tag := strings.ReplaceAll(d.Version, "+", "_")
		present, err := pub.hasTag(ctx, repo, tag)
		if err != nil {
			return err
		}
		if present {
			log.Info("provider already on the sandbox", "provider", name, "version", d.Version)
			continue
		}
		src, err := providerArchiveURL(d, platform)
		if err != nil {
			return err
		}
		zip, err := pub.download(ctx, src)
		if err != nil {
			return fmt.Errorf("provider %s: %w", name, err)
		}
		if err := pub.pushProvider(ctx, repo, tag, path.Base(src), zip, goos, arch); err != nil {
			return fmt.Errorf("provider %s: %w", name, err)
		}
		log.Info("provider mirrored (OCI)", "provider", name, "version", d.Version, "platform", platform,
			"repository", repo)
	}
	return nil
}

// jobPlatform is the provider platform of the Jobs, the one of Docker's server, whose
// architecture the k3d nodes have.
func (pub *publisher) jobPlatform(ctx context.Context) (string, error) {
	arch, err := pub.run.Output(ctx, runner.Cmd{Name: "docker", Args: []string{"version", "-f", "{{.Server.Arch}}"}})
	if err != nil {
		return "", fmt.Errorf("docker server architecture: %w", err)
	}
	return "linux_" + strings.TrimSpace(arch), nil
}

func (pub *publisher) download(ctx context.Context, src string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	resp, err := pub.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", src, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

type ociDescriptor struct {
	MediaType    string            `json:"mediaType"`
	ArtifactType string            `json:"artifactType,omitempty"`
	Digest       string            `json:"digest"`
	Size         int               `json:"size"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	Platform     *ociPlatform      `json:"platform,omitempty"`
}

type ociPlatform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
}

func digestOf(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// pushProvider pushes one provider version for one platform: the zip and the empty config as
// blobs, the platform manifest, then the index under the version tag.
func (pub *publisher) pushProvider(ctx context.Context, repo, tag, filename string, zip []byte, goos, arch string) error {
	for _, blob := range [][]byte{zip, emptyConfig} {
		if err := pub.pushBlob(ctx, repo, blob); err != nil {
			return err
		}
	}
	manifest, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     mediaManifest,
		"artifactType":  artifactProviderTgt,
		"config":        ociDescriptor{MediaType: mediaEmpty, Digest: digestOf(emptyConfig), Size: len(emptyConfig)},
		"layers": []ociDescriptor{{MediaType: mediaZip, Digest: digestOf(zip), Size: len(zip),
			Annotations: map[string]string{annotationTitle: filename}}},
	})
	if err != nil {
		return err
	}
	if err := pub.putManifest(ctx, repo, digestOf(manifest), mediaManifest, manifest); err != nil {
		return err
	}
	index, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     mediaIndex,
		"artifactType":  artifactProvider,
		"manifests": []ociDescriptor{{MediaType: mediaManifest, ArtifactType: artifactProviderTgt,
			Digest: digestOf(manifest), Size: len(manifest), Platform: &ociPlatform{OS: goos, Architecture: arch}}},
	})
	if err != nil {
		return err
	}
	return pub.putManifest(ctx, repo, tag, mediaIndex, index)
}

func (pub *publisher) pushBlob(ctx context.Context, repo string, blob []byte) error {
	base := "http://" + pub.registry + "/v2/" + repo
	digest := digestOf(blob)
	if resp, err := pub.do(ctx, http.MethodHead, base+"/blobs/"+digest, "", nil); err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return nil
		}
	}
	resp, err := pub.do(ctx, http.MethodPost, base+"/blobs/uploads/", "", nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("blob upload to %s: HTTP %d", repo, resp.StatusCode)
	}
	loc, err := resp.Request.URL.Parse(resp.Header.Get("Location"))
	if err != nil {
		return fmt.Errorf("blob upload to %s: Location: %w", repo, err)
	}
	q := loc.Query()
	q.Set("digest", digest)
	loc.RawQuery = q.Encode()
	resp, err = pub.do(ctx, http.MethodPut, loc.String(), "application/octet-stream", blob)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("blob %s to %s: HTTP %d %s", digest, repo, resp.StatusCode, bytes.TrimSpace(body))
	}
	return nil
}

func (pub *publisher) putManifest(ctx context.Context, repo, ref, mediaType string, raw []byte) error {
	resp, err := pub.do(ctx, http.MethodPut,
		"http://"+pub.registry+"/v2/"+repo+"/manifests/"+url.PathEscape(ref), mediaType, raw)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("manifest %s:%s: HTTP %d %s", repo, ref, resp.StatusCode, bytes.TrimSpace(body))
	}
	return nil
}

func (pub *publisher) do(ctx context.Context, method, target, contentType string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := pub.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("registry unreachable (%s): %w", pub.registry, err)
	}
	return resp, nil
}
