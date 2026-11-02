package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/zzjha-cn/codex-rewind/internal/journal"
	"github.com/zzjha-cn/codex-rewind/internal/store"
	"github.com/zzjha-cn/codex-rewind/internal/testutil"
)

func saveCompletedRewind(t *testing.T, root, opID, sessionID, threadID, targetTurn, newThread string) *store.RewindOp {
	t.Helper()
	l := store.New(root)
	op := store.NewRewindOp(opID, sessionID, threadID, targetTurn, store.ModeBefore, "state-target", time.Now())
	op.NewThreadID = &newThread
	op.SetPhase(journal.Completed, time.Now())
	if err := store.SaveRewindOp(l, op); err != nil {
		t.Fatalf("save rewind op: %v", err)
	}
	return op
}

func TestCLIListShowsRewindSourceAndPendingFork(t *testing.T) {
	root := testutil.TempRepo(t)
	cliStart(t, root)
	cliTurn(t, root, 1)
	target := cliTurn(t, root, 2)
	cliTurn(t, root, 3)
	forkID := "sess-fork"
	saveCompletedRewind(t, root, "op-1", cliSession, cliSession, target, forkID)

	code, out, stderr := runCLI(t, []string{"list", "--root", root}, "")
	if code != 0 {
		t.Fatalf("list exit=%d stderr=%s", code, stderr)
	}
	for _, want := range []string{
		"SESSION sess-cli",
		"rewind source",
		"rewind at [2]",
		"new thread sess-for",
		"pending resume: codex resume sess-fork",
		"[rewind target]",
		"excluded from new thread sess-for",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("list output missing %q:\n%s", want, out)
		}
	}

	code, out, stderr = runCLI(t, []string{"list", "--root", root, "--json"}, "")
	if code != 0 {
		t.Fatalf("list json exit=%d stderr=%s", code, stderr)
	}
	var lj listJSON
	if err := json.Unmarshal([]byte(out), &lj); err != nil {
		t.Fatalf("list json: %v\n%s", err, out)
	}
	if len(lj.Sessions) != 1 {
		t.Fatalf("default list should focus one branch, got %d", len(lj.Sessions))
	}
	src := lj.Sessions[0]
	if src.Rewind == nil || !src.Rewind.PendingResume {
		t.Fatalf("rewind view=%+v", src.Rewind)
	}
	if !src.Rewind.TargetInWindow || src.Rewind.TargetSeq != 2 || src.Rewind.NewThreadID != forkID {
		t.Fatalf("rewind target=%+v", src.Rewind)
	}
	if src.ForkedFrom != nil {
		t.Fatalf("source should not be marked as fork: %+v", src.ForkedFrom)
	}

	// --all shows the source and a synthetic pending fork together.
	code, out, stderr = runCLI(t, []string{"list", "--root", root, "--all"}, "")
	if code != 0 {
		t.Fatalf("list --all exit=%d stderr=%s", code, stderr)
	}
	for _, want := range []string{
		"SESSION sess-cli",
		"SESSION sess-for",
		"rewind fork",
		"forked from sess-cli at [2]",
		"pending resume",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("list --all output missing %q:\n%s", want, out)
		}
	}

	code, out, stderr = runCLI(t, []string{"list", "--root", root, "--all", "--json"}, "")
	if code != 0 {
		t.Fatalf("list --all json exit=%d stderr=%s", code, stderr)
	}
	var aj listJSON
	if err := json.Unmarshal([]byte(out), &aj); err != nil {
		t.Fatalf("list --all json: %v\n%s", err, out)
	}
	if len(aj.Sessions) != 2 {
		t.Fatalf("list --all should include source and pending fork, got %d", len(aj.Sessions))
	}
	var pending *sessionJSON
	for i := range aj.Sessions {
		if aj.Sessions[i].PendingResume {
			pending = &aj.Sessions[i]
		}
	}
	if pending == nil || pending.Session == nil || pending.Session.SessionID != forkID {
		t.Fatalf("pending fork entry=%+v", pending)
	}
	if pending.ForkedFrom == nil || pending.ForkedFrom.TargetSeq != 2 {
		t.Fatalf("pending forkedFrom=%+v", pending.ForkedFrom)
	}
}

func TestCLIListFocusesForkAfterResume(t *testing.T) {
	root := testutil.TempRepo(t)
	cliStart(t, root)
	cliTurn(t, root, 1)
	target := cliTurn(t, root, 2)
	forkID := "sess-fork"
	saveCompletedRewind(t, root, "op-1", cliSession, cliSession, target, forkID)

	// Resume the fork: the SessionStart hook creates its session directory.
	hookEvent(t, root, map[string]any{
		"hook_event_name": "SessionStart",
		"session_id":      forkID,
		"thread_id":       forkID,
		"source":          "resume",
	})

	code, out, stderr := runCLI(t, []string{"list", "--root", root}, "")
	if code != 0 {
		t.Fatalf("list exit=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(out, "SESSION sess-for") {
		t.Fatalf("default list should focus the fork:\n%s", out)
	}
	if strings.Contains(out, "SESSION sess-cli") {
		t.Fatalf("default list should not show the source branch:\n%s", out)
	}
	for _, want := range []string{"rewind fork", "forked from sess-cli at [2]", "(no retained turns)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("fork output missing %q:\n%s", want, out)
		}
	}

	code, out, stderr = runCLI(t, []string{"status", "--root", root}, "")
	if code != 0 {
		t.Fatalf("status exit=%d stderr=%s", code, stderr)
	}
	for _, want := range []string{"Rewinds:          1", "Last rewind:", "sess-cli at [2] (before) -> sess-for"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "pending resume)") {
		t.Fatalf("status should not report a pending resume:\n%s", out)
	}

	code, out, stderr = runCLI(t, []string{"status", "--root", root, "--json"}, "")
	if code != 0 {
		t.Fatalf("status json exit=%d stderr=%s", code, stderr)
	}
	var st statusJSON
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("status json: %v\n%s", err, out)
	}
	if st.Rewinds != 1 || st.PendingResumes != 0 || st.LastRewind == nil {
		t.Fatalf("status=%+v", st)
	}
	if st.LastRewind.PendingResume || st.LastRewind.TargetSeq != 2 {
		t.Fatalf("last rewind=%+v", st.LastRewind)
	}
}

func TestCLIStatusReportsPendingResume(t *testing.T) {
	root := testutil.TempRepo(t)
	cliStart(t, root)
	cliTurn(t, root, 1)
	target := cliTurn(t, root, 2)
	saveCompletedRewind(t, root, "op-1", cliSession, cliSession, target, "sess-fork")

	code, out, stderr := runCLI(t, []string{"status", "--root", root}, "")
	if code != 0 {
		t.Fatalf("status exit=%d stderr=%s", code, stderr)
	}
	for _, want := range []string{"Rewinds:          1 (1 pending resume)", "pending resume)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output missing %q:\n%s", want, out)
		}
	}
}
