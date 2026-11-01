package rewindservice_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zzjha-cn/codex-rewind/internal/app/hookservice"
	"github.com/zzjha-cn/codex-rewind/internal/app/rewindservice"
	"github.com/zzjha-cn/codex-rewind/internal/display"
	"github.com/zzjha-cn/codex-rewind/internal/journal"
	"github.com/zzjha-cn/codex-rewind/internal/snapshot"
	"github.com/zzjha-cn/codex-rewind/internal/store"
	"github.com/zzjha-cn/codex-rewind/internal/testutil"
)

const sid = "sess-rewind"

type fakeForker struct {
	id    string
	err   error
	calls []rewindservice.ForkRequest
}

func (f *fakeForker) Fork(_ context.Context, req rewindservice.ForkRequest) (string, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return "", f.err
	}
	if f.id == "" {
		return "new-thread-test", nil
	}
	return f.id, nil
}

type harness struct {
	t      *testing.T
	root   string
	clock  func() time.Time
	hook   *hookservice.Service
	forker *fakeForker
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := testutil.TempRepo(t)
	clock := testutil.FixedClock(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	h := &harness{t: t, root: root, clock: clock, forker: &fakeForker{id: "new-thread-test"}}
	h.hook = hookservice.New(hookservice.Options{RootOverride: root, Now: clock})
	if _, err := h.hook.Handle(context.Background(), hookservice.Payload{
		SessionID: sid, ThreadID: sid, Cwd: root, HookEventName: hookservice.EventSessionStart, Source: "cli",
	}); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) prompt(turnID, prompt string) {
	h.t.Helper()
	if _, err := h.hook.Handle(context.Background(), hookservice.Payload{
		SessionID: sid, TurnID: turnID, Cwd: h.root, HookEventName: hookservice.EventUserPromptSubmit, Prompt: prompt,
	}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) stop(turnID, assistant string) {
	h.t.Helper()
	if _, err := h.hook.Handle(context.Background(), hookservice.Payload{
		SessionID: sid, TurnID: turnID, Cwd: h.root, HookEventName: hookservice.EventStop, LastAssistantMessage: assistant,
	}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) interrupt(turnID string) {
	h.t.Helper()
	if _, err := h.hook.Handle(context.Background(), hookservice.Payload{
		SessionID: sid, TurnID: turnID, Cwd: h.root, HookEventName: hookservice.EventInterrupt,
	}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) doTurn(n int) string {
	h.t.Helper()
	turnID := "turn-" + itoa(n)
	h.prompt(turnID, "prompt "+itoa(n))
	content := testutil.Read(h.t, h.root, "app.txt")
	testutil.Write(h.t, h.root, "app.txt", content+"change "+itoa(n)+"\n")
	testutil.Write(h.t, h.root, "f"+itoa(n)+".txt", "file "+itoa(n)+"\n")
	h.stop(turnID, "reply "+itoa(n))
	return turnID
}

func (h *harness) rewind(req rewindservice.Request, force, resume, dry bool) (*rewindservice.Result, error) {
	h.t.Helper()
	sets := rewindservice.Options{Root: h.root, Force: force, Resume: resume, DryRun: dry, Forker: h.forker, Now: h.clock}
	return rewindservice.New(sets).Rewind(context.Background(), req)
}

func (h *harness) queue() *store.Queue {
	h.t.Helper()
	q, err := store.LoadQueueReadOnly(store.New(h.root), sid, 10, display.Limits{})
	if err != nil {
		h.t.Fatal(err)
	}
	return q
}

func (h *harness) ops() []*store.RewindOp {
	h.t.Helper()
	ops, errs := store.ListRewindOps(store.New(h.root))
	if len(errs) > 0 {
		h.t.Fatal(errs[0])
	}
	return ops
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestRewindBeforeRestoresBaseline(t *testing.T) {
	h := newHarness(t)
	for i := 1; i <= 3; i++ {
		h.doTurn(i)
	}
	res, err := h.rewind(rewindservice.Request{SessionID: sid, Seq: 2, Mode: store.ModeBefore}, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := testutil.Read(t, h.root, "app.txt"); got != "base\nchange 1\n" {
		t.Fatalf("app=%q", got)
	}
	if !testutil.Exists(h.root, "f1.txt") || testutil.Exists(h.root, "f2.txt") || testutil.Exists(h.root, "f3.txt") {
		t.Fatal("file set does not match turn 1 final state")
	}
	if len(h.forker.calls) != 1 || h.forker.calls[0].Mode != store.ModeBefore || h.forker.calls[0].TurnID != "turn-2" {
		t.Fatalf("fork calls=%+v", h.forker.calls)
	}
	if res.NewThreadID != "new-thread-test" || res.Op == nil || res.Op.Phase != journal.Completed {
		t.Fatalf("result=%+v", res)
	}
	ops := h.ops()
	if len(ops) != 1 || ops[0].Phase != journal.Completed || ops[0].NewThreadID == nil {
		t.Fatalf("ops=%+v", ops)
	}
	if res.Plan.Writes() != 1 || res.Plan.Deletes() != 2 {
		t.Fatalf("plan writes=%d deletes=%d", res.Plan.Writes(), res.Plan.Deletes())
	}
}

func TestRewindThroughRestoresFinal(t *testing.T) {
	h := newHarness(t)
	for i := 1; i <= 3; i++ {
		h.doTurn(i)
	}
	res, err := h.rewind(rewindservice.Request{SessionID: sid, Seq: 2, Mode: store.ModeThrough}, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := testutil.Read(t, h.root, "app.txt"); got != "base\nchange 1\nchange 2\n" {
		t.Fatalf("app=%q", got)
	}
	if !testutil.Exists(h.root, "f1.txt") || !testutil.Exists(h.root, "f2.txt") || testutil.Exists(h.root, "f3.txt") {
		t.Fatal("through file set wrong")
	}
	if len(h.forker.calls) != 1 || h.forker.calls[0].Mode != store.ModeThrough {
		t.Fatalf("fork calls=%+v", h.forker.calls)
	}
	if res.Op.Phase != journal.Completed {
		t.Fatalf("phase=%s", res.Op.Phase)
	}
}

func TestRewindLast(t *testing.T) {
	h := newHarness(t)
	for i := 1; i <= 3; i++ {
		h.doTurn(i)
	}
	res, err := h.rewind(rewindservice.Request{SessionID: sid, Last: true, Mode: store.ModeBefore}, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Target.TurnID != "turn-3" {
		t.Fatalf("target=%s", res.Target.TurnID)
	}
	if got := testutil.Read(t, h.root, "app.txt"); got != "base\nchange 1\nchange 2\n" {
		t.Fatalf("app=%q", got)
	}
	if testutil.Exists(h.root, "f3.txt") {
		t.Fatal("f3 not removed")
	}
}

func TestRewindConflictBlocksThenForce(t *testing.T) {
	h := newHarness(t)
	for i := 1; i <= 3; i++ {
		h.doTurn(i)
	}
	content := testutil.Read(t, h.root, "app.txt")
	testutil.Write(t, h.root, "app.txt", content+"manual edit\n")

	res, err := h.rewind(rewindservice.Request{SessionID: sid, Seq: 2, Mode: store.ModeBefore}, false, false, true)
	if err != nil {
		t.Fatalf("dry-run should report conflicts without failing: %v", err)
	}
	if res.Plan == nil || len(res.Plan.Conflicts) != 1 || res.Plan.Conflicts[0].Path != "app.txt" {
		t.Fatalf("dry-run conflicts=%+v", res)
	}
	if len(h.forker.calls) != 0 || len(h.ops()) != 0 {
		t.Fatal("dry-run forked or created an operation")
	}

	_, err = h.rewind(rewindservice.Request{SessionID: sid, Seq: 2, Mode: store.ModeBefore}, false, false, false)
	var conflictErr *rewindservice.ConflictError
	if !errors.As(err, &conflictErr) {
		t.Fatalf("err=%v, want ConflictError", err)
	}
	if len(h.forker.calls) != 0 || len(h.ops()) != 0 {
		t.Fatal("conflict refusal forked or created an operation")
	}
	if !strings.Contains(testutil.Read(t, h.root, "app.txt"), "manual edit") {
		t.Fatal("conflict refusal modified the file")
	}

	res, err = h.rewind(rewindservice.Request{SessionID: sid, Seq: 2, Mode: store.ModeBefore}, true, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := testutil.Read(t, h.root, "app.txt"); got != "base\nchange 1\n" {
		t.Fatalf("force app=%q", got)
	}
	if len(h.forker.calls) != 1 || len(h.ops()) != 1 || h.ops()[0].Phase != journal.Completed {
		t.Fatalf("force calls=%d ops=%+v", len(h.forker.calls), h.ops())
	}
	_ = res
}

func TestRewindMissingObjectFailsClosed(t *testing.T) {
	h := newHarness(t)
	for i := 1; i <= 3; i++ {
		h.doTurn(i)
	}
	q := h.queue()
	target := q.Turn("turn-2")
	if target == nil {
		t.Fatal("turn-2 missing")
	}
	l := store.New(h.root)
	st, err := snapshot.LoadState(l, target.BaselineStateID)
	if err != nil {
		t.Fatal(err)
	}
	app := st.Entry("app.txt")
	if app == nil {
		t.Fatal("app entry missing")
	}
	cas := snapshot.NewCAS(l)
	path, err := cas.Path(app.OID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	_, err = h.rewind(rewindservice.Request{SessionID: sid, Seq: 2, Mode: store.ModeBefore}, false, false, false)
	if err == nil || !strings.Contains(err.Error(), "refusing partial restore") {
		t.Fatalf("err=%v", err)
	}
	if len(h.forker.calls) != 0 {
		t.Fatal("forked despite a missing object")
	}
	if len(h.ops()) != 0 {
		t.Fatal("created an operation despite a missing object")
	}
	if testutil.Exists(h.root, "f3.txt") == false {
		t.Fatal("workspace changed despite fail-closed")
	}
}

func TestRewindResumeAfterForkFailure(t *testing.T) {
	h := newHarness(t)
	for i := 1; i <= 3; i++ {
		h.doTurn(i)
	}
	h.forker.err = errors.New("codex is down")
	req := rewindservice.Request{SessionID: sid, Seq: 2, Mode: store.ModeBefore}
	if _, err := h.rewind(req, false, false, false); err == nil {
		t.Fatal("expected fork failure")
	}
	if got := testutil.Read(t, h.root, "app.txt"); got != "base\nchange 1\nchange 2\nchange 3\n" {
		t.Fatalf("files changed after fork failure: %q", got)
	}
	ops := h.ops()
	if len(ops) != 1 || ops[0].Phase != journal.FailedRetryable || ops[0].NewThreadID != nil {
		t.Fatalf("op after failure=%+v", ops)
	}

	h.forker.err = nil
	h.forker.calls = nil
	res, err := h.rewind(rewindservice.Request{SessionID: sid, Seq: 2, Mode: store.ModeBefore}, false, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.forker.calls) != 1 {
		t.Fatalf("resume fork calls=%d", len(h.forker.calls))
	}
	if got := testutil.Read(t, h.root, "app.txt"); got != "base\nchange 1\n" {
		t.Fatalf("resume app=%q", got)
	}
	if res.Op.Phase != journal.Completed || res.Op.Error != nil {
		t.Fatalf("op=%+v", res.Op)
	}
}

func TestRewindResumeReusesForkedThread(t *testing.T) {
	h := newHarness(t)
	for i := 1; i <= 3; i++ {
		h.doTurn(i)
	}
	res, err := h.rewind(rewindservice.Request{SessionID: sid, Seq: 2, Mode: store.ModeBefore}, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.forker.calls) != 1 {
		t.Fatalf("first fork calls=%d", len(h.forker.calls))
	}
	op := res.Op
	// Simulate a crash after the fork but before files were restored.
	op.Phase = journal.ThreadForked
	op.Error = nil
	if err := store.SaveRewindOp(store.New(h.root), op); err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, h.root, "app.txt", "base\nchange 1\nchange 2\nchange 3\n")
	testutil.Write(t, h.root, "f2.txt", "file 2\n")
	testutil.Write(t, h.root, "f3.txt", "file 3\n")

	h.forker = &fakeForker{err: errors.New("must not fork again")}
	res2, err := h.rewind(rewindservice.Request{SessionID: sid, Seq: 2, Mode: store.ModeBefore}, false, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.forker.calls) != 0 {
		t.Fatalf("resume forked again: %+v", h.forker.calls)
	}
	if res2.NewThreadID != res.NewThreadID {
		t.Fatalf("thread changed: %s vs %s", res2.NewThreadID, res.NewThreadID)
	}
	if got := testutil.Read(t, h.root, "app.txt"); got != "base\nchange 1\n" {
		t.Fatalf("app=%q", got)
	}
	if res2.Op.Phase != journal.Completed {
		t.Fatalf("phase=%s", res2.Op.Phase)
	}
}

func TestRewindRestoresPreExistingUntrackedAndLeavesUnrelated(t *testing.T) {
	h := newHarness(t)
	testutil.Write(t, h.root, "notes.txt", "v0\n")
	h.doTurn(1)
	h.prompt("turn-2", "change notes")
	testutil.Write(t, h.root, "notes.txt", "v2\n")
	testutil.Write(t, h.root, "app.txt", "base\nchange 1\nchange 2\n")
	h.stop("turn-2", "ok")
	testutil.Write(t, h.root, "mine.txt", "keep me\n")

	if _, err := h.rewind(rewindservice.Request{SessionID: sid, Seq: 2, Mode: store.ModeBefore}, false, false, false); err != nil {
		t.Fatal(err)
	}
	if got := testutil.Read(t, h.root, "notes.txt"); got != "v0\n" {
		t.Fatalf("pre-existing untracked file not restored: %q", got)
	}
	if got := testutil.Read(t, h.root, "mine.txt"); got != "keep me\n" {
		t.Fatalf("unrelated untracked file touched: %q", got)
	}
}

func TestRewindThroughInterruptedIsRejected(t *testing.T) {
	h := newHarness(t)
	h.doTurn(1)
	h.prompt("turn-2", "interrupted")
	h.interrupt("turn-2")
	_, err := h.rewind(rewindservice.Request{SessionID: sid, Seq: 2, Mode: store.ModeThrough}, false, false, false)
	if err == nil || !strings.Contains(err.Error(), "no final state") {
		t.Fatalf("err=%v", err)
	}
	if len(h.forker.calls) != 0 {
		t.Fatal("forked for an interrupted through rewind")
	}
}

func TestRewindRefusesActiveTurn(t *testing.T) {
	h := newHarness(t)
	h.doTurn(1)
	h.prompt("turn-2", "still running")
	_, err := h.rewind(rewindservice.Request{SessionID: sid, Seq: 1, Mode: store.ModeBefore}, false, false, false)
	if err == nil || !strings.Contains(err.Error(), "active") {
		t.Fatalf("err=%v", err)
	}
	_, err = h.rewind(rewindservice.Request{SessionID: sid, Seq: 1, Mode: store.ModeBefore}, true, false, false)
	if err == nil || !strings.Contains(err.Error(), "does not override") {
		t.Fatalf("--force bypassed a running turn: err=%v", err)
	}
	if len(h.forker.calls) != 0 || len(h.ops()) != 0 {
		t.Fatal("active-turn refusal forked or wrote")
	}
}

func TestRewindRefusesHeldLock(t *testing.T) {
	h := newHarness(t)
	h.doTurn(1)
	h.doTurn(2)
	lock, err := store.TryAcquire(store.New(h.root).LockPath(), "test holder")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	_, err = h.rewind(rewindservice.Request{SessionID: sid, Seq: 1, Mode: store.ModeBefore}, true, false, false)
	if err == nil || !strings.Contains(err.Error(), "repository lock") {
		t.Fatalf("held lock was not refused: %v", err)
	}
	if len(h.forker.calls) != 0 {
		t.Fatal("forked while the lock was held")
	}
}

func TestRewindForceOverridesStaleActiveMarker(t *testing.T) {
	h := newHarness(t)
	h.doTurn(1)
	h.doTurn(2)
	l := store.New(h.root)
	sess, err := store.LoadSession(l, sid)
	if err != nil {
		t.Fatal(err)
	}
	sess.SetActive("turn-2") // completed turn: provably stale marker
	if err := store.SaveSession(l, sess); err != nil {
		t.Fatal(err)
	}
	if _, err := h.rewind(rewindservice.Request{SessionID: sid, Seq: 1, Mode: store.ModeBefore}, false, false, false); err == nil {
		t.Fatal("stale marker was not refused without --force")
	}
	res, err := h.rewind(rewindservice.Request{SessionID: sid, Seq: 1, Mode: store.ModeBefore}, true, false, false)
	if err != nil {
		t.Fatalf("force stale rewind: %v", err)
	}
	if len(h.forker.calls) != 1 {
		t.Fatalf("fork calls=%d", len(h.forker.calls))
	}
	if !strings.Contains(strings.Join(res.Warnings, ";"), "stale active marker") {
		t.Fatalf("missing stale warning: %v", res.Warnings)
	}
}

func TestRewindUnfinishedOpMustBeResumed(t *testing.T) {
	h := newHarness(t)
	for i := 1; i <= 3; i++ {
		h.doTurn(i)
	}
	h.forker.err = errors.New("fork down")
	req3 := rewindservice.Request{SessionID: sid, Seq: 3, Mode: store.ModeBefore}
	if _, err := h.rewind(req3, false, false, false); err == nil {
		t.Fatal("expected failure")
	}
	h.forker.err = nil
	h.forker.calls = nil
	_, err := h.rewind(rewindservice.Request{SessionID: sid, Seq: 2, Mode: store.ModeBefore}, false, false, false)
	if err == nil || !strings.Contains(err.Error(), "unfinished rewind op") {
		t.Fatalf("err=%v", err)
	}
	_, err = h.rewind(rewindservice.Request{SessionID: sid, Seq: 2, Mode: store.ModeBefore}, false, true, false)
	if err == nil || !strings.Contains(err.Error(), "targets another turn") {
		t.Fatalf("resume other target err=%v", err)
	}
	if len(h.forker.calls) != 0 {
		t.Fatal("guarded rewind still forked")
	}
}

func TestRewindOldestRetainedAfterElevenTurns(t *testing.T) {
	t.Run("before", func(t *testing.T) {
		h := newHarness(t)
		for i := 1; i <= 11; i++ {
			h.doTurn(i)
		}
		q := h.queue()
		if len(q.Turns) != 10 || q.FirstSeq != 2 {
			t.Fatalf("window n=%d first=%d", len(q.Turns), q.FirstSeq)
		}

		// Turn 1 is gone and cannot be selected, even by full turn id.
		for _, req := range []rewindservice.Request{
			{SessionID: sid, Seq: 1, Mode: store.ModeBefore},
			{SessionID: sid, TurnID: "turn-1", Mode: store.ModeBefore},
		} {
			_, err := h.rewind(req, false, false, true)
			if err == nil || !strings.Contains(err.Error(), "retained window") {
				t.Fatalf("req=%+v err=%v", req, err)
			}
		}

		// The oldest retained turn's baseline is the final state of the turn
		// that just left the window, so its objects must still be usable.
		res, err := h.rewind(rewindservice.Request{SessionID: sid, Seq: 2, Mode: store.ModeBefore}, false, false, false)
		if err != nil {
			t.Fatal(err)
		}
		if got := testutil.Read(t, h.root, "app.txt"); got != "base\nchange 1\n" {
			t.Fatalf("app=%q", got)
		}
		for i := 2; i <= 11; i++ {
			if testutil.Exists(h.root, "f"+itoa(i)+".txt") {
				t.Fatalf("f%d.txt survived --before turn 2", i)
			}
		}
		if !testutil.Exists(h.root, "f1.txt") {
			t.Fatal("f1.txt from the retained baseline was deleted")
		}
		if len(h.forker.calls) != 1 || h.forker.calls[0].TurnID != "turn-2" || h.forker.calls[0].Mode != store.ModeBefore {
			t.Fatalf("fork calls=%+v", h.forker.calls)
		}
		if res.Op.Phase != journal.Completed {
			t.Fatalf("phase=%s", res.Op.Phase)
		}
	})

	t.Run("through", func(t *testing.T) {
		h := newHarness(t)
		for i := 1; i <= 11; i++ {
			h.doTurn(i)
		}
		if _, err := h.rewind(rewindservice.Request{SessionID: sid, Seq: 2, Mode: store.ModeThrough}, false, false, false); err != nil {
			t.Fatal(err)
		}
		if got := testutil.Read(t, h.root, "app.txt"); got != "base\nchange 1\nchange 2\n" {
			t.Fatalf("through app=%q", got)
		}
		if !testutil.Exists(h.root, "f1.txt") || !testutil.Exists(h.root, "f2.txt") {
			t.Fatal("through did not restore f1/f2")
		}
		if testutil.Exists(h.root, "f3.txt") {
			t.Fatal("through left f3.txt")
		}
	})
}
