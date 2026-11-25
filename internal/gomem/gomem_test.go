package gomem

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBudgetIsAFractionOfTheCgroupLimit(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "memory.max")
	if err := os.WriteFile(p, []byte("1073741824\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := readCgroupLimit([]string{p})
	if got != 1073741824 {
		t.Fatalf("limit read = %d, want 1 Gi", got)
	}
	budget := got / 100 * marge
	if budget >= got {
		t.Errorf("budget %d >= limit %d: the GC would aim at the limit itself and get "+
			"killed before collecting; the margin covers what the GC does not count "+
			"(stacks, mappings, page cache charged to the container)", budget, got)
	}
}

func TestNoLimitReturnsZero(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"v2 no limit": "max\n",
		"v1 no limit": "9223372036854771712\n",
		"empty":       "\n",
		"unreadable":  "plenty\n",
		"zero limit":  "0\n",
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, "lim")
			if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := readCgroupLimit([]string{p}); got != 0 {
				t.Errorf("%q should return 0, got %d", content, got)
			}
		})
	}
}

func TestExplicitGomemlimitWins(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "123MiB")
	if got := Apply(); got != 0 {
		t.Errorf("explicit GOMEMLIMIT: Apply must change nothing, got %d", got)
	}
}
