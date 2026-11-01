// Package testutil provides temporary Git repositories for tests.
package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TempRepo creates a temporary Git repository with one committed file.
func TempRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	Git(t, root, "init", "-q")
	Git(t, root, "config", "user.email", "test@example.invalid")
	Git(t, root, "config", "user.name", "cxr test")
	Write(t, root, "app.txt", "base\n")
	Git(t, root, "add", "app.txt")
	Git(t, root, "commit", "-qm", "init")
	return root
}

// Git runs git in dir and fails the test on error.
func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// Write writes a file below root.
func Write(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// Read reads a file below root.
func Read(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Exists reports whether a path below root exists.
func Exists(root, rel string) bool {
	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

// FixedClock returns a clock advancing one second per call.
func FixedClock(start time.Time) func() time.Time {
	n := 0
	return func() time.Time {
		n++
		return start.Add(time.Duration(n) * time.Second)
	}
}
