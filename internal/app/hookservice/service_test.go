package hookservice

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zzjha-cn/codex-rewind/internal/display"
	"github.com/zzjha-cn/codex-rewind/internal/snapshot"
	"github.com/zzjha-cn/codex-rewind/internal/store"
	"github.com/zzjha-cn/codex-rewind/internal/testutil"
)

const testSession = "sess-hooks"

type harness struct {
	t   *testing.T
	svc *Service
	l   *store.Layout
	dir string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := testutil.TempRepo(t)
	clock := testutil.FixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	svc := New(Options{RootOverride: root, Now: clock})
	return &harness{t: t, svc: svc, l: store.New(root), dir: root}
}

func (h *harness) handle(p Payload) error {
	h.t.Helper()
	_, err := h.svc.Handle(context.Background(), p)
	return err
}

func (h *harness) sessionStart() {
	h.t.Helper()
	if err := h.handle(Payload{
		SessionID: testSession, ThreadID: testSession, Cwd: h.dir,
		HookEventName: EventSessionStart, Source: "cli",
	}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) prompt(turnID, prompt string) {
	h.t.Helper()
	if err := h.handle(Payload{
		SessionID: testSession, TurnID: turnID, Cwd: h.dir,
		HookEventName: EventUserPromptSubmit, Prompt: prompt, Source: "cli",
	}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) stop(turnID, assistant string) {
	h.t.Helper()
	if err := h.handle(Payload{
		SessionID: testSession, TurnID: turnID, Cwd: h.dir,
		HookEventName: EventStop, LastAssistantMessage: assistant,
	}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) interrupt(turnID string) {
	h.t.Helper()
	if err := h.handle(Payload{
		SessionID: testSession, TurnID: turnID, Cwd: h.dir,
		HookEventName: EventInterrupt,
	}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) doTurn(n int) string {
	h.t.Helper()
	turnID := "turn-" + itoa(n)
	h.prompt(turnID, "prompt "+itoa(n))
	testutil.Write(h.t, h.dir, "app.txt", "base\nchange "+itoa(n)+"\n")
	testutil.Write(h.t, h.dir, "f"+itoa(n)+".txt", "file "+itoa(n)+"\n")
	h.stop(turnID, "reply "+itoa(n))
	return turnID
}

func (h *harness) queue() *store.Queue {
	h.t.Helper()
	q, err := store.LoadQueueReadOnly(h.l, testSession, 10, display.Limits{})
	if err != nil {
		h.t.Fatal(err)
	}
	return q
}

func (h *harness) item(turnID string) *store.TurnItem {
	h.t.Helper()
	q := h.queue()
	t := q.Turn(turnID)
	if t == nil {
		h.t.Fatalf("turn %s not in queue", turnID)
	}
	it, err := store.LoadTurnItem(h.l, testSession, t.Seq, turnID)
	if err != nil {
		h.t.Fatal(err)
	}
	return it
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestHookLifecycleElevenTurnsKeepsTen(t *testing.T) {
	h := newHarness(t)
	h.sessionStart()
	for i := 1; i <= 11; i++ {
		h.doTurn(i)
	}
	q := h.queue()
	if len(q.Turns) != 10 || q.FirstSeq != 2 || q.LastSeq != 11 {
		t.Fatalf("queue n=%d window %d..%d", len(q.Turns), q.FirstSeq, q.LastSeq)
	}
	if q.NextSeq != 12 {
		t.Fatalf("nextSeq=%d", q.NextSeq)
	}
	items, errs := store.ListTurnItems(h.l, testSession)
	if len(errs) != 0 || len(items) != 10 || items[0].Seq != 2 {
		t.Fatalf("items=%d errs=%v", len(items), errs)
	}
	first := h.item("turn-2")
	if first.BaselineStateID == "" || first.FinalStateID == "" {
		t.Fatalf("turn 2 states missing: %+v", first)
	}
	if first.LastAssistantMessage != "reply 2" {
		t.Fatalf("assistant=%q", first.LastAssistantMessage)
	}
	sess, err := store.LoadSession(h.l, testSession)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Title != "prompt 1" || sess.ActiveTurn() != "" {
		t.Fatalf("session title=%q active=%q", sess.Title, sess.ActiveTurn())
	}
	// Turn 2's baseline is the final state of turn 1, so it must survive even
	// though turn 1 itself has left the window.
	if _, err := snapshot.LoadState(h.l, first.BaselineStateID); err != nil {
		t.Fatalf("retained baseline missing: %v", err)
	}
}

func TestHookInterruptAndLazyFinalize(t *testing.T) {
	h := newHarness(t)
	h.sessionStart()
	h.doTurn(1)
	h.prompt("turn-2", "will be interrupted")
	it := h.item("turn-2")
	if it.Status != store.TurnRunning {
		t.Fatalf("turn 2 status=%s", it.Status)
	}
	h.interrupt("turn-2")
	it = h.item("turn-2")
	if it.Status != store.TurnInterrupted || !it.NeedsFinalize {
		t.Fatalf("interrupt: status=%s needsFinalize=%v", it.Status, it.NeedsFinalize)
	}
	if it.FinalStateID != "" {
		t.Fatalf("interrupted turn already has a final state")
	}

	// The next prompt lazily finalizes the interrupted turn with the current
	// workspace before starting the new baseline.
	h.prompt("turn-3", "next")
	it = h.item("turn-2")
	if it.Status != store.TurnInterrupted || it.NeedsFinalize || it.FinalStateID == "" || it.EndedAt == nil {
		t.Fatalf("lazy finalize: %+v", it)
	}
	it3 := h.item("turn-3")
	if it3.Status != store.TurnRunning {
		t.Fatalf("turn 3 status=%s", it3.Status)
	}
	h.interrupt("turn-3")
	h.stop("turn-3", "late stop")
	it3 = h.item("turn-3")
	if it3.Status != store.TurnInterrupted || it3.NeedsFinalize || it3.FinalStateID == "" {
		t.Fatalf("Stop over an interrupted turn: %+v", it3)
	}
}

func TestHookQueueCorruptionRebuilds(t *testing.T) {
	h := newHarness(t)
	h.sessionStart()
	for i := 1; i <= 3; i++ {
		h.doTurn(i)
	}
	path, err := h.l.QueuePath(testSession)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRaw(path, "{broken"); err != nil {
		t.Fatal(err)
	}
	h.prompt("turn-4", "after corruption")
	h.stop("turn-4", "ok")
	q := h.queue()
	if len(q.Turns) != 4 || q.NextSeq != 5 || q.Turn("turn-1") == nil {
		t.Fatalf("rebuilt queue: n=%d next=%d", len(q.Turns), q.NextSeq)
	}
}

func TestHookSessionEndCloses(t *testing.T) {
	h := newHarness(t)
	h.sessionStart()
	h.doTurn(1)
	if err := h.handle(Payload{SessionID: testSession, Cwd: h.dir, HookEventName: EventSessionEnd}); err != nil {
		t.Fatal(err)
	}
	sess, err := store.LoadSession(h.l, testSession)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Open || sess.ClosedAt == nil {
		t.Fatalf("session not closed: %+v", sess)
	}
}

func TestHandleRejectsBadPayloads(t *testing.T) {
	h := newHarness(t)
	if err := h.handle(Payload{SessionID: "bad/session", Cwd: h.dir, HookEventName: EventSessionStart}); err == nil {
		t.Fatal("invalid session id accepted")
	}
	if err := h.handle(Payload{SessionID: testSession, Cwd: h.dir, HookEventName: "Nope"}); err == nil {
		t.Fatal("unsupported event accepted")
	}
	if err := h.handle(Payload{Cwd: h.dir, HookEventName: EventSessionStart}); err == nil {
		t.Fatal("missing session id accepted")
	}
}

func TestDecodePayload(t *testing.T) {
	p, err := DecodePayload(stringsReader(`{"session_id":"s1","hook_event_name":"UserPromptSubmit","user_prompt":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.PromptText() != "hello" || p.ThreadID != "s1" || !p.Supported() {
		t.Fatalf("payload=%+v", p)
	}
	if _, err := DecodePayload(stringsReader(`{`)); err == nil {
		t.Fatal("bad json accepted")
	}
	if _, err := DecodePayload(stringsReader(`{"session_id":"s","hook_event_name":"Stop","stop_hook_active":true}`)); err != nil {
		t.Fatal(err)
	}
}

func TestExtractPaths(t *testing.T) {
	input := json.RawMessage(`{
		"file_path": "a/b.txt",
		"files": ["c.txt", "/etc/passwd", "../escape", "d.txt"],
		"patch": "*** Update File: e.txt\n*** Add File: f/g.txt\n*** Delete File: ../bad"
	}`)
	got := ExtractPaths("apply_patch", input)
	want := map[string]bool{"a/b.txt": true, "c.txt": true, "d.txt": true, "e.txt": true, "f/g.txt": true}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for _, p := range got {
		if !want[p] {
			t.Fatalf("unexpected path %q in %v", p, got)
		}
	}
	if paths := ExtractPaths("shell", json.RawMessage(`{"command":"echo hi"}`)); len(paths) != 0 {
		t.Fatalf("shell command produced paths: %v", paths)
	}
}

func stringsReader(s string) *strings.Reader { return strings.NewReader(s) }

func writeRaw(path, content string) error { return os.WriteFile(path, []byte(content), 0600) }
