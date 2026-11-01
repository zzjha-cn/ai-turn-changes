package snapshot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zzjha-cn/codex-rewind/internal/fsutil"
	"github.com/zzjha-cn/codex-rewind/internal/store"
	"github.com/zzjha-cn/codex-rewind/internal/workspace"
)

// Apply executes a restore plan. It returns the paths it touched, including
// those applied before an error, so the caller can roll back.
func Apply(root string, cas *CAS, plan *Plan) ([]string, error) {
	if plan == nil {
		return nil, errors.New("nil plan")
	}
	var applied []string
	for _, a := range plan.Actions {
		if err := applyAction(root, cas, a); err != nil {
			return applied, fmt.Errorf("%s %s: %w", a.Kind, a.Path, err)
		}
		applied = append(applied, a.Path)
	}
	return applied, nil
}

func applyAction(root string, cas *CAS, a Action) error {
	switch a.Kind {
	case ActionWrite:
		if a.Expected == nil || !a.Expected.Exists {
			return errors.New("write action has no target entry")
		}
		return RestoreEntry(root, cas, a.Expected)
	case ActionDelete:
		return DeleteEntry(root, a.Path)
	default:
		return fmt.Errorf("unknown action %q", a.Kind)
	}
}

// RestorePaths writes or deletes paths to match target. It is used both for
// the main restore and for safety-state rollback.
func RestorePaths(root string, cas *CAS, target *State, paths []string) ([]string, error) {
	targetMap := map[string]*FileEntry{}
	if target != nil {
		targetMap = target.Map()
	}
	var applied []string
	for _, p := range uniqueSorted(paths) {
		desired := existing(targetMap[p])
		var err error
		if desired == nil {
			err = DeleteEntry(root, p)
		} else {
			err = RestoreEntry(root, cas, desired)
		}
		if err != nil {
			return applied, fmt.Errorf("restore %s: %w", p, err)
		}
		applied = append(applied, p)
	}
	return applied, nil
}

// RestoreEntry recreates one file, symlink and mode from CAS.
func RestoreEntry(root string, cas *CAS, e *FileEntry) error {
	if e == nil || !e.Exists {
		return errors.New("entry does not exist")
	}
	if !workspace.ValidRelPath(e.Path) {
		return fmt.Errorf("invalid path %q", e.Path)
	}
	abs, err := workspace.AbsWithin(root, e.Path)
	if err != nil {
		return err
	}
	if err := workspace.EnsureSafeParent(root, e.Path); err != nil {
		return err
	}
	st, statErr := os.Lstat(abs)
	if statErr == nil && st.IsDir() {
		return fmt.Errorf("target is a directory: %s", e.Path)
	}
	if e.IsSymlink {
		target := ""
		if e.LinkTarget != nil {
			target = *e.LinkTarget
		}
		if statErr == nil {
			if err := os.Remove(abs); err != nil {
				return err
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		return os.Symlink(target, abs)
	}
	if cas == nil {
		return errors.New("nil CAS")
	}
	data, err := cas.Get(e.OID)
	if err != nil {
		return err
	}
	mode := os.FileMode(e.Mode) & 0777
	if mode == 0 {
		mode = 0644
	}
	if err := fsutil.WriteFileAtomic(abs, data, mode); err != nil {
		return err
	}
	return os.Chmod(abs, mode)
}

// DeleteEntry removes a file and any empty parent directories up to root.
func DeleteEntry(root, rel string) error {
	if !workspace.ValidRelPath(rel) {
		return fmt.Errorf("invalid path %q", rel)
	}
	abs, err := workspace.AbsWithin(root, rel)
	if err != nil {
		return err
	}
	st, err := os.Lstat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if st.IsDir() {
		return fmt.Errorf("refusing to delete directory %s", rel)
	}
	if err := os.Remove(abs); err != nil {
		return err
	}
	workspace.CleanEmptyParents(root, filepath.Dir(abs))
	return nil
}

// VerifyPaths rescans paths and reports any difference from target.
func VerifyPaths(l *store.Layout, cas *CAS, target *State, paths []string) ([]Conflict, error) {
	current, err := CapturePaths(l, cas, paths, CaptureOptions{SkipObjects: true, NoPersist: true})
	if err != nil {
		return nil, err
	}
	targetMap := map[string]*FileEntry{}
	if target != nil {
		targetMap = target.Map()
	}
	currentMap := current.Map()
	var conflicts []Conflict
	for _, p := range uniqueSorted(paths) {
		desired := existing(targetMap[p])
		actual := existing(currentMap[p])
		if sameContent(desired, actual) {
			continue
		}
		conflicts = append(conflicts, Conflict{Path: p, Reason: verifyReason(desired, actual)})
	}
	return conflicts, nil
}

func verifyReason(desired, actual *FileEntry) string {
	switch {
	case desired == nil && actual != nil:
		return "file still exists after restore"
	case desired != nil && actual == nil:
		return "file is missing after restore"
	default:
		return "file does not match target state after restore"
	}
}
