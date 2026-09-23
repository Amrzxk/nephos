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
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("credential directory must be private and not a symlink")
	}
	f, err := os.CreateTemp(dir, ".credentials-*")
	if err != nil {
		return fmt.Errorf("create temporary credential: %w", err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if err := f.Chmod(0o600); err != nil {
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
	raw, err := base64.RawURLEncoding.DecodeString(string(token))
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != string(token) {
		return nil, fmt.Errorf("credential archive token is malformed")
	}
	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("credential archive must contain exactly one regular token file")
	}
	return token, nil
}
