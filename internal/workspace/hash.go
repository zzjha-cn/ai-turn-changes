package workspace

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const OIDPrefix = "sha256:"

// HashBytes returns the lowercase hex SHA-256 of data.
func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// OID formats a raw hex digest as a manifest object id.
func OID(hexDigest string) string { return OIDPrefix + hexDigest }

// HashOID hashes data and returns the manifest oid.
func HashOID(data []byte) string { return OID(HashBytes(data)) }

// ParseOID validates an oid and returns its hex digest.
func ParseOID(oid string) (string, error) {
	if !strings.HasPrefix(oid, OIDPrefix) {
		return "", fmt.Errorf("invalid oid %q", oid)
	}
	digest := strings.TrimPrefix(oid, OIDPrefix)
	if len(digest) != 64 {
		return "", fmt.Errorf("invalid oid length %q", oid)
	}
	for _, r := range digest {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return "", fmt.Errorf("invalid oid %q", oid)
		}
	}
	return digest, nil
}

// IsBinary applies Git's heuristic: a NUL byte in the first 8000 bytes.
func IsBinary(data []byte) bool {
	n := len(data)
	if n > 8000 {
		n = 8000
	}
	return bytes.IndexByte(data[:n], 0) >= 0
}

// CountLines counts logical lines, treating a missing final newline as a line.
func CountLines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	n := bytes.Count(data, []byte("\n"))
	if data[len(data)-1] != '\n' {
		n++
	}
	return n
}
