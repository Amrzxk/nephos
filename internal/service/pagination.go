package service

import (
	"encoding/base64"
	"strings"
)

func pageSize(limit int) (int, error) {
	if limit == 0 {
		return 50, nil
	}
	if limit < 1 || limit > 100 {
		return 0, invalidParameter("limit must be from 1 to 100")
	}
	return limit, nil
}

func encodePageToken(kind, lastID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(kind + ":" + lastID))
}

func decodePageToken(kind, token string) (string, error) {
	if token == "" {
		return "", nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", invalidParameter("invalid page_token")
	}
	value := string(decoded)
	prefix := kind + ":"
	if !strings.HasPrefix(value, prefix) {
		return "", invalidParameter("page_token belongs to a different resource type")
	}
	id := strings.TrimPrefix(value, prefix)
	if !validPageID(kind, id) {
		return "", invalidParameter("invalid page_token")
	}
	return id, nil
}

func validPageID(kind, id string) bool {
	prefix := kind + "-"
	if !strings.HasPrefix(id, prefix) || len(id) != len(prefix)+17 {
		return false
	}
	for _, ch := range id[len(prefix):] {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}
