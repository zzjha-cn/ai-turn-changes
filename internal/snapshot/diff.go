package snapshot

import (
	"sort"
	"strings"

	"github.com/zzjha-cn/codex-rewind/internal/store"
	"github.com/zzjha-cn/codex-rewind/internal/workspace"
)

const maxExactDiffLines = 2000

// Diff compares two manifests and produces turn file changes with line stats.
func Diff(oldState, newState *State, cas *CAS) []store.FileChange {
	oldMap := map[string]*FileEntry{}
	newMap := map[string]*FileEntry{}
	if oldState != nil {
		oldMap = oldState.Map()
	}
	if newState != nil {
		newMap = newState.Map()
	}
	seen := map[string]struct{}{}
	for p := range oldMap {
		seen[p] = struct{}{}
	}
	for p := range newMap {
		seen[p] = struct{}{}
	}
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var changes []store.FileChange
	for _, p := range paths {
		oe, ne := oldMap[p], newMap[p]
		oldExists := oe != nil && oe.Exists
		newExists := ne != nil && ne.Exists
		if !oldExists && !newExists {
			continue
		}
		var status string
		switch {
		case !oldExists && newExists:
			status = "added"
		case oldExists && !newExists:
			status = "deleted"
		default:
			if oe.OID == ne.OID && oe.IsSymlink == ne.IsSymlink && sameLink(oe, ne) && oe.Mode == ne.Mode {
				continue
			}
			status = "modified"
		}
		ch := store.FileChange{Path: p, Status: status}
		if oldExists {
			ch.PreviousOID = oe.OID
		}
		if newExists {
			ch.CurrentOID = ne.OID
		}
		if newExists {
			ch.IsBinary = ne.IsBinary
		} else if oldExists {
			ch.IsBinary = oe.IsBinary
		}
		applyStats(&ch, oe, ne, cas)
		changes = append(changes, ch)
	}
	return changes
}

func sameLink(a, b *FileEntry) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.LinkTarget == nil || b.LinkTarget == nil {
		return a.LinkTarget == nil && b.LinkTarget == nil
	}
	return *a.LinkTarget == *b.LinkTarget
}

func applyStats(ch *store.FileChange, oe, ne *FileEntry, cas *CAS) {
	if ch.IsBinary || cas == nil {
		return
	}
	if oe != nil && oe.IsSymlink || ne != nil && ne.IsSymlink {
		return
	}
	switch ch.Status {
	case "added":
		if data, err := cas.bytesUnsafe(ne.OID); err == nil {
			ch.Insertions = workspace.CountLines(data)
		}
	case "deleted":
		if data, err := cas.bytesUnsafe(oe.OID); err == nil {
			ch.Deletions = workspace.CountLines(data)
		}
	case "modified":
		if oe.OID == ne.OID {
			return
		}
		oldData, err1 := cas.bytesUnsafe(oe.OID)
		newData, err2 := cas.bytesUnsafe(ne.OID)
		if err1 == nil && err2 == nil {
			ch.Insertions, ch.Deletions = LineDiff(oldData, newData)
		}
	}
}

// LineDiff returns inserted and deleted line counts.
func LineDiff(oldData, newData []byte) (insertions, deletions int) {
	a := splitLines(oldData)
	b := splitLines(newData)
	if len(a) > maxExactDiffLines || len(b) > maxExactDiffLines {
		return len(b), len(a)
	}
	lcs := lcsLen(a, b)
	return len(b) - lcs, len(a) - lcs
}

func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func lcsLen(a, b []string) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				cur[j] = prev[j-1] + 1
			} else if prev[j] >= cur[j-1] {
				cur[j] = prev[j]
			} else {
				cur[j] = cur[j-1]
			}
		}
		prev, cur = cur, prev
		for j := range cur {
			cur[j] = 0
		}
	}
	return prev[len(b)]
}
