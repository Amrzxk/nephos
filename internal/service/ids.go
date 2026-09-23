package service

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// NewID returns an AWS-style identifier with 17 lowercase hexadecimal
// characters. Supplying a reader permits deterministic tests; nil uses the
// cryptographic operating-system source.
func NewID(prefix string, random io.Reader) (string, error) {
	if !validIDPrefix(prefix) {
		return "", invalidParameter("invalid resource ID prefix")
	}
	if random == nil {
		random = rand.Reader
	}
	var entropy [9]byte
	if _, err := io.ReadFull(random, entropy[:]); err != nil {
		return "", fmt.Errorf("generate %s ID: %w", prefix, err)
	}
	return prefix + hex.EncodeToString(entropy[:])[:17], nil
}

func validIDPrefix(prefix string) bool {
	if len(prefix) < 2 || !strings.HasSuffix(prefix, "-") {
		return false
	}
	for i := 0; i < len(prefix)-1; i++ {
		ch := prefix[i]
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' {
			return false
		}
	}
	return prefix[0] >= 'a' && prefix[0] <= 'z'
}
