// Package snapshot implements content-addressed storage, workspace manifests
// and physical restore.
package snapshot

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zzjha-cn/codex-rewind/internal/fsutil"
	"github.com/zzjha-cn/codex-rewind/internal/store"
	"github.com/zzjha-cn/codex-rewind/internal/workspace"
)

// CAS stores raw file bytes under objects/<first two hex>/<remaining hex>.
type CAS struct {
	dir string
}

func NewCAS(l *store.Layout) *CAS { return &CAS{dir: l.ObjectsDir()} }

func (c *CAS) Path(oid string) (string, error) {
	digest, err := workspace.ParseOID(oid)
	if err != nil {
		return "", err
	}
	return filepath.Join(c.dir, digest[:2], digest[2:]), nil
}

// Put stores data and returns its oid. It reuses an existing object only after
// verifying its content, so a corrupt object is replaced rather than trusted.
func (c *CAS) Put(data []byte) (string, error) {
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	oid := workspace.OID(digest)
	path := filepath.Join(c.dir, digest[:2], digest[2:])
	if st, err := os.Stat(path); err == nil {
		if st.Size() == int64(len(data)) {
			if existing, rerr := os.ReadFile(path); rerr == nil && sha256.Sum256(existing) == sum {
				return oid, nil
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	if err := fsutil.WriteFileAtomic(path, data, 0444); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0444); err != nil {
		return "", err
	}
	return oid, nil
}

// PutString stores the bytes of s.
func (c *CAS) PutString(s string) (string, error) { return c.Put([]byte(s)) }

// Get reads an object and verifies its SHA-256.
func (c *CAS) Get(oid string) ([]byte, error) {
	path, err := c.Path(oid)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read object %s: %w", oid, err)
	}
	sum := sha256.Sum256(data)
	digest, err := workspace.ParseOID(oid)
	if err != nil {
		return nil, err
	}
	if hex.EncodeToString(sum[:]) != digest {
		return nil, fmt.Errorf("object %s is corrupt", oid)
	}
	return data, nil
}

// Verify checks existence and content hash.
func (c *CAS) Verify(oid string) error {
	_, err := c.Get(oid)
	return err
}

// Has reports whether the object appears present with a plausible path.
func (c *CAS) Has(oid string) bool {
	path, err := c.Path(oid)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// Bytes returns the object bytes without verifying (internal fast path).
func (c *CAS) bytesUnsafe(oid string) ([]byte, error) {
	path, err := c.Path(oid)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// Equal compares two objects by content.
func (c *CAS) Equal(a, b string) bool {
	if a == b {
		return true
	}
	da, err := c.bytesUnsafe(a)
	if err != nil {
		return false
	}
	db, err := c.bytesUnsafe(b)
	if err != nil {
		return false
	}
	return bytes.Equal(da, db)
}

// VerifyAll fails if any object is missing or corrupt.
func (c *CAS) VerifyAll(oids []string) error {
	seen := make(map[string]struct{}, len(oids))
	for _, oid := range oids {
		if oid == "" {
			continue
		}
		if _, ok := seen[oid]; ok {
			continue
		}
		seen[oid] = struct{}{}
		if err := c.Verify(oid); err != nil {
			return err
		}
	}
	return nil
}
