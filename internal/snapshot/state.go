package snapshot

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/zzjha-cn/codex-rewind/internal/store"
	"github.com/zzjha-cn/codex-rewind/internal/workspace"
)

const StateSchemaVersion = 1

// FileEntry is one path in a workspace state manifest.
type FileEntry struct {
	Path          string  `json:"path"`
	Exists        bool    `json:"exists"`
	OID           string  `json:"oid"`
	Mode          uint32  `json:"mode"`
	IsSymlink     bool    `json:"isSymlink"`
	LinkTarget    *string `json:"linkTarget"`
	IsBinary      bool    `json:"isBinary"`
	Size          int64   `json:"size"`
	MtimeUnixNano int64   `json:"mtimeUnixNano,omitempty"`
	Unstable      bool    `json:"unstable,omitempty"`
}

// State mirrors states/<id>.json.
type State struct {
	SchemaVersion int         `json:"schemaVersion"`
	StateID       string      `json:"stateId"`
	ParentStateID *string     `json:"parentStateId"`
	CreatedAt     time.Time   `json:"createdAt"`
	Files         []FileEntry `json:"files"`
}

func (s *State) Map() map[string]*FileEntry {
	m := make(map[string]*FileEntry, len(s.Files))
	for i := range s.Files {
		m[s.Files[i].Path] = &s.Files[i]
	}
	return m
}

func (s *State) Entry(path string) *FileEntry {
	for i := range s.Files {
		if s.Files[i].Path == path {
			return &s.Files[i]
		}
	}
	return nil
}

func (s *State) Paths() []string {
	out := make([]string, 0, len(s.Files))
	for _, f := range s.Files {
		out = append(out, f.Path)
	}
	return out
}

// OIDs returns all object ids referenced by the state.
func (s *State) OIDs() []string {
	out := make([]string, 0, len(s.Files))
	for _, f := range s.Files {
		if f.Exists && f.OID != "" && !f.IsSymlink {
			out = append(out, f.OID)
		}
	}
	return out
}

type CaptureOptions struct {
	// Previous lets Capture reuse object ids for unchanged files.
	Previous *State
	// Now overrides the clock (tests).
	Now func() time.Time
	// SkipObjects computes hashes without writing CAS objects (dry-run).
	SkipObjects bool
	// NoPersist avoids writing states/<id>.json (verification scans).
	NoPersist bool
	// Rand is used for state ids (tests); defaults to crypto/rand.
	Rand io.Reader
}

// Capture scans the worktree and writes a state manifest.
func Capture(l *store.Layout, cas *CAS, opts CaptureOptions) (*State, error) {
	paths, err := workspace.ListFiles(l.Root)
	if err != nil {
		return nil, err
	}
	candidates := map[string]struct{}{}
	if opts.Previous != nil {
		if c, err := workspace.StatusCandidates(l.Root); err == nil {
			candidates = c
		}
	}
	return capturePaths(l, cas, paths, opts, candidates)
}

// CapturePaths scans just the given paths without persisting a manifest.
func CapturePaths(l *store.Layout, cas *CAS, paths []string, opts CaptureOptions) (*State, error) {
	opts.NoPersist = true
	return capturePaths(l, cas, paths, opts, nil)
}

func capturePaths(l *store.Layout, cas *CAS, paths []string, opts CaptureOptions, candidates map[string]struct{}) (*State, error) {
	now := time.Now()
	if opts.Now != nil {
		now = opts.Now()
	}
	prev := map[string]*FileEntry{}
	if opts.Previous != nil {
		prev = opts.Previous.Map()
	}
	if cas == nil {
		return nil, errors.New("nil CAS")
	}
	entries := make([]FileEntry, 0, len(paths))
	seen := map[string]struct{}{}
	for _, rel := range paths {
		if !workspace.ValidRelPath(rel) {
			continue
		}
		if _, ok := seen[rel]; ok {
			continue
		}
		seen[rel] = struct{}{}
		_, candidate := candidates[rel]
		entry, include, err := captureEntry(l.Root, rel, prev[rel], candidate, cas, opts.SkipObjects)
		if err != nil {
			return nil, err
		}
		if !include {
			continue
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	st := &State{
		SchemaVersion: StateSchemaVersion,
		CreatedAt:     now,
		Files:         entries,
	}
	if opts.Previous != nil && opts.Previous.StateID != "" {
		parent := opts.Previous.StateID
		st.ParentStateID = &parent
	}
	st.StateID = NewStateID(now, opts.Rand)
	if !opts.NoPersist {
		if err := SaveState(l, st); err != nil {
			return nil, err
		}
	}
	return st, nil
}

func captureEntry(root, rel string, prev *FileEntry, candidate bool, cas *CAS, skipObjects bool) (FileEntry, bool, error) {
	abs, err := workspace.AbsWithin(root, rel)
	if err != nil {
		return FileEntry{}, false, err
	}
	st, err := os.Lstat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return FileEntry{Path: rel, Exists: false}, true, nil
		}
		return FileEntry{}, false, err
	}
	entry := FileEntry{
		Path:          rel,
		Exists:        true,
		Mode:          uint32(st.Mode().Perm()),
		Size:          st.Size(),
		MtimeUnixNano: st.ModTime().UnixNano(),
	}
	if st.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(abs)
		if err != nil {
			return FileEntry{}, false, err
		}
		entry.IsSymlink = true
		entry.LinkTarget = &target
		oid := workspace.HashOID([]byte("symlink:" + target))
		if !skipObjects {
			if oid, err = cas.PutString("symlink:" + target); err != nil {
				return FileEntry{}, false, err
			}
		}
		entry.OID = oid
		entry.Size = int64(len(target))
		return entry, true, nil
	}
	if !st.Mode().IsRegular() {
		// Directories (submodules), sockets, FIFOs and devices are not
		// restorable and must not be recorded as absent.
		return FileEntry{}, false, nil
	}
	entry.IsBinary = prev != nil && prev.IsBinary
	if prev != nil && prev.Exists && !prev.IsSymlink && prev.OID != "" && !candidate &&
		prev.Size == st.Size() && prev.MtimeUnixNano == entry.MtimeUnixNano && cas.Has(prev.OID) {
		entry.OID = prev.OID
		entry.IsBinary = prev.IsBinary
		return entry, true, nil
	}
	data, unstable, err := readStable(abs, st)
	if err != nil {
		return FileEntry{}, false, err
	}
	entry.Unstable = unstable
	entry.IsBinary = workspace.IsBinary(data)
	entry.Size = int64(len(data))
	if skipObjects {
		entry.OID = workspace.HashOID(data)
	} else {
		oid, err := cas.Put(data)
		if err != nil {
			return FileEntry{}, false, err
		}
		entry.OID = oid
	}
	return entry, true, nil
}

// readStable reads a file and re-checks its stat, retrying once if the file
// changed mid-read.
func readStable(abs string, before os.FileInfo) ([]byte, bool, error) {
	var data []byte
	for attempt := 0; attempt < 2; attempt++ {
		b, err := os.ReadFile(abs)
		if err != nil {
			return nil, false, err
		}
		after, err := os.Lstat(abs)
		if err != nil {
			return nil, false, err
		}
		data = b
		if after.Size() == before.Size() && after.ModTime().Equal(before.ModTime()) {
			return data, false, nil
		}
		before = after
	}
	return data, true, nil
}

// NewStateID returns a sortable unique state id.
func NewStateID(now time.Time, r io.Reader) string {
	buf := make([]byte, 4)
	if r == nil {
		r = rand.Reader
	}
	if _, err := io.ReadFull(r, buf); err != nil {
		return fmt.Sprintf("state-%d", now.UnixNano())
	}
	return fmt.Sprintf("state-%d-%s", now.UnixNano(), hex.EncodeToString(buf))
}

func StatePath(l *store.Layout, stateID string) (string, error) {
	if err := store.ValidateID(stateID); err != nil {
		return "", err
	}
	return filepath.Join(l.StatesDir(), stateID+".json"), nil
}

func LoadState(l *store.Layout, stateID string) (*State, error) {
	path, err := StatePath(l, stateID)
	if err != nil {
		return nil, err
	}
	var st State
	if err := store.ReadJSON(path, &st); err != nil {
		return nil, err
	}
	if st.SchemaVersion != StateSchemaVersion {
		return nil, fmt.Errorf("state %s: unsupported schemaVersion %d", stateID, st.SchemaVersion)
	}
	if st.StateID != stateID {
		return nil, fmt.Errorf("state %s: stateId mismatch %q", stateID, st.StateID)
	}
	if st.Files == nil {
		st.Files = []FileEntry{}
	}
	return &st, nil
}

func SaveState(l *store.Layout, st *State) error {
	if st == nil {
		return errors.New("nil state")
	}
	if st.SchemaVersion == 0 {
		st.SchemaVersion = StateSchemaVersion
	}
	if st.Files == nil {
		st.Files = []FileEntry{}
	}
	path, err := StatePath(l, st.StateID)
	if err != nil {
		return err
	}
	return store.WriteJSON(path, st, 0600)
}

// ListStateIDs returns state ids present on disk.
func ListStateIDs(l *store.Layout) ([]string, error) {
	entries, err := os.ReadDir(l.StatesDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id := e.Name()[:len(e.Name())-len(".json")]
		if store.ValidateID(id) == nil {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}
