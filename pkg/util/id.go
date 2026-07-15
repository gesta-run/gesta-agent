package util

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
)

func NewID(prefix string) string {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s-%p", prefix, &b)))
		return prefix + "_" + hex.EncodeToString(sum[:12])
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

func HashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func ShortHash(value string) string {
	h := HashString(value)
	if len(h) > 16 {
		return h[:16]
	}
	return h
}
