package sandbox

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// vmModule is the tarball the vm-apply Job downloads in place of terraform/vm. Its variables
// and outputs are terraform/vm's own, read from RONDO's tree, and so are the locals the outputs
// derive from; what goes is vSphere: the machines are the sandbox's containers, already running
// at the IPs the Target gives. tofu still plans, applies and keeps its state on the stand-in.
func vmModule(rondoDir string) ([]byte, error) {
	dir := filepath.Join(rondoDir, "terraform", "vm")
	files := map[string][]byte{}
	for _, name := range []string{"variables.tf", "outputs.tf"} {
		raw, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // RONDO's module
		if err != nil {
			return nil, fmt.Errorf("RONDO machine module: %w", err)
		}
		files[name] = raw
	}
	real, err := os.ReadFile(filepath.Join(dir, "main.tf")) //nolint:gosec // RONDO's module
	if err != nil {
		return nil, fmt.Errorf("RONDO machine module: %w", err)
	}
	locals, err := machineLocals(string(real))
	if err != nil {
		return nil, err
	}
	files["main.tf"] = []byte(`# rondo-sandbox: terraform/vm without vSphere. The machines are the sandbox containers, already
# started at the IPs the Target gives; variables, outputs and locals are those of terraform/vm.
terraform {
  required_version = ">= 1.10.0"
  backend "http" {}
}

locals {
` + locals + `
}

resource "terraform_data" "machine" {
  for_each = local.machines
  input    = each.value
}
`)
	return tarGz(files)
}

var (
	fqdnSuffixLocal = regexp.MustCompile(`(?m)^  fqdn_suffix = .*$`)
	machinesLocal   = regexp.MustCompile(`(?ms)^  machines = merge\(\[$.*?^  \]\.\.\.\)$`)
)

// machineLocals are the locals of terraform/vm's main.tf that its outputs read.
func machineLocals(mainTF string) (string, error) {
	fqdn := fqdnSuffixLocal.FindString(mainTF)
	machines := machinesLocal.FindString(mainTF)
	if fqdn == "" || machines == "" {
		return "", fmt.Errorf("terraform/vm/main.tf: locals fqdn_suffix and machines not found, " +
			"the sandbox machine module reuses them (internal/sandbox/vmmodule.go)")
	}
	return fqdn + "\n\n" + machines, nil
}

// tarGzDir archives a directory as CI does (`tar czf … -C dir .`), reproducibly: the same files
// give the same bytes, so the same digest.
func tarGzDir(dir string) ([]byte, error) {
	files := map[string][]byte{}
	modes := map[string]fs.FileMode{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(p) //nolint:gosec // the component's or RONDO's sources
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = raw
		modes[filepath.ToSlash(rel)] = info.Mode().Perm()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return tarGzModes(files, modes)
}

func tarGz(files map[string][]byte) ([]byte, error) { return tarGzModes(files, nil) }

func tarGzModes(files map[string][]byte, modes map[string]fs.FileMode) ([]byte, error) {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	dirs := map[string]bool{}
	for _, name := range names {
		for d := filepath.Dir(name); d != "." && !dirs[d]; d = filepath.Dir(d) {
			dirs[d] = true
		}
	}
	dirList := make([]string, 0, len(dirs))
	for d := range dirs {
		dirList = append(dirList, d)
	}
	sort.Strings(dirList)
	for _, d := range dirList {
		if err := tw.WriteHeader(&tar.Header{Name: "./" + d + "/", Mode: 0o755, Typeflag: tar.TypeDir,
			ModTime: time.Unix(0, 0), Format: tar.FormatPAX}); err != nil {
			return nil, err
		}
	}
	for _, name := range names {
		mode := fs.FileMode(0o644)
		if m, ok := modes[name]; ok {
			mode = m
		}
		raw := files[name]
		if err := tw.WriteHeader(&tar.Header{Name: "./" + strings.TrimPrefix(name, "./"), Mode: int64(mode),
			Size: int64(len(raw)), Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0), Format: tar.FormatPAX}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(raw); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
