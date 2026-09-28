package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Amrzxk/nephos/internal/store"
)

func validateIdempotencyKey(key string) error {
	if key == "" {
		return nil
	}
	if len(key) > 255 || !utf8.ValidString(key) {
		return invalidParameter("Idempotency-Key must be valid UTF-8 and at most 255 bytes")
	}
	for _, r := range key {
		if unicode.IsControl(r) {
			return invalidParameter("Idempotency-Key must not contain control characters")
		}
	}
	return nil
}

func hashPayload(fields ...string) (string, error) {
	canonicalJSON, err := json.Marshal(fields)
	if err != nil {
		return "", fmt.Errorf("encode canonical create request: %w", err)
	}
	sum := sha256.Sum256(canonicalJSON)
	return hex.EncodeToString(sum[:]), nil
}

func loadReplay[T any](ctx context.Context, tx *store.Tx, operation, key, hash string, now time.Time) (result T, found bool, err error) {
	var zero T
	if key == "" {
		return zero, false, nil
	}
	record, err := tx.GetIdempotency(ctx, "default", operation, key)
	if errors.Is(err, sql.ErrNoRows) {
		return zero, false, nil
	}
	if err != nil {
		return zero, false, err
	}
	if record.ExpiresAt <= now.Unix() {
		return zero, false, nil
	}
	if record.PayloadHash != hash {
		return zero, false, &Error{
			Code: "IdempotentParameterMismatch", Message: "Idempotency-Key was already used for different parameters",
			Status: 409,
		}
	}
	if err := json.Unmarshal([]byte(record.ResponseJSON), &result); err != nil {
		return zero, false, fmt.Errorf("decode original %s result: %w", operation, err)
	}
	return result, true, nil
}

func saveReplay[T any](ctx context.Context, tx *store.Tx, operation, key, hash, resourceID string, result T, now time.Time) error {
	if key == "" {
		return nil
	}
	snapshot, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode original %s result: %w", operation, err)
	}
	return tx.PutIdempotency(ctx, store.Idempotency{
		WorkspaceID: "default", Operation: operation, Key: key, PayloadHash: hash,
		ResourceID: resourceID, ResponseJSON: string(snapshot),
		CreatedAt: now.Unix(), ExpiresAt: now.Add(24 * time.Hour).Unix(),
	})
}
