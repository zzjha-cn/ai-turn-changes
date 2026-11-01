package store

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/zzjha-cn/codex-rewind/internal/display"
	"github.com/zzjha-cn/codex-rewind/internal/testutil"
)

func newTestLayout(t *testing.T) *Layout {
	t.Helper()
	root := testutil.TempRepo(t)
	l := New(root)
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	return l
}

func addTestItem(t *testing.T, l *Layout, sessionID, turnID string, seq int64, status string) *TurnItem {
	t.Helper()
	if _, err := l.EnsureSession(sessionID); err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(seq) * time.Minute)
	it := &TurnItem{
		SchemaVersion:   TurnSchemaVersion,
		TurnID:          turnID,
		Seq:             seq,
		Status:          status,
		Prompt:          fmt.Sprintf("prompt %d", seq),
		StartedAt:       started,
		BaselineStateID: fmt.Sprintf("state-base-%d", seq),
		Changes:         []FileChange{},
	}
	if status != TurnRunning {
		it.FinalStateID = fmt.Sprintf("state-final-%d", seq)
	}
	if err := SaveTurnItem(l, sessionID, it); err != nil {
		t.Fatal(err)
	}
	return it
}

func TestQueueTrimKeepsNewestTerminalTurns(t *testing.T) {
	l := newTestLayout(t)
	sid := "sess-trim"
	q := NewQueue(sid, 10)
	for i := int64(1); i <= 12; i++ {
		it := addTestItem(t, l, sid, fmt.Sprintf("turn-%02d", i), i, TurnCompleted)
		q.Upsert(it, display.Limits{})
		removed := q.Trim(10)
		for _, r := range removed {
			if err := DeleteTurnFiles(l, sid, r); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(q.Turns) != 10 {
		t.Fatalf("retained %d turns, want 10", len(q.Turns))
	}
	if q.FirstSeq != 3 || q.LastSeq != 12 {
		t.Fatalf("window %d..%d, want 3..12", q.FirstSeq, q.LastSeq)
	}
	items, errs := ListTurnItems(l, sid)
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(items) != 10 || items[0].Seq != 3 {
		t.Fatalf("items=%d first=%d", len(items), items[0].Seq)
	}
	if _, err := os.Stat(mustItemPath(t, l, sid, 1, "turn-01")); !os.IsNotExist(err) {
		t.Fatalf("oldest item still exists: %v", err)
	}
}

func TestQueueTrimDoesNotCountRunningTurns(t *testing.T) {
	l := newTestLayout(t)
	sid := "sess-running"
	q := NewQueue(sid, 10)
	for i := int64(1); i <= 10; i++ {
		q.Upsert(addTestItem(t, l, sid, fmt.Sprintf("turn-%02d", i), i, TurnCompleted), display.Limits{})
	}
	q.Upsert(addTestItem(t, l, sid, "turn-run1", 11, TurnRunning), display.Limits{})
	q.Upsert(addTestItem(t, l, sid, "turn-run2", 12, TurnRunning), display.Limits{})
	if removed := q.Trim(10); len(removed) != 0 {
		t.Fatalf("trim removed %v with 10 terminal + 2 running", removed)
	}
	if len(q.Turns) != 12 || q.RetainedTurnCount != 10 {
		t.Fatalf("turns=%d terminal=%d", len(q.Turns), q.RetainedTurnCount)
	}
	q.Upsert(addTestItem(t, l, sid, "turn-13", 13, TurnCompleted), display.Limits{})
	removed := q.Trim(10)
	if len(removed) != 1 || removed[0].TurnID != "turn-01" {
		t.Fatalf("removed=%v, want oldest terminal turn-01", removed)
	}
	if q.Turn("turn-run1") == nil || q.Turn("turn-run2") == nil {
		t.Fatal("running turns were removed")
	}
}

func TestQueueRebuildFromItems(t *testing.T) {
	l := newTestLayout(t)
	sid := "sess-rebuild"
	for i := int64(1); i <= 12; i++ {
		addTestItem(t, l, sid, fmt.Sprintf("turn-%02d", i), i, TurnCompleted)
	}
	path, err := l.QueuePath(sid)
	if err != nil {
		t.Fatal(err)
	}
	garbage := []byte("{ this is not json")
	if err := os.WriteFile(path, garbage, 0600); err != nil {
		t.Fatal(err)
	}
	ro, err := LoadQueueReadOnly(l, sid, 10, display.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !ro.Rebuilt || len(ro.Turns) != 10 || ro.FirstSeq != 3 {
		t.Fatalf("readonly rebuild: rebuilt=%v n=%d first=%d", ro.Rebuilt, len(ro.Turns), ro.FirstSeq)
	}
	if data, _ := os.ReadFile(path); string(data) != string(garbage) {
		t.Fatal("read-only load rewrote a corrupt queue")
	}
	q, err := LoadQueue(l, sid, 10, display.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !q.Rebuilt || q.NextSeq != 13 || len(q.Turns) != 10 {
		t.Fatalf("rebuild: rebuilt=%v next=%d n=%d", q.Rebuilt, q.NextSeq, len(q.Turns))
	}
	saved, err := LoadQueue(l, sid, 10, display.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Rebuilt || len(saved.Turns) != 10 {
		t.Fatalf("persisted queue not readable: %+v", saved)
	}
}

func TestQueueUpsertUpdatesAndEventLogBounded(t *testing.T) {
	l := newTestLayout(t)
	sid := "sess-events"
	it := addTestItem(t, l, sid, "turn-01", 1, TurnRunning)
	q := NewQueue(sid, 10)
	q.Upsert(it, display.Limits{})
	it.Status = TurnCompleted
	it.LastAssistantMessage = "done"
	q.Upsert(it, display.Limits{})
	if len(q.Turns) != 1 || q.Turns[0].Status != TurnCompleted {
		t.Fatalf("upsert did not replace: %+v", q.Turns)
	}
	for i := 0; i < 8; i++ {
		ev := Event{At: time.Now(), Event: "PreToolUse", Tool: "shell", Paths: []string{"a"}}
		if err := AppendEvent(l, sid, 1, "turn-01", ev, 3, 1<<20); err != nil {
			t.Fatal(err)
		}
	}
	events, err := ReadEvents(l, sid, 1, "turn-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events=%d, want bounded 3", len(events))
	}
	if events[0].SchemaVersion != EventSchemaVersion {
		t.Fatalf("event schemaVersion=%d", events[0].SchemaVersion)
	}
	if err := DeleteTurnFiles(l, sid, q.Turns[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mustItemPath(t, l, sid, 1, "turn-01")); !os.IsNotExist(err) {
		t.Fatal("item was not deleted")
	}
	if _, err := os.Stat(mustEventPath(t, l, sid, 1, "turn-01")); !os.IsNotExist(err) {
		t.Fatal("events were not deleted")
	}
}

func TestShortID(t *testing.T) {
	if got := ShortID("01a19106-9444-7ff1"); got != "01a19106" {
		t.Fatalf("got %q", got)
	}
	if got := ShortID("short"); got != "short" {
		t.Fatalf("got %q", got)
	}
}

func TestLockAndStaleDetection(t *testing.T) {
	l := newTestLayout(t)
	lock, err := TryAcquire(l.LockPath(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TryAcquire(l.LockPath(), "second"); err == nil {
		t.Fatal("second acquire succeeded")
	} else if err != ErrLocked {
		t.Fatalf("err=%v, want ErrLocked", err)
	}
	if stale, err := Stale(l.LockPath()); err != nil || stale {
		t.Fatalf("held lock reported stale=%v err=%v", stale, err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if stale, err := Stale(l.LockPath()); err != nil || stale {
		t.Fatalf("released lock reported stale=%v err=%v", stale, err)
	}
	// Simulate a crashed holder: no releasedAt and a PID that cannot exist.
	if err := os.WriteFile(l.LockPath(), []byte(`{"pid":1073741824,"startedAt":"2026-01-01T00:00:00Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if stale, err := Stale(l.LockPath()); err != nil || !stale {
		t.Fatalf("dead holder reported stale=%v err=%v", stale, err)
	}
}

func mustItemPath(t *testing.T, l *Layout, sid string, seq int64, turn string) string {
	t.Helper()
	p, err := TurnItemPath(l, sid, seq, turn)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mustEventPath(t *testing.T, l *Layout, sid string, seq int64, turn string) string {
	t.Helper()
	p, err := EventPath(l, sid, seq, turn)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
