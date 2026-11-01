package snapshot

import "testing"

func TestBuildPlanExplicitAbsenceIsNotConflict(t *testing.T) {
	target := &State{StateID: "t", Files: []FileEntry{*entry("a", 1, 0644)}}
	current := &State{StateID: "c"} // file is absent now
	latest := map[string]*FileEntry{"a": {Path: "a", Exists: false}}
	plan := BuildPlan(target, current, []string{"a"}, latest)
	if len(plan.Conflicts) != 0 {
		t.Fatalf("explicit absence treated as manual edit: %+v", plan.Conflicts)
	}
	if plan.Writes() != 1 {
		t.Fatalf("actions=%+v", plan.Actions)
	}

	// A manual creation after the recorded absence is a conflict.
	target2 := &State{StateID: "t2"}
	current2 := &State{StateID: "c2", Files: []FileEntry{*entry("a", 2, 0644)}}
	plan = BuildPlan(target2, current2, []string{"a"}, latest)
	if len(plan.Conflicts) != 1 || plan.Deletes() != 1 {
		t.Fatalf("manual creation not flagged: %+v", plan)
	}
}
