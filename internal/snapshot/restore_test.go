package snapshot

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zzjha-cn/codex-rewind/internal/testutil"
)

func TestApplyFullRestoreAndVerify(t *testing.T) {
	l, cas, root := newSnapshotLayout(t)
	testutil.Write(t, root, "data.bin", "a\x00b")
	testutil.Write(t, root, "script.sh", "#!/bin/sh\necho hi\n")
	if err := chmod(root, "script.sh", 0755); err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, root, "dir/file.txt", "nested\n")
	if err := os.Symlink("app.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	target, err := Capture(l, cas, CaptureOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Mutate the worktree in ways a discarded turn could have done.
	testutil.Write(t, root, "app.txt", "changed\n")
	if err := removeFile(root, "data.bin"); err != nil {
		t.Fatal(err)
	}
	if err := chmod(root, "script.sh", 0600); err != nil {
		t.Fatal(err)
	}
	if err := removeFile(root, "link"); err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, root, "link", "now a regular file")
	if err := removeFile(root, "dir/file.txt"); err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, root, "extra.txt", "introduced\n")
	current, err := Capture(l, cas, CaptureOptions{Previous: target})
	if err != nil {
		t.Fatal(err)
	}

	scope := append(target.Paths(), "extra.txt")
	latest := map[string]*FileEntry{}
	for _, p := range scope {
		if e := current.Entry(p); e != nil {
			latest[p] = e
		} else {
			latest[p] = &FileEntry{Path: p, Exists: false}
		}
	}
	plan := BuildPlan(target, current, scope, latest)
	if plan.HasConflicts() {
		t.Fatalf("unexpected conflicts: %+v", plan.Conflicts)
	}
	if plan.Writes() != 5 || plan.Deletes() != 1 {
		t.Fatalf("plan writes=%d deletes=%d actions=%+v", plan.Writes(), plan.Deletes(), plan.Actions)
	}
	applied, err := Apply(root, cas, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 6 {
		t.Fatalf("applied=%v", applied)
	}

	if got := testutil.Read(t, root, "app.txt"); got != "base\n" {
		t.Fatalf("app=%q", got)
	}
	if got := testutil.Read(t, root, "data.bin"); got != "a\x00b" {
		t.Fatalf("data=%q", got)
	}
	info, err := os.Stat(filepath.Join(root, "script.sh"))
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("script mode %v %v", info, err)
	}
	if target, err := os.Readlink(filepath.Join(root, "link")); err != nil || target != "app.txt" {
		t.Fatalf("symlink=%q %v", target, err)
	}
	if got := testutil.Read(t, root, "dir/file.txt"); got != "nested\n" {
		t.Fatalf("nested=%q", got)
	}
	if testutil.Exists(root, "extra.txt") {
		t.Fatal("extra.txt was not deleted")
	}
	conflicts, err := VerifyPaths(l, cas, target, scope)
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("verify: %+v %v", conflicts, err)
	}

	testutil.Write(t, root, "app.txt", "manual tamper\n")
	conflicts, err = VerifyPaths(l, cas, target, scope)
	if err != nil || len(conflicts) != 1 || conflicts[0].Path != "app.txt" {
		t.Fatalf("tamper verify: %+v %v", conflicts, err)
	}
}

func TestRestoreEntryRejectsSymlinkedParent(t *testing.T) {
	_, cas, root := newSnapshotLayout(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	oid, err := cas.Put([]byte("payload\n"))
	if err != nil {
		t.Fatal(err)
	}
	e := &FileEntry{Path: "linked/passwd", Exists: true, OID: oid, Mode: 0644}
	if err := RestoreEntry(root, cas, e); err == nil {
		t.Fatal("restore through symlinked parent succeeded")
	}
	if _, err := os.Stat(filepath.Join(outside, "passwd")); !os.IsNotExist(err) {
		t.Fatal("wrote outside the workspace")
	}
}

func TestRestoreEntryMissingObjectFails(t *testing.T) {
	_, cas, root := newSnapshotLayout(t)
	oid, err := cas.Put([]byte("payload\n"))
	if err != nil {
		t.Fatal(err)
	}
	path, _ := cas.Path(oid)
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	e := &FileEntry{Path: "new.txt", Exists: true, OID: oid, Mode: 0644}
	if err := RestoreEntry(root, cas, e); err == nil {
		t.Fatal("restore with a missing object succeeded")
	}
}

func TestDeleteEntryCleansOnlyEmptyParents(t *testing.T) {
	_, _, root := newSnapshotLayout(t)
	testutil.Write(t, root, "a/b/file.txt", "x")
	testutil.Write(t, root, "a/keep.txt", "keep")
	if err := DeleteEntry(root, "a/b/file.txt"); err != nil {
		t.Fatal(err)
	}
	if testutil.Exists(root, "a/b") {
		t.Fatal("empty b directory remains")
	}
	if !testutil.Exists(root, "a/keep.txt") {
		t.Fatal("sibling file deleted")
	}
	if err := DeleteEntry(root, "a/keep.txt"); err != nil {
		t.Fatal(err)
	}
	if testutil.Exists(root, "a") {
		t.Fatal("empty a directory remains")
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := DeleteEntry(root, "dir"); err == nil {
		t.Fatal("delete of a directory succeeded")
	}
}

func TestRestorePathsRollbackDeletesNewFiles(t *testing.T) {
	l, cas, root := newSnapshotLayout(t)
	target, err := Capture(l, cas, CaptureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, root, "introduced.txt", "new")
	applied, err := RestorePaths(root, cas, target, []string{"introduced.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || testutil.Exists(root, "introduced.txt") {
		t.Fatalf("rollback failed: %v", applied)
	}
}

func TestLoadStateRejectsWrongSchema(t *testing.T) {
	l, _, _ := newSnapshotLayout(t)
	path, err := StatePath(l, "state-bad")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schemaVersion":99,"stateId":"state-bad"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(l, "state-bad"); err == nil {
		t.Fatal("wrong schema accepted")
	}
}
