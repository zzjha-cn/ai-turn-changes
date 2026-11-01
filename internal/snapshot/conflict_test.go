package snapshot

import (
	"fmt"
	"testing"

	"github.com/zzjha-cn/codex-rewind/internal/workspace"
)

func fakeOID(n int) string { return workspace.OID(fmt.Sprintf("%064x", n)) }

func entry(path string, n int, mode uint32) *FileEntry {
	return &FileEntry{Path: path, Exists: true, OID: fakeOID(n), Mode: mode, Size: 1}
}

func TestBuildPlanActionsAndConflictDetection(t *testing.T) {
	target := &State{StateID: "state-target", Files: []FileEntry{
		*entry("a", 1, 0644),
		*entry("b", 2, 0644),
		*entry("lost", 3, 0644),
	}}
	current := &State{StateID: "state-current", Files: []FileEntry{
		*entry("a", 9, 0644), // modified manually after the recorded turn
		*entry("b", 2, 0644),
		*entry("old", 4, 0644), // introduced by a discarded turn
		*entry("unowned", 5, 0644),
	}}
	latest := map[string]*FileEntry{
		"a":    entry("a", 1, 0644),
		"b":    entry("b", 2, 0644),
		"old":  entry("old", 4, 0644),
		"lost": entry("lost", 3, 0644),
	}
	plan := BuildPlan(target, current, []string{"a", "b", "old", "lost", "unowned"}, latest)
	if plan.Writes() != 2 || plan.Deletes() != 2 {
		t.Fatalf("actions: writes=%d deletes=%d %+v", plan.Writes(), plan.Deletes(), plan.Actions)
	}
	if len(plan.Conflicts) != 3 {
		t.Fatalf("conflicts=%+v, want a/lost/unowned", plan.Conflicts)
	}
	conflicted := map[string]bool{}
	for _, c := range plan.Conflicts {
		conflicted[c.Path] = true
	}
	for _, p := range []string{"a", "lost", "unowned"} {
		if !conflicted[p] {
			t.Errorf("missing conflict for %s", p)
		}
	}
	if conflicted["b"] || conflicted["old"] {
		t.Errorf("unexpected conflict: %+v", plan.Conflicts)
	}
}

func TestBuildPlanNoActionWhenAlreadyMatching(t *testing.T) {
	target := &State{StateID: "t", Files: []FileEntry{*entry("a", 1, 0644)}}
	current := &State{StateID: "c", Files: []FileEntry{*entry("a", 1, 0644)}}
	plan := BuildPlan(target, current, []string{"a"}, map[string]*FileEntry{"a": entry("a", 1, 0644)})
	if len(plan.Actions) != 0 || len(plan.Conflicts) != 0 {
		t.Fatalf("unexpected plan: %+v", plan)
	}
}

func TestBuildPlanModeChangeIsRestored(t *testing.T) {
	target := &State{StateID: "t", Files: []FileEntry{*entry("script", 1, 0755)}}
	current := &State{StateID: "c", Files: []FileEntry{*entry("script", 1, 0644)}}
	plan := BuildPlan(target, current, []string{"script"}, map[string]*FileEntry{"script": entry("script", 1, 0755)})
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != ActionWrite {
		t.Fatalf("mode-only change not planned: %+v", plan.Actions)
	}
}

func TestUniqueSortedScope(t *testing.T) {
	got := uniqueSorted([]string{"b", "a", "b", "", "c"})
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("got %v", got)
	}
}
