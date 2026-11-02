package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/zzjha-cn/codex-rewind/internal/journal"
)

const RewindOpSchemaVersion = 1

// RewindConflict records one file that could not be restored safely.
type RewindConflict struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// RewindOp mirrors rewind/<op-id>.json.
type RewindOp struct {
	SchemaVersion   int              `json:"schemaVersion"`
	OpID            string           `json:"opId"`
	SourceSessionID string           `json:"sourceSessionId"`
	SourceThreadID  string           `json:"sourceThreadId"`
	TargetTurnID    string           `json:"targetTurnId"`
	Mode            string           `json:"mode"`
	Phase           journal.Phase    `json:"phase"`
	SafetyStateID   *string          `json:"safetyStateId"`
	TargetStateID   string           `json:"targetStateId"`
	NewThreadID     *string          `json:"newThreadId"`
	NewThreadName   string           `json:"newThreadName,omitempty"`
	Conflicts       []RewindConflict `json:"conflicts"`
	CreatedAt       time.Time        `json:"createdAt"`
	UpdatedAt       time.Time        `json:"updatedAt"`
	Error           *string          `json:"error"`
}

// Rewind modes.
const (
	ModeBefore  = "before"
	ModeThrough = "through"
)

func NewRewindOp(opID, sessionID, threadID, turnID, mode, targetStateID string, now time.Time) *RewindOp {
	return &RewindOp{
		SchemaVersion:   RewindOpSchemaVersion,
		OpID:            opID,
		SourceSessionID: sessionID,
		SourceThreadID:  threadID,
		TargetTurnID:    turnID,
		Mode:            mode,
		Phase:           journal.Planned,
		TargetStateID:   targetStateID,
		Conflicts:       []RewindConflict{},
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func (o *RewindOp) SetPhase(p journal.Phase, now time.Time) {
	o.Phase = p
	o.UpdatedAt = now
}

func (o *RewindOp) SetError(err string, now time.Time) {
	if err == "" {
		o.Error = nil
	} else {
		e := err
		o.Error = &e
	}
	o.UpdatedAt = now
}

func RewindOpPath(l *Layout, opID string) (string, error) {
	if err := ValidateID(opID); err != nil {
		return "", err
	}
	return filepath.Join(l.RewindDir(), opID+".json"), nil
}

func LoadRewindOp(l *Layout, opID string) (*RewindOp, error) {
	path, err := RewindOpPath(l, opID)
	if err != nil {
		return nil, err
	}
	var op RewindOp
	if err := ReadJSON(path, &op); err != nil {
		return nil, err
	}
	if op.SchemaVersion != RewindOpSchemaVersion {
		return nil, fmt.Errorf("rewind op %s: unsupported schemaVersion %d", opID, op.SchemaVersion)
	}
	if op.OpID != opID {
		return nil, fmt.Errorf("rewind op %s: opId mismatch %q", opID, op.OpID)
	}
	if op.Conflicts == nil {
		op.Conflicts = []RewindConflict{}
	}
	return &op, nil
}

func SaveRewindOp(l *Layout, op *RewindOp) error {
	if op == nil {
		return fmt.Errorf("nil rewind op")
	}
	if op.SchemaVersion == 0 {
		op.SchemaVersion = RewindOpSchemaVersion
	}
	if op.Conflicts == nil {
		op.Conflicts = []RewindConflict{}
	}
	path, err := RewindOpPath(l, op.OpID)
	if err != nil {
		return err
	}
	return WriteJSON(path, op, 0600)
}

// ListRewindOps returns all readable operations, newest first.
func ListRewindOps(l *Layout) ([]*RewindOp, []error) {
	entries, err := os.ReadDir(l.RewindDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []error{err}
	}
	var ops []*RewindOp
	var errs []error
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id := e.Name()[:len(e.Name())-len(".json")]
		op, err := LoadRewindOp(l, id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		ops = append(ops, op)
	}
	sort.SliceStable(ops, func(i, j int) bool { return ops[i].CreatedAt.After(ops[j].CreatedAt) })
	return ops, errs
}

// IncompleteOps returns operations that still need work or review.
func IncompleteOps(l *Layout) ([]*RewindOp, error) {
	ops, errs := ListRewindOps(l)
	if len(errs) > 0 {
		return nil, errs[0]
	}
	var out []*RewindOp
	for _, op := range ops {
		if op.Phase.Incomplete() {
			out = append(out, op)
		}
	}
	return out, nil
}

// FindReusableOp finds an incomplete op for the same target that already has a
// forked thread, so a retry never forks twice.
func FindReusableOp(l *Layout, sessionID, turnID, mode string) (*RewindOp, error) {
	ops, err := IncompleteOps(l)
	if err != nil {
		return nil, err
	}
	for _, op := range ops {
		if op.SourceSessionID != sessionID || op.TargetTurnID != turnID || op.Mode != mode {
			continue
		}
		if op.NewThreadID != nil && *op.NewThreadID != "" {
			return op, nil
		}
		return nil, nil
	}
	return nil, nil
}
