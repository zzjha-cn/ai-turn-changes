package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzjha-cn/codex-rewind/internal/testutil"
)

func TestValidRelPath(t *testing.T) {
	valid := []string{"a.txt", "dir/b.txt", "a b/c-d_e.txt"}
	for _, p := range valid {
		if !ValidRelPath(p) {
			t.Errorf("ValidRelPath(%q)=false, want true", p)
		}
	}
	invalid := []string{"", ".", "/abs", "../up", "a/../b", "a//b", "a/./b", `.git/config`, ".codex-rewind/x", `a\b`, "a/\x00b"}
	for _, p := range invalid {
		if ValidRelPath(p) {
			t.Errorf("ValidRelPath(%q)=true, want false", p)
		}
	}
}

func TestAbsWithinRejectsEscape(t *testing.T) {
	root := t.TempDir()
	got, err := AbsWithin(root, "dir/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "dir", "file.txt"); got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	if _, err := AbsWithin(root, "../outside"); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
	if _, err := AbsWithin(root, "/etc/passwd"); err == nil {
		t.Fatal("expected absolute path to be rejected")
	}
}

func TestEnsureSafeParentRejectsSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSafeParent(root, "linked/passwd"); err == nil {
		t.Fatal("expected symlinked parent to be rejected")
	}
	if _, err := os.Stat(filepath.Join(outside, "passwd")); !os.IsNotExist(err) {
		t.Fatal("file escaped through symlinked parent")
	}
}

func TestEnsureSafeParentCreatesMissingDirs(t *testing.T) {
	root := t.TempDir()
	if err := EnsureSafeParent(root, "a/b/c.txt"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "a", "b"))
	if err != nil || !info.IsDir() {
		t.Fatalf("nested dirs not created: %v", err)
	}
}

func TestResolvePrefersGitRoot(t *testing.T) {
	root := testutil.TempRepo(t)
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(sub, "")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(root)
	if resolved, _ := filepath.EvalSymlinks(got); resolved != want {
		t.Fatalf("Resolve(%s)=%s, want %s", sub, got, want)
	}
}

func TestResolveExplicitDataDir(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, DataDirName)
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve("", data)
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Fatalf("got %s want %s", got, root)
	}
}

func TestListFilesIncludesTrackedAndUntrackedNotIgnored(t *testing.T) {
	root := testutil.TempRepo(t)
	testutil.Write(t, root, "untracked.txt", "u\n")
	testutil.Write(t, root, "ignored.log", "i\n")
	testutil.Write(t, root, ".gitignore", "*.log\n")
	if err := os.Mkdir(filepath.Join(root, DataDirName), 0700); err != nil {
		t.Fatal(err)
	}
	files, err := ListFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"app.txt": true, "untracked.txt": true, ".gitignore": true}
	got := map[string]bool{}
	for _, f := range files {
		got[f] = true
	}
	for f := range want {
		if !got[f] {
			t.Errorf("missing %s in %v", f, files)
		}
	}
	for f := range got {
		if f == "ignored.log" || f == DataDirName || strings.HasPrefix(f, DataDirName+"/") {
			t.Errorf("unexpected %s in listing", f)
		}
	}
}

func TestCheckDataIgnored(t *testing.T) {
	root := testutil.TempRepo(t)
	if err := os.Mkdir(filepath.Join(root, DataDirName), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, DataDirName, ".gitignore"), []byte("*\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, DataDirName, "state.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	lines, err := CheckDataIgnored(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 0 {
		t.Fatalf("data dir appears in git status: %v", lines)
	}
}

func TestGitRootAndIsGitRepo(t *testing.T) {
	root := testutil.TempRepo(t)
	want, _ := filepath.EvalSymlinks(root)
	got, ok := GitRoot(root)
	if resolved, _ := filepath.EvalSymlinks(got); !ok || resolved != want {
		t.Fatalf("GitRoot=%q,%v want %q,true", got, ok, want)
	}
	if IsGitRepo(t.TempDir()) {
		t.Fatal("plain temp dir should not be a git repo")
	}
}
