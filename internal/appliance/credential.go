package appliance

import (
	"archive/tar"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const maxTokenBytes = 128

// LocalCredentialStore writes the host-side token to one private file.
type LocalCredentialStore struct {
	Path string
}

// Save verifies the Docker TAR archive before atomically replacing the token.
func (s LocalCredentialStore) Save(ctx context.Context, archive io.Reader) error {
	if s.Path == "" {
		return fmt.Errorf("credential path is empty")
	}
	token, err := extractToken(archive)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create credential directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("check credential directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("credential directory must be a directory, not a symlink")
	}
	if err := checkPrivate(info); err != nil {
		return fmt.Errorf("credential directory must be private: %w", err)
	}
	f, err := os.CreateTemp(dir, ".credentials-*")
	if err != nil {
		return fmt.Errorf("create temporary credential: %w", err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if err := restrictToOwner(f); err != nil {
		_ = f.Close()
		return fmt.Errorf("restrict credential permissions: %w", err)
	}
	if _, err := f.Write(token); err != nil {
		_ = f.Close()
		return fmt.Errorf("write credential: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync credential: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close credential: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), s.Path); err != nil {
		return fmt.Errorf("replace credential: %w", err)
	}
	return nil
}

// LoadCredential reads the host-side API token written by Save. It refuses
// symlinks, non-regular or oversized files, files other users can read, and
// anything that is not a canonical 32-byte token.
func LoadCredential(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("read API credential %s (run nephos up): %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxTokenBytes {
		return "", fmt.Errorf("credential file must be a private regular file")
	}
	if err := checkPrivate(info); err != nil {
		return "", fmt.Errorf("credential file must be a private regular file: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read credential file: %w", err)
	}
	if !validToken(string(data)) {
		return "", fmt.Errorf("credential file is malformed")
	}
	return string(data), nil
}

// validToken accepts only the canonical unpadded base64url form of 32 bytes.
func validToken(token string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(raw) == 32 && base64.RawURLEncoding.EncodeToString(raw) == token
}

func extractToken(archive io.Reader) ([]byte, error) {
	reader := tar.NewReader(io.LimitReader(archive, 1<<20))
	h, err := reader.Next()
	if err != nil {
		return nil, fmt.Errorf("read credential archive: %w", err)
	}
	if h.Name != "api-token" || h.Typeflag != tar.TypeReg || h.Size < 1 || h.Size > maxTokenBytes {
		return nil, fmt.Errorf("credential archive has an unexpected entry")
	}
	token, err := io.ReadAll(io.LimitReader(reader, maxTokenBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read credential token: %w", err)
	}
	if int64(len(token)) != h.Size {
		return nil, fmt.Errorf("credential archive token size mismatch")
	}
	if !validToken(string(token)) {
		return nil, fmt.Errorf("credential archive token is malformed")
	}
	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("credential archive must contain exactly one regular token file")
	}
	return token, nil
}
