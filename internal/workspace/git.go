package workspace

import (
	"bytes"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// GitRoot returns the worktree root for dir, if dir is inside a Git worktree.
func GitRoot(dir string) (string, bool) {
	out, err := runGit(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", false
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", false
	}
	return root, true
}

// IsGitRepo reports whether dir is inside a Git worktree.
func IsGitRepo(dir string) bool {
	_, ok := GitRoot(dir)
	return ok
}

func runGit(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return out, nil
}

// ListFiles enumerates tracked and untracked non-ignored files, excluding the
// rewind data directory and .git.
func ListFiles(root string) ([]string, error) {
	out, err := runGit(root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	var files []string
	for _, raw := range bytes.Split(out, []byte{0}) {
		p := filepathSlash(string(raw))
		if p == "" {
			continue
		}
		if !ValidRelPath(p) {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		files = append(files, p)
	}
	sort.Strings(files)
	return files, nil
}

// StatusCandidates returns paths reported by git status --porcelain=v2, which
// is used to avoid re-hashing unchanged files.
func StatusCandidates(root string) (map[string]struct{}, error) {
	out, err := runGit(root, "status", "--porcelain=v2", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	entries := strings.Split(string(out), "\x00")
	out2 := make(map[string]struct{})
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if e == "" {
			continue
		}
		var p string
		switch e[0] {
		case '1':
			parts := strings.SplitN(e, " ", 9)
			if len(parts) == 9 {
				p = parts[8]
			}
		case '2':
			parts := strings.SplitN(e, " ", 10)
			if len(parts) == 10 {
				p = parts[9]
				// The next NUL-separated field is the original path.
				i++
			}
		case 'u':
			parts := strings.SplitN(e, " ", 11)
			if len(parts) == 11 {
				p = parts[10]
			}
		case '?', '!':
			p = strings.TrimPrefix(e[2:], " ")
		}
		p = filepathSlash(p)
		if p != "" && ValidRelPath(p) {
			out2[p] = struct{}{}
		}
	}
	return out2, nil
}

// CheckDataIgnored verifies that the rewind data directory does not show up in
// git status. It returns the offending status lines, if any.
func CheckDataIgnored(root string) ([]string, error) {
	out, err := runGit(root, "status", "--porcelain=v1", "--untracked-files=all", "--", DataDirName)
	if err != nil {
		return nil, nil
	}
	var lines []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

func filepathSlash(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}
