package gomem

import (
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

const marge = 80

var cgroupPaths = []string{
	"/sys/fs/cgroup/memory.max",
	"/sys/fs/cgroup/memory/memory.limit_in_bytes",
}

func Apply() int64 {
	if v := strings.TrimSpace(os.Getenv("GOMEMLIMIT")); v != "" {
		return 0
	}
	limit := readCgroupLimit(cgroupPaths)
	if limit <= 0 {
		return 0
	}
	budget := limit / 100 * marge
	debug.SetMemoryLimit(budget)
	return budget
}

func readCgroupLimit(paths []string) int64 {
	for _, p := range paths {
		raw, err := os.ReadFile(p) //nolint:gosec // constant cgroup path
		if err != nil {
			continue
		}
		s := strings.TrimSpace(string(raw))
		if s == "" || s == "max" {
			continue
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n <= 0 {
			continue
		}
		if n > 1<<50 {
			continue
		}
		return n
	}
	return 0
}
