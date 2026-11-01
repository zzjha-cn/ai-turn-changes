package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zzjha-cn/codex-rewind/internal/store"
	"github.com/zzjha-cn/codex-rewind/internal/testutil"
	"github.com/zzjha-cn/codex-rewind/internal/workspace"
)

func newSnapshotLayout(t *testing.T) (*store.Layout, *CAS, string) {
	t.Helper()
	root := testutil.TempRepo(t)
	l := store.New(root)
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	return l, NewCAS(l), root
}

func TestCASPutGetReuseAndCorruption(t *testing.T) {
	_, cas, _ := newSnapshotLayout(t)
	oid, err := cas.Put([]byte("hello world\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(oid, workspace.OIDPrefix) {
		t.Fatalf("oid=%s", oid)
	}
	path, err := cas.Path(oid)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0444 {
		t.Fatalf("object stat: %v %v", info, err)
	}
	same, err := cas.Put([]byte("hello world\n"))
	if err != nil || same != oid {
		t.Fatalf("reuse: %s %v", same, err)
	}
	data, err := cas.Get(oid)
	if err != nil || string(data) != "hello world\n" {
		t.Fatalf("get: %q %v", data, err)
	}
	if !cas.Equal(oid, same) {
		t.Fatal("Equal failed")
	}
	if err := cas.VerifyAll([]string{oid, oid, ""}); err != nil {
		t.Fatal(err)
	}
	// Corruption is detected and Put replaces the bad object.
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("tampered!!!!"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := cas.Get(oid); err == nil {
		t.Fatal("corrupt object was accepted")
	}
	if _, err := cas.Put([]byte("hello world\n")); err != nil {
		t.Fatal(err)
	}
	if data, err := cas.Get(oid); err != nil || string(data) != "hello world\n" {
		t.Fatalf("object not repaired: %q %v", data, err)
	}
	if _, err := cas.Path("not-an-oid"); err == nil {
		t.Fatal("invalid oid accepted")
	}
}

func TestCaptureRecordsExistingUntrackedBaseline(t *testing.T) {
	l, cas, root := newSnapshotLayout(t)
	testutil.Write(t, root, "untracked.txt", "pre-existing content\n")
	testutil.Write(t, root, "ignored.log", "ignored\n")
	testutil.Write(t, root, ".gitignore", "*.log\n")
	st, err := Capture(l, cas, CaptureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	e := st.Entry("untracked.txt")
	if e == nil || !e.Exists {
		t.Fatalf("untracked baseline missing: %+v", st.Files)
	}
	data, err := cas.Get(e.OID)
	if err != nil || string(data) != "pre-existing content\n" {
		t.Fatalf("object content: %q %v", data, err)
	}
	if st.Entry("ignored.log") != nil {
		t.Fatal("ignored file was captured")
	}
	if e.Mode != 0644 {
		t.Fatalf("mode=%o", e.Mode)
	}
}

func TestCaptureSymlinkStoresTargetNotContent(t *testing.T) {
	l, cas, root := newSnapshotLayout(t)
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP SECRET"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	st, err := Capture(l, cas, CaptureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	e := st.Entry("link")
	if e == nil || !e.Exists || !e.IsSymlink || e.LinkTarget == nil || *e.LinkTarget != secret {
		t.Fatalf("symlink entry: %+v", e)
	}
	data, err := cas.Get(e.OID)
	if err != nil || string(data) != "symlink:"+secret {
		t.Fatalf("stored symlink bytes: %q %v", data, err)
	}
}

func TestCaptureDoesNotTraverseSymlinkDirectory(t *testing.T) {
	l, cas, root := newSnapshotLayout(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("hidden"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked-dir")); err != nil {
		t.Fatal(err)
	}
	st, err := Capture(l, cas, CaptureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range st.Files {
		if strings.HasPrefix(f.Path, "linked-dir/") {
			t.Fatalf("traversed symlink directory: %s", f.Path)
		}
	}
}

func TestCaptureReusesUnchangedObject(t *testing.T) {
	l, cas, root := newSnapshotLayout(t)
	first, err := Capture(l, cas, CaptureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	firstEntry := first.Entry("app.txt")
	second, err := Capture(l, cas, CaptureOptions{Previous: first})
	if err != nil {
		t.Fatal(err)
	}
	secondEntry := second.Entry("app.txt")
	if secondEntry == nil || secondEntry.OID != firstEntry.OID {
		t.Fatalf("object not reused: %+v vs %+v", firstEntry, secondEntry)
	}
	testutil.Write(t, root, "app.txt", "changed\n")
	third, err := Capture(l, cas, CaptureOptions{Previous: second})
	if err != nil {
		t.Fatal(err)
	}
	thirdEntry := third.Entry("app.txt")
	if thirdEntry == nil || thirdEntry.OID == firstEntry.OID {
		t.Fatal("changed file reused old object")
	}
	if data, err := cas.Get(thirdEntry.OID); err != nil || string(data) != "changed\n" {
		t.Fatalf("changed content: %q %v", data, err)
	}
}

func TestStateSaveLoadRoundTrip(t *testing.T) {
	l, _, _ := newSnapshotLayout(t)
	st := &State{
		SchemaVersion: StateSchemaVersion,
		StateID:       "state-1",
		CreatedAt:     time.Now(),
		Files:         []FileEntry{{Path: "a", Exists: true, OID: "sha256:" + strings.Repeat("a", 64), Mode: 0644}},
	}
	if err := SaveState(l, st); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(l, "state-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.StateID != st.StateID || len(got.Files) != 1 || got.Files[0].Path != "a" {
		t.Fatalf("round trip: %+v", got)
	}
	ids, err := ListStateIDs(l)
	if err != nil || len(ids) != 1 || ids[0] != "state-1" {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	if _, err := LoadState(l, "../escape"); err == nil {
		t.Fatal("invalid state id accepted")
	}
}
