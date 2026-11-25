package paths

import (
	"os"
	"path/filepath"
)

func Home() string {
	if h := os.Getenv("RONDO_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".rondo"
	}
	return filepath.Join(home, ".rondo")
}

func BinDir() string { return filepath.Join(Home(), "bin") }

func CacheDir() string { return filepath.Join(Home(), "cache") }

// EnvDir holds an environment's rondo.yaml and state; the default environment is Home.
func EnvDir(env string) string {
	if env == "" {
		return Home()
	}
	return filepath.Join(Home(), "envs", env)
}

// StateDir is an environment's state directory, outside CacheDir() which bundle.Resolve wipes.
func StateDir(env string) string { return filepath.Join(EnvDir(env), "state") }

func CurrentEnvFile() string { return filepath.Join(Home(), "current-env") }

func ExpandHome(p string) string {
	if p == "~" || len(p) >= 2 && p[:2] == "~/" {
		if home, err := os.UserHomeDir(); err == nil {
			if p == "~" {
				return home
			}
			return filepath.Join(home, p[2:])
		}
	}
	return p
}
