package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrLocked is returned when another process holds the repository lock.
var ErrLocked = errors.New("repository lock is held")

// Lock is an advisory exclusive lock on .codex-rewind/lock.
type Lock struct {
	f    *os.File
	path string
}

// LockSchemaVersion versions the lock metadata written to .codex-rewind/lock.
const LockSchemaVersion = 1

type lockInfo struct {
	SchemaVersion int        `json:"schemaVersion"`
	PID           int        `json:"pid"`
	StartedAt     time.Time  `json:"startedAt"`
	Command       string     `json:"command,omitempty"`
	ReleasedAt    *time.Time `json:"releasedAt,omitempty"`
}

// TryAcquire takes the lock without blocking.
func TryAcquire(path string, command string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	if err := flockExclusive(f); err != nil {
		f.Close()
		if errors.Is(err, errWouldBlock) {
			return nil, ErrLocked
		}
		return nil, err
	}
	info := lockInfo{SchemaVersion: LockSchemaVersion, PID: os.Getpid(), StartedAt: time.Now(), Command: command}
	if data, err := json.Marshal(info); err == nil {
		_ = f.Truncate(0)
		_, _ = f.Seek(0, 0)
		_, _ = f.Write(append(data, '\n'))
		_ = f.Sync()
	}
	return &Lock{f: f, path: path}, nil
}

// Release marks the lock as released, then unlocks and closes the file. The
// lock file itself is never removed to avoid a create/unlink race between
// processes.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	if info, err := LockInfoPath(l.path); err == nil {
		now := time.Now()
		info.ReleasedAt = &now
		if data, err := json.Marshal(info); err == nil {
			_ = l.f.Truncate(0)
			_, _ = l.f.Seek(0, 0)
			_, _ = l.f.Write(append(data, '\n'))
			_ = l.f.Sync()
		}
	}
	err := funlock(l.f)
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	l.f = nil
	return err
}

func (l *Lock) Path() string { return l.path }

// LockInfoPath returns the recorded holder information, if readable.
func LockInfoPath(path string) (lockInfo, error) {
	var info lockInfo
	data, err := os.ReadFile(path)
	if err != nil {
		return info, err
	}
	err = json.Unmarshal(data, &info)
	return info, err
}

// Stale reports whether a lock file records a dead holder and no process
// currently holds the flock. The lock file itself intentionally survives normal
// runs, so mere existence is not staleness.
func Stale(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if err := flockExclusive(f); err != nil {
		if errors.Is(err, errWouldBlock) {
			return false, nil
		}
		return false, err
	}
	_ = funlock(f)
	info, ierr := LockInfoPath(path)
	if ierr != nil || info.PID <= 0 {
		return true, nil
	}
	if info.ReleasedAt != nil {
		return false, nil
	}
	return !processAlive(info.PID), nil
}

func dirOf(path string) string {
	return filepath.Dir(path)
}

// String helps diagnostics.
func (l *Lock) String() string {
	if l == nil {
		return "lock(nil)"
	}
	return fmt.Sprintf("lock(%s)", l.path)
}
