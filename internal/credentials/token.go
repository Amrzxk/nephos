// Package credentials owns the appliance's persistent API credential.
package credentials

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const tokenBytes = 32

// LoadOrCreate returns a volume-backed token, creating it with restrictive
// permissions on first boot. Existing malformed or exposed tokens fail closed.
func LoadOrCreate(path string) (string, error) {
	if token, err := load(path); err == nil {
		return token, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("creating API secret directory: %w", err)
	}
	var raw [tokenBytes]byte
	if _, err := io.ReadFull(rand.Reader, raw[:]); err != nil {
		return "", fmt.Errorf("generating API token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return load(path)
	}
	if err != nil {
		return "", fmt.Errorf("creating API token: %w", err)
	}
	if _, err := f.WriteString(token); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("writing API token: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("closing API token: %w", err)
	}
	return token, nil
}

func load(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("checking API token: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return "", fmt.Errorf("API token file must be regular with mode 0600")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading API token: %w", err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(string(data))
	if err != nil || len(raw) != tokenBytes {
		return "", fmt.Errorf("API token file is malformed")
	}
	return string(data), nil
}
