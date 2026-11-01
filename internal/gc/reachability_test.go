package gc

import (
	"os"
	"testing"
	"time"

	"github.com/zzjha-cn/codex-rewind/internal/display"
	"github.com/zzjha-cn/codex-rewind/internal/journal"
	"github.com/zzjha-cn/codex-rewind/internal/snapshot"
	"github.com/zzjha-cn/codex-rewind/internal/store"
	"github.com/zzjha-cn/codex-rewind/internal/testutil"
	"github.com/zzjha-cn/codex-rewind/internal/workspace"
)

type fixture struct {
	l   *store.Layout
	cas *snapshot.CAS
	sid string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := testutil.TempRepo(t)
	l := store.New(root)
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	sid := "sess-gc"
	if _, err := l.EnsureSession(sid); err != nil {
		t.Fatal(err)
	}
	sess := store.NewSession(sid, sid, "cli", root, root, time.Now())
	if err := store.SaveSession(l, sess); err != nil {
		t.Fatal(err)
	}
	return &fixture{l: l, cas: snapshot.NewCAS(l), sid: sid}
}

func (f *fixture) state(t *testing.T, id string, files map[string]string) *snapshot.State {
	t.Helper()
	st := &snapshot.State{SchemaVersion: snapshot.StateSchemaVersion, StateID: id, CreatedAt: time.Now()}
	for path, content := range files {
		oid, err := f.cas.Put([]byte(content))
		if err != nil {
			t.Fatal(err)
		}
		st.Files = append(st.Files, snapshot.FileEntry{
			Path: path, Exists: true, OID: oid, Mode: 0644, Size: int64(len(content)),
		})
	}
	if err := snapshot.SaveState(f.l, st); err != nil {
		t.Fatal(err)
	}
	return st
}

func (f *fixture) queue(t *testing.T, turns ...store.QueueTurn) {
	t.Helper()
	q := store.NewQueue(f.sid, 10)
	q.Turns = turns
	for _, turn := range turns {
		if turn.Seq >= q.NextSeq {
			q.NextSeq = turn.Seq + 1
		}
	}
	if err := store.SaveQueue(f.l, q); err != nil {
		t.Fatal(err)
	}
}

func qt(seq int64, id, baseline, final string) store.QueueTurn {
	return store.QueueTurn{
		TurnID: id, Seq: seq, ShortID: store.ShortID(id), Status: store.TurnCompleted,
		StartedAt: time.Now(), BaselineStateID: baseline, FinalStateID: final,
		Display: display.Display{},
	}
}

func TestCollectKeepsSharedObjectsAndReferencedStates(t *testing.T) {
	f := newFixture(t)
	s1b := f.state(t, "state-s1b", map[string]string{"a": "A1", "shared": "S"})
	s1f := f.state(t, "state-s1f", map[string]string{"a": "A2", "shared": "S"})
	s2f := f.state(t, "state-s2f", map[string]string{"a": "A3", "shared": "S"})
	orphan := f.state(t, "state-orphan", map[string]string{"orphan": "O"})
	safety := f.state(t, "state-safety", map[string]string{"safety": "Q"})
	f.queue(t, qt(1, "turn-1", s1b.StateID, s1f.StateID), qt(2, "turn-2", s1f.StateID, s2f.StateID))
	op := store.NewRewindOp("rewind-1", f.sid, f.sid, "turn-2", store.ModeBefore, s2f.StateID, time.Now())
	op.Phase = journal.ThreadForked
	op.SafetyStateID = &safety.StateID
	if err := store.SaveRewindOp(f.l, op); err != nil {
		t.Fatal(err)
	}
	orphanObj, _ := f.cas.Put([]byte("O"))
	sharedObj, _ := f.cas.Put([]byte("S"))

	dry, err := Collect(f.l, Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(dry.UnreachableStates) != 1 || dry.UnreachableStates[0] != orphan.StateID {
		t.Fatalf("unreachable states=%v", dry.UnreachableStates)
	}
	if len(dry.UnreachableObjects) != 1 || dry.UnreachableObjects[0] != orphanObj {
		t.Fatalf("unreachable objects=%v", dry.UnreachableObjects)
	}
	for _, oid := range dry.UnreachableObjects {
		if oid == sharedObj {
			t.Fatal("shared object marked unreachable")
		}
	}
	if _, err := os.Stat(objectPath(f.l, orphanObj)); err != nil {
		t.Fatal("dry-run deleted data")
	}

	res, err := Collect(f.l, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.DeletedStates) != 1 || res.DeletedStates[0] != orphan.StateID {
		t.Fatalf("deleted states=%v", res.DeletedStates)
	}
	if len(res.DeletedObjects) != 1 || res.DeletedObjects[0] != orphanObj {
		t.Fatalf("deleted objects=%v", res.DeletedObjects)
	}
	if _, err := os.Stat(objectPath(f.l, sharedObj)); err != nil {
		t.Fatal("shared object was deleted")
	}
	if _, err := snapshot.LoadState(f.l, safety.StateID); err != nil {
		t.Fatalf("incomplete op safety state deleted: %v", err)
	}

	// Dropping turn 1 makes its baseline unreachable, but the state shared with
	// turn 2 and the incomplete op must survive.
	f.queue(t, qt(2, "turn-2", s1f.StateID, s2f.StateID))
	if _, err := Collect(f.l, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statePathFor(t, f.l, s1b.StateID)); !os.IsNotExist(err) {
		t.Fatalf("unreachable baseline survived: %v", err)
	}
	if _, err := os.Stat(statePathFor(t, f.l, s1f.StateID)); err != nil {
		t.Fatal("state referenced as turn 2 baseline was deleted")
	}
	if _, err := os.Stat(objectPath(f.l, sharedObj)); err != nil {
		t.Fatal("shared object was deleted after trimming")
	}
}

func TestCollectReportsMissingObjects(t *testing.T) {
	f := newFixture(t)
	st := f.state(t, "state-1", map[string]string{"a": "present"})
	f.queue(t, qt(1, "turn-1", st.StateID, st.StateID))
	oid := st.Files[0].OID
	path := objectPath(f.l, oid)
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	res, err := Collect(f.l, Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.MissingObjects) != 1 || res.MissingObjects[0] != oid {
		t.Fatalf("missing=%v", res.MissingObjects)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("errors=%v", res.Errors)
	}
}

func TestCollectExcludesSessionsForCleanPlan(t *testing.T) {
	f := newFixture(t)
	st := f.state(t, "state-1", map[string]string{"a": "present"})
	f.queue(t, qt(1, "turn-1", st.StateID, st.StateID))
	res, err := Collect(f.l, Options{DryRun: true, ExcludeSessions: map[string]struct{}{f.sid: struct{}{}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.UnreachableStates) != 1 || res.UnreachableStates[0] != st.StateID {
		t.Fatalf("excluded session state still reachable: %v", res.UnreachableStates)
	}
}

func TestObjectPathRejectsInvalidOID(t *testing.T) {
	l := store.New(t.TempDir())
	if got := objectPath(l, "not-an-oid"); got != "" {
		t.Fatalf("got %q", got)
	}
	if _, err := workspace.ParseOID("sha256:zz"); err == nil {
		t.Fatal("invalid oid accepted")
	}
}

func statePathFor(t *testing.T, l *store.Layout, id string) string {
	t.Helper()
	path, err := snapshot.StatePath(l, id)
	if err != nil {
		t.Fatal(err)
	}
	return path
}
