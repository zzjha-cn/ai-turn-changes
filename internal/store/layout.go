// Package store owns the .codex-rewind on-disk layout and its JSON records.
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/zzjha-cn/codex-rewind/internal/fsutil"
)

// DataDirName is the self-contained data directory created in a repository root.
const DataDirName = ".codex-rewind"

var (
	validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// Layout describes paths inside <root>/.codex-rewind.
type Layout struct {
	Root string
}

func New(root string) *Layout {
	abs, err := filepath.Abs(root)
	if err == nil {
		root = abs
	}
	return &Layout{Root: root}
}

func (l *Layout) DataDir() string    { return filepath.Join(l.Root, DataDirName) }
func (l *Layout) ConfigPath() string { return filepath.Join(l.DataDir(), "config.json") }
func (l *Layout) IgnorePath() string { return filepath.Join(l.DataDir(), ".gitignore") }
func (l *Layout) LockPath() string   { return filepath.Join(l.DataDir(), "lock") }
func (l *Layout) ObjectsDir() string { return filepath.Join(l.DataDir(), "objects") }
func (l *Layout) StatesDir() string  { return filepath.Join(l.DataDir(), "states") }
func (l *Layout) TurnDir() string    { return filepath.Join(l.DataDir(), "turn") }
func (l *Layout) RewindDir() string  { return filepath.Join(l.DataDir(), "rewind") }
func (l *Layout) LogsDir() string    { return filepath.Join(l.DataDir(), "logs") }
func (l *Layout) TmpDir() string     { return filepath.Join(l.DataDir(), "tmp") }

func (l *Layout) Exists() bool {
	st, err := os.Stat(l.DataDir())
	return err == nil && st.IsDir()
}

// Ensure creates the data layout with 0700 permissions and the self-ignoring
// .gitignore containing "*".
func (l *Layout) Ensure() error {
	dirs := []string{
		l.DataDir(), l.ObjectsDir(), l.StatesDir(), l.TurnDir(),
		l.RewindDir(), l.LogsDir(), l.TmpDir(),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0700); err != nil {
			return err
		}
		if err := os.Chmod(d, 0700); err != nil {
			return err
		}
	}
	ignore := filepath.Join(l.DataDir(), ".gitignore")
	if data, err := os.ReadFile(ignore); err != nil || string(data) != "*\n" {
		if err := fsutil.WriteFileAtomic(ignore, []byte("*\n"), 0600); err != nil {
			return err
		}
	}
	return nil
}

// SessionDir validates id and returns turn/<id>.
func (l *Layout) SessionDir(sessionID string) (string, error) {
	if err := ValidateID(sessionID); err != nil {
		return "", err
	}
	return filepath.Join(l.TurnDir(), sessionID), nil
}

func (l *Layout) SessionJSONPath(sessionID string) (string, error) {
	dir, err := l.SessionDir(sessionID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "session.json"), nil
}

func (l *Layout) QueuePath(sessionID string) (string, error) {
	dir, err := l.SessionDir(sessionID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "queue.json"), nil
}

func (l *Layout) ItemsDir(sessionID string) (string, error) {
	dir, err := l.SessionDir(sessionID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "items"), nil
}

func (l *Layout) EventsDir(sessionID string) (string, error) {
	dir, err := l.SessionDir(sessionID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "events"), nil
}

// EnsureSession creates the session directory tree.
func (l *Layout) EnsureSession(sessionID string) (string, error) {
	dir, err := l.SessionDir(sessionID)
	if err != nil {
		return "", err
	}
	for _, d := range []string{dir, filepath.Join(dir, "items"), filepath.Join(dir, "events")} {
		if err := os.MkdirAll(d, 0700); err != nil {
			return "", err
		}
		if err := os.Chmod(d, 0700); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// SessionIDs lists directory names under turn/, sorted.
func (l *Layout) SessionIDs() ([]string, error) {
	entries, err := os.ReadDir(l.TurnDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if err := ValidateID(e.Name()); err != nil {
			continue
		}
		ids = append(ids, e.Name())
	}
	sort.Strings(ids)
	return ids, nil
}

// ValidateID accepts UUIDs and other strict session/operation identifiers.
func ValidateID(id string) error {
	if id == "" {
		return errors.New("empty id")
	}
	if strings.Contains(id, "..") || strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("invalid id %q", id)
	}
	if !validID.MatchString(id) {
		return fmt.Errorf("invalid id %q", id)
	}
	return nil
}

// ItemRelPath returns items/<seq zero padded>-<turnID>.json relative to the
// session directory.
func ItemRelPath(seq int64, turnID string) (string, error) {
	if seq < 0 {
		return "", fmt.Errorf("invalid seq %d", seq)
	}
	if err := ValidateID(turnID); err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Join("items", fmt.Sprintf("%08d-%s.json", seq, turnID))), nil
}

// EventRelPath returns events/<seq zero padded>-<turnID>.jsonl.
func EventRelPath(seq int64, turnID string) (string, error) {
	if seq < 0 {
		return "", fmt.Errorf("invalid seq %d", seq)
	}
	if err := ValidateID(turnID); err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Join("events", fmt.Sprintf("%08d-%s.jsonl", seq, turnID))), nil
}

// IsDataRelPath reports whether a workspace-relative slash path lives inside
// the rewind data directory or .git.
func IsDataRelPath(rel string) bool {
	rel = filepath.ToSlash(rel)
	return rel == DataDirName || strings.HasPrefix(rel, DataDirName+"/") ||
		rel == ".git" || strings.HasPrefix(rel, ".git/")
}
