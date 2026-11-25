package tofustate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKey(t *testing.T) {
	for _, tc := range []struct {
		prefix string
		stack  []string
		want   string
	}{
		{"", []string{"k3s"}, "k3s/terraform.tfstate"},
		{"prod", []string{"vm", "mft"}, "prod/vm/mft/terraform.tfstate"},
		{"sites/prod", []string{"rke2-a"}, "sites/prod/rke2-a/terraform.tfstate"},
	} {
		if got := (S3Bucket{KeyPrefix: tc.prefix}).Key(tc.stack...); got != tc.want {
			t.Errorf("Key(%q, %v) = %q, want %q", tc.prefix, tc.stack, got, tc.want)
		}
	}
}

func TestBackendConfig(t *testing.T) {
	got := S3Bucket{Bucket: "b", Endpoint: "https://s3.local", UsePathStyle: true}.BackendConfig("p/k3s/terraform.tfstate")
	want := `bucket = "b"
key = "p/k3s/terraform.tfstate"
region = "us-east-1"
endpoints = { s3 = "https://s3.local" }
use_path_style = true
use_lockfile = true
skip_credentials_validation = true
skip_region_validation = true
skip_requesting_account_id = true
skip_metadata_api_check = true
skip_s3_checksum = true
`
	if got != want {
		t.Errorf("config:\n%s\nwant:\n%s", got, want)
	}
	if aws := (S3Bucket{Bucket: "b", Region: "eu-west-3"}).BackendConfig("k"); strings.Contains(aws, "endpoints") ||
		!strings.Contains(aws, `region = "eu-west-3"`) {
		t.Errorf("AWS bucket config:\n%s", aws)
	}
	if q := quote(`a"${b}\`); q != `"a\"$${b}\\"` {
		t.Errorf("quote = %s", q)
	}
}

func TestEncodeDecode(t *testing.T) {
	b := S3Bucket{Bucket: "b", Endpoint: "https://s3", Region: "r", KeyPrefix: "p", UsePathStyle: true}
	got, err := Decode(b.Encode())
	if err != nil || got != b {
		t.Errorf("Decode(Encode) = %+v, %v", got, err)
	}
	if _, err := Decode(`{"endpoint":"x"}`); err == nil {
		t.Error("a bucket without a name accepted")
	}
	if _, err := Decode("not json"); err == nil {
		t.Error("garbage accepted")
	}
}

func TestSelect(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, OverrideFile)
	if err := Select(dir, S3); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(p); err != nil || !strings.Contains(string(raw), `backend "s3" {}`) {
		t.Fatalf("override = %q, %v", raw, err)
	}
	if err := Select(dir, HTTP); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("override left with http: %v", err)
	}
	if err := Select(dir, HTTP); err != nil {
		t.Errorf("http without an override: %v", err)
	}
	if err := Select(dir, "gcs"); err == nil {
		t.Error("unknown backend accepted")
	}
}
