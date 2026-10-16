package main

import (
	"crypto/sha256"
	"encoding/hex"
)

func configVersion(cfg Config) string {
	sum := sha256.Sum256(mustJSON(cfg))
	return `"` + hex.EncodeToString(sum[:]) + `"`
}
