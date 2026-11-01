// Package workspace resolves repository roots and enumerates worktree files.
package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DataDirName is duplicated here to avoid importing store from the scanner.
const DataDirName = ".codex-rewind"

// Resolve determines the repository root.
//
// Priority:
//  1. explicit root (a .codex-rewind directory is accepted and its parent is used);
//  2. git rev-parse --show-toplevel from start;
//  3. the nearest ancestor containing .codex-rewind/;
//  4. start (or the current directory when start is empty).
func Resolve(start, explicit string) (string, error) {
	if explicit != "" {
		abs, err := filepath.Abs(explicit)
		if err != nil {
			return "", err
		}
		if filepath.Base(abs) == DataDirName {
			abs = filepath.Dir(abs)
		}
		return abs, nil
	}
	if start == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		start = wd
	}
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	if st, err := os.Stat(abs); err == nil && !st.IsDir() {
		abs = filepath.Dir(abs)
	}
	if root, ok := GitRoot(abs); ok {
		return root, nil
	}
	for dir := abs; ; {
		if st, err := os.Stat(filepath.Join(dir, DataDirName)); err == nil && st.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return abs, nil
}

// ValidRelPath reports whether p is a safe repository-relative slash path.
func ValidRelPath(p string) bool {
	if p == "" || p == "." {
		return false
	}
	if strings.ContainsRune(p, 0) {
		return false
	}
	if strings.Contains(p, `\`) {
		return false
	}
	if strings.HasPrefix(p, "/") {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(p))
	if clean != p {
		return false
	}
	parts := strings.Split(p, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	if parts[0] == ".git" || parts[0] == DataDirName {
		return false
	}
	return true
}

// AbsWithin joins root and rel after validation.
func AbsWithin(root, rel string) (string, error) {
	if !ValidRelPath(rel) {
		return "", fmt.Errorf("invalid workspace path %q", rel)
	}
	abs := filepath.Join(root, filepath.FromSlash(rel))
	check, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	if check == ".." || strings.HasPrefix(check, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes workspace: %q", rel)
	}
	return abs, nil
}

// EnsureSafeParent verifies that every existing parent component of rel is a
// real directory and not a symlink, preventing writes outside the workspace
// through a linked parent.
func EnsureSafeParent(root, rel string) error {
	abs, err := AbsWithin(root, rel)
	if err != nil {
		return err
	}
	parent := filepath.Dir(abs)
	checkRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	var missing []string
	for dir := parent; ; dir = filepath.Dir(dir) {
		if dir == checkRoot || dir == "." || dir == string(filepath.Separator) {
			break
		}
		st, err := os.Lstat(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				missing = append(missing, dir)
				continue
			}
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("parent directory is a symlink: %s", dir)
		}
		if !st.IsDir() {
			return fmt.Errorf("parent is not a directory: %s", dir)
		}
	}
	// Create missing parents only after all existing ancestors were validated.
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0755); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	return nil
}

// CleanEmptyParents removes empty directories from dir up to root.
func CleanEmptyParents(root, dir string) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return
	}
	for absDir != absRoot && strings.HasPrefix(absDir, absRoot+string(filepath.Separator)) {
		if err := os.Remove(absDir); err != nil {
			return
		}
		absDir = filepath.Dir(absDir)
	}
}
