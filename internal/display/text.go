// Package display turns prompts, assistant messages and file changes into the
// bounded, human readable text stored in turn queues.
package display

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// StripControl removes control characters while preserving newlines and tabs.
// It normalises CRLF and CR line endings to LF.
func StripControl(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// TruncateUTF8 truncates s to at most maxBytes without splitting a rune.
func TruncateUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 || s == "" {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	n := maxBytes
	for n > 0 && !utf8.ValidString(s[:n]) {
		n--
	}
	return s[:n]
}

// FirstLine returns the first non-empty line after control character stripping.
func FirstLine(s string) string {
	s = StripControl(s)
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

// Title builds a short display title from a prompt.
func Title(prompt string) string {
	t := FirstLine(prompt)
	if t == "" {
		return "(no prompt)"
	}
	return TruncateUTF8(t, 80)
}

// OneLine collapses whitespace runs into single spaces and strips control chars.
func OneLine(s string) string {
	s = StripControl(s)
	return strings.Join(strings.Fields(s), " ")
}

// Limits are the configured display bounds.
type Limits struct {
	MaxPromptBytes    int
	MaxAssistantBytes int
	MaxFiles          int
	MaxTextChars      int
}

// Change is the subset of a file change needed for human readable summaries.
type Change struct {
	Path       string
	Status     string
	Insertions int
	Deletions  int
	IsBinary   bool
}

// Display is embedded in queue.json for terminal selection.
type Display struct {
	Title         string   `json:"title"`
	UserText      string   `json:"userText"`
	AssistantText string   `json:"assistantText"`
	FileSummary   string   `json:"fileSummary"`
	Files         []string `json:"files"`
}

// Build creates a bounded Display. Prompt and assistant text are assumed to be
// already capped at the storage limits by the caller; they are additionally
// capped to MaxTextChars here for the selection UI.
func Build(prompt, assistant string, changes []Change, limits Limits) Display {
	maxText := limits.MaxTextChars
	if maxText <= 0 {
		maxText = 500
	}
	maxPrompt := limits.MaxPromptBytes
	if maxPrompt <= 0 || maxPrompt > maxText {
		maxPrompt = maxText
	}
	maxAssistant := limits.MaxAssistantBytes
	if maxAssistant <= 0 || maxAssistant > maxText {
		maxAssistant = maxText
	}
	maxFiles := limits.MaxFiles
	if maxFiles <= 0 {
		maxFiles = 10
	}

	d := Display{
		Title:         Title(prompt),
		UserText:      TruncateUTF8(StripControl(prompt), maxPrompt),
		AssistantText: TruncateUTF8(StripControl(assistant), maxAssistant),
		Files:         make([]string, 0, min(len(changes), maxFiles)),
	}
	insertions, deletions := 0, 0
	for i, c := range changes {
		insertions += c.Insertions
		deletions += c.Deletions
		if i < maxFiles {
			d.Files = append(d.Files, c.Path)
		}
	}
	d.FileSummary = FileSummary(len(changes), insertions, deletions)
	return d
}

// FileSummary renders "N files, +X -Y" with singular/plural handling.
func FileSummary(files, insertions, deletions int) string {
	noun := "files"
	if files == 1 {
		noun = "file"
	}
	return pluralInt(files) + " " + noun + ", +" + strconv.Itoa(insertions) + " -" + strconv.Itoa(deletions)
}

func pluralInt(n int) string { return strconv.Itoa(n) }

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
