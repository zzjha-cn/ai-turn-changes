package snapshot

import (
	"strings"
	"testing"

	"github.com/zzjha-cn/codex-rewind/internal/workspace"
)

func TestLineDiff(t *testing.T) {
	ins, del := LineDiff([]byte("a\nb\nc\n"), []byte("a\nc\nd\n"))
	if ins != 1 || del != 1 {
		t.Fatalf("insertions=%d deletions=%d want 1/1", ins, del)
	}
	ins, del = LineDiff(nil, []byte("one\ntwo\n"))
	if ins != 2 || del != 0 {
		t.Fatalf("add: %d/%d", ins, del)
	}
	ins, del = LineDiff([]byte("one\ntwo\n"), nil)
	if ins != 0 || del != 2 {
		t.Fatalf("delete: %d/%d", ins, del)
	}
	ins, del = LineDiff([]byte("same\n"), []byte("same\n"))
	if ins != 0 || del != 0 {
		t.Fatalf("same: %d/%d", ins, del)
	}
	big := strings.Repeat("line\n", maxExactDiffLines+1)
	ins, del = LineDiff([]byte(big), []byte("small\n"))
	if ins != 1 || del != maxExactDiffLines+1 {
		t.Fatalf("large fallback: %d/%d", ins, del)
	}
}

func TestDiffStatusesAndBinary(t *testing.T) {
	l, cas, root := newSnapshotLayout(t)
	oldState, err := Capture(l, cas, CaptureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFile(root, "app.txt", "changed\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(root, "added.txt", "new\nline\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(root, "bin.dat", "a\x00b"); err != nil {
		t.Fatal(err)
	}
	newState, err := Capture(l, cas, CaptureOptions{Previous: oldState})
	if err != nil {
		t.Fatal(err)
	}
	changes := Diff(oldState, newState, cas)
	seen := map[string]string{}
	for _, c := range changes {
		seen[c.Path] = c.Status
	}
	if seen["app.txt"] != "modified" || seen["added.txt"] != "added" || seen["bin.dat"] != "added" {
		t.Fatalf("changes=%+v", changes)
	}
	for _, c := range changes {
		if c.Path == "bin.dat" && !c.IsBinary {
			t.Fatal("binary file not marked")
		}
		if c.Path == "added.txt" && (c.Insertions != 2 || c.Deletions != 0) {
			t.Fatalf("added stats: +%d -%d", c.Insertions, c.Deletions)
		}
	}
}

func TestDiffDeletedAndModeOnly(t *testing.T) {
	l, cas, root := newSnapshotLayout(t)
	if err := chmod(root, "app.txt", 0755); err != nil {
		t.Fatal(err)
	}
	oldState, err := Capture(l, cas, CaptureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := chmod(root, "app.txt", 0644); err != nil {
		t.Fatal(err)
	}
	if err := removeFile(root, "app.txt"); err != nil {
		t.Fatal(err)
	}
	newState, err := Capture(l, cas, CaptureOptions{Previous: oldState})
	if err != nil {
		t.Fatal(err)
	}
	changes := Diff(oldState, newState, cas)
	if len(changes) != 1 || changes[0].Status != "deleted" || changes[0].Deletions != 1 {
		t.Fatalf("changes=%+v", changes)
	}
	if changes[0].IsBinary {
		t.Fatal("deleted text marked binary")
	}
}

func TestDiffIgnoresUnchanged(t *testing.T) {
	l, cas, _ := newSnapshotLayout(t)
	st, err := Capture(l, cas, CaptureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if changes := Diff(st, st, cas); len(changes) != 0 {
		t.Fatalf("unexpected changes: %+v", changes)
	}
	var _ = workspace.OID("")
}
