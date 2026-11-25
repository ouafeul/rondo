// Package tofustate selects the backend an OpenTofu module keeps its state in. The modules declare
// `backend "http" {}`; an s3 state adds an override file that turns it into `backend "s3" {}`, and
// both are configured at `tofu init` (partial configuration).
package tofustate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// The backends.
const (
	HTTP = "http"
	S3   = "s3"
)

// OverrideFile is the override that selects the s3 backend in a module directory.
const OverrideFile = "backend_override.tf"

const s3Override = "terraform {\n  backend \"s3\" {}\n}\n"

// DefaultRegion is the region of an S3 state that names none, the one MinIO and Ceph answer to.
const DefaultRegion = "us-east-1"

// S3Bucket is an s3 state backend; KeyPrefix holds every path segment above the stack.
type S3Bucket struct {
	Bucket       string `json:"bucket"`
	Endpoint     string `json:"endpoint,omitempty"`
	Region       string `json:"region,omitempty"`
	KeyPrefix    string `json:"keyPrefix,omitempty"`
	UsePathStyle bool   `json:"usePathStyle,omitempty"`
}

// Key is the object key of the state of a stack, path its segments under KeyPrefix.
func (b S3Bucket) Key(stack ...string) string {
	parts := append([]string{b.KeyPrefix}, stack...)
	return strings.TrimPrefix(path.Join(append(parts, "terraform.tfstate")...), "/")
}

// BackendConfig is the -backend-config file of the state at key: S3 API compatibility settings
// (no AWS account, STS or instance metadata to ask), and a lock file next to the state.
func (b S3Bucket) BackendConfig(key string) string {
	region := b.Region
	if region == "" {
		region = DefaultRegion
	}
	var s strings.Builder
	attr := func(name, value string) { fmt.Fprintf(&s, "%s = %s\n", name, value) }
	attr("bucket", quote(b.Bucket))
	attr("key", quote(key))
	attr("region", quote(region))
	if b.Endpoint != "" {
		attr("endpoints", "{ s3 = "+quote(b.Endpoint)+" }")
	}
	attr("use_path_style", fmt.Sprint(b.UsePathStyle))
	attr("use_lockfile", "true")
	for _, skip := range []string{"skip_credentials_validation", "skip_region_validation",
		"skip_requesting_account_id", "skip_metadata_api_check", "skip_s3_checksum"} {
		attr(skip, "true")
	}
	return s.String()
}

// Encode is b as one environment value, Decode's input.
func (b S3Bucket) Encode() string {
	raw, _ := json.Marshal(b)
	return string(raw)
}

// Decode reads what Encode wrote.
func Decode(s string) (S3Bucket, error) {
	var b S3Bucket
	if err := json.Unmarshal([]byte(s), &b); err != nil {
		return b, fmt.Errorf("s3 state settings: %w", err)
	}
	if b.Bucket == "" {
		return b, errors.New("s3 state settings: no bucket")
	}
	return b, nil
}

// Select makes the module in dir use backend: s3 writes the override, http removes it.
func Select(dir, backend string) error {
	p := filepath.Join(dir, OverrideFile)
	switch backend {
	case S3:
		return os.WriteFile(p, []byte(s3Override), 0o600)
	case HTTP, "":
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return fmt.Errorf("unknown state backend %q", backend)
}

// quote is s as an HCL string literal, template sequences escaped.
func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "${", "$${", "%{", "%%{")
	return `"` + r.Replace(s) + `"`
}
