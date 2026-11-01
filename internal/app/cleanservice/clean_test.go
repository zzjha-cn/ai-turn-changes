package cleanservice

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/zzjha-cn/codex-rewind/internal/display"
	"github.com/zzjha-cn/codex-rewind/internal/journal"
	"github.com/zzjha-cn/codex-rewind/internal/snapshot"
	"github.com/zzjha-cn/codex-rewind/internal/store"
	"github.com/zzjha-cn/codex-rewind/internal/testutil"
)

type cleanFixture struct {
	t     *testing.T
	root  string
	l     *store.Layout
	cas   *snapshot.CAS
	now   time.Time
	sidA  string
	sidB  string
	state map[string]*snapshot.State
	obj   map[string]string
}

func newCleanFixture(t *testing.T) *cleanFixture {
	t.Helper()
	root := testutil.TempRepo(t)
	l := store.New(root)
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	f := &cleanFixture{t: t, root: root, l: l, cas: snapshot.NewCAS(l),
		now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC), sidA: "sess-old", sidB: "sess-new",
		state: map[string]*snapshot.State{}, obj: map[string]string{}}
	f.makeState("state-a-base", map[string]string{"shared": "S", "onlyA": "A-base"})
	f.makeState("state-a-final", map[string]string{"shared": "S", "onlyA": "A-final"})
	f.makeState("state-b-base", map[string]string{"shared": "S", "onlyB": "B-base"})
	f.makeState("state-b-final", map[string]string{"shared": "S", "onlyB": "B-final"})
	f.makeState("state-safe", map[string]string{"safe": "Q"})

	old := f.now.Add(-60 * 24 * time.Hour)
	f.makeSession(f.sidA, old, &old)
	f.makeSession(f.sidB, f.now, nil)
	f.makeQueue(f.sidA, "turn-a1", "state-a-base", "state-a-final")
	f.makeQueue(f.sidB, "turn-b1", "state-b-base", "state-b-final")

	op := store.NewRewindOp("rewind-old", f.sidA, f.sidA, "turn-a1", store.ModeBefore, "state-a-final", old)
	op.Phase = journal.Completed
	op.SafetyStateID = strPtr("state-a-base")
	op.UpdatedAt = old
	if err := store.SaveRewindOp(l, op); err != nil {
		t.Fatal(err)
	}
	op2 := store.NewRewindOp("rewind-open", f.sidB, f.sidB, "turn-b1", store.ModeBefore, "state-b-final", old)
	op2.Phase = journal.ThreadForked
	op2.SafetyStateID = strPtr("state-safe")
	op2.UpdatedAt = old
	if err := store.SaveRewindOp(l, op2); err != nil {
		t.Fatal(err)
	}
	return f
}

func strPtr(s string) *string { return &s }

func (f *cleanFixture) makeState(id string, files map[string]string) {
	f.t.Helper()
	st := &snapshot.State{SchemaVersion: snapshot.StateSchemaVersion, StateID: id, CreatedAt: f.now}
	for path, content := range files {
		oid, err := f.cas.Put([]byte(content))
		if err != nil {
			f.t.Fatal(err)
		}
		st.Files = append(st.Files, snapshot.FileEntry{Path: path, Exists: true, OID: oid, Mode: 0644, Size: int64(len(content))})
		f.obj[content] = oid
	}
	if err := snapshot.SaveState(f.l, st); err != nil {
		f.t.Fatal(err)
	}
	f.state[id] = st
}

func (f *cleanFixture) makeSession(id string, last time.Time, closed *time.Time) {
	f.t.Helper()
	sess := store.NewSession(id, id, "cli", f.root, f.root, last)
	sess.LastActivityAt = last
	if closed != nil {
		sess.Open = false
		sess.ClosedAt = closed
	}
	if err := store.SaveSession(f.l, sess); err != nil {
		f.t.Fatal(err)
	}
}

func (f *cleanFixture) makeQueue(sid, turnID, baseline, final string) {
	f.t.Helper()
	q := store.NewQueue(sid, 10)
	q.Turns = []store.QueueTurn{{
		TurnID: turnID, Seq: 1, ShortID: store.ShortID(turnID), Status: store.TurnCompleted,
		StartedAt: f.now, BaselineStateID: baseline, FinalStateID: final, Display: display.Display{},
	}}
	q.NextSeq = 2
	if err := store.SaveQueue(f.l, q); err != nil {
		f.t.Fatal(err)
	}
}

func (f *cleanFixture) existsState(id string) bool {
	path, err := snapshot.StatePath(f.l, id)
	if err != nil {
		f.t.Fatal(err)
	}
	_, err = os.Stat(path)
	return err == nil
}

func (f *cleanFixture) existsObject(content string) bool {
	oid := f.obj[content]
	path, err := f.cas.Path(oid)
	if err != nil {
		f.t.Fatal(err)
	}
	_, err = os.Stat(path)
	return err == nil
}

func (f *cleanFixture) sessionDirExists(sid string) bool {
	dir, err := f.l.SessionDir(sid)
	if err != nil {
		f.t.Fatal(err)
	}
	_, err = os.Stat(dir)
	return err == nil
}

func TestCleanDryRunThenExecuteKeepsSharedObjectsAndIncompleteOps(t *testing.T) {
	f := newCleanFixture(t)
	opts := Options{Root: f.root, OlderThan: 30 * 24 * time.Hour, Now: func() time.Time { return f.now }}
	plan, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.DryRun || plan.Executed {
		t.Fatalf("default clean was not a dry-run: %+v", plan)
	}
	if len(plan.Sessions) != 1 || plan.Sessions[0].SessionID != f.sidA {
		t.Fatalf("sessions=%+v", plan.Sessions)
	}
	if len(plan.Ops) != 1 || plan.Ops[0].OpID != "rewind-old" {
		t.Fatalf("ops=%+v", plan.Ops)
	}
	for _, id := range []string{"state-a-base", "state-a-final"} {
		found := false
		for _, s := range plan.States {
			if s == id {
				found = true
			}
		}
		if !found {
			t.Errorf("state %s not planned for deletion: %v", id, plan.States)
		}
	}
	for _, id := range []string{"state-b-base", "state-b-final", "state-safe"} {
		for _, s := range plan.States {
			if s == id {
				t.Errorf("reachable state %s planned for deletion", id)
			}
		}
	}
	sharedOID := f.obj["S"]
	for _, oid := range plan.Objects {
		if oid == sharedOID {
			t.Fatal("shared object planned for deletion")
		}
	}
	if !f.sessionDirExists(f.sidA) || !f.existsState("state-a-base") {
		t.Fatal("dry-run deleted data")
	}

	opts.Yes = true
	plan, err = Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Executed || plan.DryRun {
		t.Fatalf("execution flags: %+v", plan)
	}
	if f.sessionDirExists(f.sidA) {
		t.Fatal("expired session directory survived")
	}
	if f.sessionDirExists(f.sidB) == false {
		t.Fatal("recent session was deleted")
	}
	if _, err := store.LoadRewindOp(f.l, "rewind-old"); !os.IsNotExist(err) {
		t.Fatalf("old completed op survived: %v", err)
	}
	if _, err := store.LoadRewindOp(f.l, "rewind-open"); err != nil {
		t.Fatalf("incomplete op was deleted by default: %v", err)
	}
	if f.existsState("state-a-base") || f.existsState("state-a-final") {
		t.Fatal("unreachable session states survived")
	}
	if !f.existsState("state-b-base") || !f.existsState("state-b-final") || !f.existsState("state-safe") {
		t.Fatal("referenced states were deleted")
	}
	if !f.existsObject("S") {
		t.Fatal("shared object was deleted")
	}
	if !f.existsObject("Q") {
		t.Fatal("incomplete op safety object was deleted")
	}
	if f.existsObject("A-base") || f.existsObject("A-final") {
		t.Fatal("unreachable objects survived")
	}
	if plan.ReclaimBytes <= 0 {
		t.Fatalf("reclaim bytes=%d", plan.ReclaimBytes)
	}
}

func TestCleanIncludeIncompleteDeletesOldOps(t *testing.T) {
	f := newCleanFixture(t)
	plan, err := Run(context.Background(), Options{
		Root: f.root, OlderThan: 30 * 24 * time.Hour, Yes: true, IncludeIncomplete: true,
		Now: func() time.Time { return f.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Executed {
		t.Fatal("not executed")
	}
	found := false
	for _, op := range plan.Ops {
		if op.OpID == "rewind-open" {
			found = true
		}
	}
	if !found {
		t.Fatalf("incomplete op not in plan: %+v", plan.Ops)
	}
	if _, err := store.LoadRewindOp(f.l, "rewind-open"); !os.IsNotExist(err) {
		t.Fatalf("incomplete op survived explicit include: %v", err)
	}
	if f.existsState("state-safe") || f.existsObject("Q") {
		t.Fatal("orphaned safety state/object survived")
	}
	if !f.existsState("state-b-final") || !f.existsObject("S") {
		t.Fatal("retained session data was deleted")
	}
}

func TestCleanSkipsStaleOpenSessionsByDefault(t *testing.T) {
	f := newCleanFixture(t)
	// Reopen session A without a closedAt timestamp.
	sess, err := store.LoadSession(f.l, f.sidA)
	if err != nil {
		t.Fatal(err)
	}
	sess.Open = true
	sess.ClosedAt = nil
	if err := store.SaveSession(f.l, sess); err != nil {
		t.Fatal(err)
	}
	opts := Options{Root: f.root, OlderThan: 30 * 24 * time.Hour, Now: func() time.Time { return f.now }}
	plan, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan.Sessions {
		if s.SessionID == f.sidA {
			t.Fatal("stale open session was selected by default")
		}
	}
	opts.IncludeStaleOpen = true
	plan, err = Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range plan.Sessions {
		if s.SessionID == f.sidA {
			found = true
		}
	}
	if !found {
		t.Fatal("stale open session not selected with --include-stale-open")
	}
}

func TestParseAge(t *testing.T) {
	cases := map[string]time.Duration{
		"30d": 30 * 24 * time.Hour,
		"48h": 48 * time.Hour,
		"2w":  14 * 24 * time.Hour,
		"90m": 90 * time.Minute,
		"7":   7 * 24 * time.Hour,
	}
	for in, want := range cases {
		got, err := ParseAge(in)
		if err != nil || got != want {
			t.Errorf("ParseAge(%q)=%v,%v want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "0d", "-1d", "abc", "1x"} {
		if _, err := ParseAge(bad); err == nil {
			t.Errorf("ParseAge(%q) succeeded", bad)
		}
	}
}
