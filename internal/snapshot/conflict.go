package snapshot

import (
	"fmt"
	"sort"
)

// ActionKind is a physical restore operation.
type ActionKind string

const (
	ActionWrite  ActionKind = "write"
	ActionDelete ActionKind = "delete"
)

// Action is one file that must be written or deleted.
type Action struct {
	Path     string
	Kind     ActionKind
	Expected *FileEntry
	Current  *FileEntry
}

// Conflict describes a path whose current content no longer matches the last
// recorded turn state.
type Conflict struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Plan is the computed restore plan for a target state.
type Plan struct {
	TargetStateID string
	Actions       []Action
	Conflicts     []Conflict
}

func (p *Plan) HasConflicts() bool { return len(p.Conflicts) > 0 }

func (p *Plan) Paths() []string {
	out := make([]string, 0, len(p.Actions))
	for _, a := range p.Actions {
		out = append(out, a.Path)
	}
	return out
}

func (p *Plan) Writes() int {
	n := 0
	for _, a := range p.Actions {
		if a.Kind == ActionWrite {
			n++
		}
	}
	return n
}

func (p *Plan) Deletes() int { return len(p.Actions) - p.Writes() }

// BuildPlan computes the restore actions for scope paths.
//
// latest maps each path to the newest recorded entry from the turns being
// discarded. A path whose current state differs from that entry was modified
// outside the discarded turns and is reported as a conflict.
func BuildPlan(target, current *State, scope []string, latest map[string]*FileEntry) *Plan {
	targetMap := map[string]*FileEntry{}
	currentMap := map[string]*FileEntry{}
	if target != nil {
		targetMap = target.Map()
	}
	if current != nil {
		currentMap = current.Map()
	}
	plan := &Plan{Conflicts: []Conflict{}}
	if target != nil {
		plan.TargetStateID = target.StateID
	}
	paths := uniqueSorted(scope)
	for _, p := range paths {
		desired := existing(targetMap[p])
		actual := existing(currentMap[p])
		if sameContent(desired, actual) {
			continue
		}
		expected := desired
		if latestEntry := latest[p]; latestEntry != nil {
			// A nil here means the newest recorded state explicitly has the
			// path absent (a tombstone), which must not be confused with "no
			// record": restoring a file deleted by a discarded turn is safe.
			expected = existing(latestEntry)
		}
		if !sameContent(actual, expected) {
			plan.Conflicts = append(plan.Conflicts, Conflict{Path: p, Reason: conflictReason(desired, actual)})
		}
		kind := ActionWrite
		if desired == nil {
			kind = ActionDelete
		}
		plan.Actions = append(plan.Actions, Action{Path: p, Kind: kind, Expected: desired, Current: actual})
	}
	return plan
}

func existing(e *FileEntry) *FileEntry {
	if e == nil || !e.Exists {
		return nil
	}
	return e
}

func sameContent(a, b *FileEntry) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if a.OID != b.OID || a.IsSymlink != b.IsSymlink || !sameLink(a, b) {
		return false
	}
	if !a.IsSymlink && a.Mode != b.Mode {
		return false
	}
	return true
}

func conflictReason(desired, actual *FileEntry) string {
	switch {
	case desired == nil:
		return "file was created or modified after the recorded turn; rewind would delete it"
	case actual == nil:
		return "file is missing; rewind would recreate it"
	default:
		return "file content was modified after the recorded turn"
	}
}

func uniqueSorted(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, p := range in {
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// FormatConflicts renders conflicts for CLI output.
func FormatConflicts(cs []Conflict) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, fmt.Sprintf("%s: %s", c.Path, c.Reason))
	}
	return out
}
