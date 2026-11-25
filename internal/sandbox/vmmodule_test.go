package sandbox

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"strings"
	"testing"
)

func untar(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			b, _ := io.ReadAll(tr)
			out[h.Name] = string(b)
		}
	}
}

// The module keeps terraform/vm's interface: its variables, its outputs and the locals they read.
func TestVMModuleKeepsTerraformVMsInterface(t *testing.T) {
	raw, err := vmModule("../..")
	if err != nil {
		t.Fatal(err)
	}
	files := untar(t, raw)
	for _, name := range []string{"variables.tf", "outputs.tf"} {
		want, _ := os.ReadFile("../../terraform/vm/" + name)
		if files["./"+name] != string(want) {
			t.Errorf("%s is not the one from terraform/vm", name)
		}
	}
	main := files["./main.tf"]
	for _, want := range []string{`backend "http" {}`, "machines = merge([", "fqdn_suffix =", `resource "terraform_data" "machine"`} {
		if !strings.Contains(main, want) {
			t.Errorf("main.tf without %q:\n%s", want, main)
		}
	}
	if strings.Contains(main, "vsphere") {
		t.Errorf("main.tf still talks to vSphere:\n%s", main)
	}
	again, _ := vmModule("../..")
	if !bytes.Equal(raw, again) {
		t.Error("two archives of the same module differ")
	}
	if _, err := machineLocals("locals {}"); err == nil {
		t.Error("a main.tf without the expected locals accepted")
	}
}
