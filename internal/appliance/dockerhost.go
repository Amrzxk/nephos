package appliance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// dockerContextHost resolves the endpoint of the active Docker CLI context,
// mirroring what `docker` itself does, so Nephos reaches the same daemon the
// learner's `docker ps` does. That matters on Colima, OrbStack, Rancher
// Desktop and Docker Desktop for Linux, which select their socket through a
// context rather than DOCKER_HOST. It returns "" when DOCKER_HOST is set or
// the default context is active, leaving the client's own defaults (the
// Unix socket, or the npipe on Windows) in charge.
func dockerContextHost(getenv func(string) string, home string) (string, error) {
	if getenv("DOCKER_HOST") != "" {
		return "", nil
	}
	configDir := getenv("DOCKER_CONFIG")
	if configDir == "" {
		if home == "" {
			return "", nil
		}
		configDir = filepath.Join(home, ".docker")
	}
	name := getenv("DOCKER_CONTEXT")
	if name == "" {
		data, err := os.ReadFile(filepath.Join(configDir, "config.json"))
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("read Docker CLI config: %w", err)
		}
		var config struct {
			CurrentContext string `json:"currentContext"`
		}
		if err := json.Unmarshal(data, &config); err != nil {
			return "", fmt.Errorf("parse Docker CLI config: %w", err)
		}
		name = config.CurrentContext
	}
	if name == "" || name == "default" {
		return "", nil
	}
	digest := sha256.Sum256([]byte(name))
	metaPath := filepath.Join(configDir, "contexts", "meta", hex.EncodeToString(digest[:]), "meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return "", fmt.Errorf("read Docker context %q: %w", name, err)
	}
	var meta struct {
		Endpoints struct {
			Docker struct {
				Host string `json:"Host"`
			} `json:"docker"`
		} `json:"Endpoints"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return "", fmt.Errorf("parse Docker context %q: %w", name, err)
	}
	if meta.Endpoints.Docker.Host == "" {
		return "", fmt.Errorf("docker context %q has no Docker endpoint", name)
	}
	return meta.Endpoints.Docker.Host, nil
}
