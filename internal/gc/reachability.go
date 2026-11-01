// Package gc computes state/object reachability and removes orphans.
package gc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zzjha-cn/codex-rewind/internal/display"
	"github.com/zzjha-cn/codex-rewind/internal/snapshot"
	"github.com/zzjha-cn/codex-rewind/internal/store"
	"github.com/zzjha-cn/codex-rewind/internal/workspace"
)

// Options controls a collection run.
type Options struct {
	DryRun bool
	// ExcludeSessions treats these session ids as already deleted, used to
	// compute a dry-run clean plan.
	ExcludeSessions map[string]struct{}
}

// Result reports what was found and (unless DryRun) removed.
type Result struct {
	ReachableStates    int
	ReachableObjects   int
	UnreachableStates  []string
	UnreachableObjects []string
	DeletedStates      []string
	DeletedObjects     []string
	MissingStates      []string
	MissingObjects     []string
	BytesFreed         int64
	Errors             []error
	DryRun             bool
}

// Collect removes states not referenced by retained turns or incomplete rewind
// ops, then removes objects not referenced by reachable states.
func Collect(l *store.Layout, opts Options) (*Result, error) {
	res := &Result{DryRun: opts.DryRun}
	if !l.Exists() {
		return res, nil
	}
	reachableStates, err := collectReferencedStates(l, opts, res)
	if err != nil {
		return nil, err
	}
	res.ReachableStates = len(reachableStates)

	reachableOIDs := make(map[string]struct{})
	for id := range reachableStates {
		st, err := snapshot.LoadState(l, id)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				res.MissingStates = append(res.MissingStates, id)
			} else {
				res.Errors = append(res.Errors, err)
			}
			continue
		}
		for _, f := range st.Files {
			if f.Exists && f.OID != "" {
				reachableOIDs[f.OID] = struct{}{}
			}
		}
	}
	res.ReachableObjects = len(reachableOIDs)

	stateIDs, err := snapshot.ListStateIDs(l)
	if err != nil {
		return nil, err
	}
	for _, id := range stateIDs {
		if _, ok := reachableStates[id]; ok {
			continue
		}
		res.UnreachableStates = append(res.UnreachableStates, id)
	}

	objectOIDs, sizes, err := listObjects(l)
	if err != nil {
		return nil, err
	}
	for _, oid := range objectOIDs {
		if _, ok := reachableOIDs[oid]; ok {
			continue
		}
		res.UnreachableObjects = append(res.UnreachableObjects, oid)
	}

	// Report missing objects only for referenced ones.
	for oid := range reachableOIDs {
		if _, ok := sizes[oid]; !ok {
			res.MissingObjects = append(res.MissingObjects, oid)
		}
	}
	sort.Strings(res.MissingObjects)
	sort.Strings(res.UnreachableStates)
	sort.Strings(res.UnreachableObjects)

	if opts.DryRun {
		return res, nil
	}

	for _, id := range res.UnreachableStates {
		path, err := snapshot.StatePath(l, id)
		if err != nil {
			res.Errors = append(res.Errors, err)
			continue
		}
		if err := os.Remove(path); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				res.Errors = append(res.Errors, err)
			}
			continue
		}
		res.DeletedStates = append(res.DeletedStates, id)
	}
	for _, oid := range res.UnreachableObjects {
		path := objectPath(l, oid)
		if path == "" {
			continue
		}
		size := sizes[oid]
		if err := os.Remove(path); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				res.Errors = append(res.Errors, err)
			}
			continue
		}
		res.DeletedObjects = append(res.DeletedObjects, oid)
		res.BytesFreed += size
	}
	return res, nil
}

func collectReferencedStates(l *store.Layout, opts Options, res *Result) (map[string]struct{}, error) {
	reachable := make(map[string]struct{})
	sessions, errs := store.LoadSessions(l)
	res.Errors = append(res.Errors, errs...)
	for _, s := range sessions {
		if _, skip := opts.ExcludeSessions[s.SessionID]; skip {
			continue
		}
		q, err := store.LoadQueueReadOnly(l, s.SessionID, 0, display.Limits{})
		if err != nil {
			res.Errors = append(res.Errors, err)
			continue
		}
		for _, t := range q.Turns {
			if t.BaselineStateID != "" {
				reachable[t.BaselineStateID] = struct{}{}
			}
			if t.FinalStateID != "" {
				reachable[t.FinalStateID] = struct{}{}
			}
		}
	}
	ops, errs := store.ListRewindOps(l)
	res.Errors = append(res.Errors, errs...)
	for _, op := range ops {
		if !op.Phase.Incomplete() {
			continue
		}
		if op.SafetyStateID != nil && *op.SafetyStateID != "" {
			reachable[*op.SafetyStateID] = struct{}{}
		}
		if op.TargetStateID != "" {
			reachable[op.TargetStateID] = struct{}{}
		}
	}
	return reachable, nil
}

func listObjects(l *store.Layout) ([]string, map[string]int64, error) {
	sizes := make(map[string]int64)
	var oids []string
	root := l.ObjectsDir()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 62 {
			return nil
		}
		oid := workspace.OID(parts[0] + parts[1])
		if _, err := workspace.ParseOID(oid); err != nil {
			return nil
		}
		if !isHex(parts[0]) || !isHex(parts[1]) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		oids = append(oids, oid)
		sizes[oid] = info.Size()
		return nil
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, sizes, nil
		}
		return nil, nil, err
	}
	sort.Strings(oids)
	return oids, sizes, nil
}

func objectPath(l *store.Layout, oid string) string {
	digest, err := workspace.ParseOID(oid)
	if err != nil {
		return ""
	}
	return filepath.Join(l.ObjectsDir(), digest[:2], digest[2:])
}

func isHex(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return s != ""
}

// String summarises a result for logs.
func (r *Result) String() string {
	return fmt.Sprintf("states=%d objects=%d unreachableStates=%d unreachableObjects=%d missingStates=%d missingObjects=%d bytesFreed=%d errors=%d",
		r.ReachableStates, r.ReachableObjects, len(r.UnreachableStates), len(r.UnreachableObjects),
		len(r.MissingStates), len(r.MissingObjects), r.BytesFreed, len(r.Errors))
}
