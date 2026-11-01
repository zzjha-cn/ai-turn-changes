package display

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStripControl(t *testing.T) {
	in := "a\r\nb\rc\x00d\x1be\tf"
	got := StripControl(in)
	if strings.ContainsAny(got, "\x00\x1b\r") {
		t.Fatalf("control characters remain: %q", got)
	}
	if !strings.Contains(got, "\n") || !strings.Contains(got, "\t") {
		t.Fatalf("newline/tab should be preserved: %q", got)
	}
	if got != "a\nb\ncd"+"e\tf" {
		t.Fatalf("got %q", got)
	}
}

func TestTruncateUTF8DoesNotSplitRune(t *testing.T) {
	s := "héllo世界"
	for max := 0; max <= len(s)+2; max++ {
		got := TruncateUTF8(s, max)
		if !utf8.ValidString(got) {
			t.Fatalf("max=%d produced invalid UTF-8: %q", max, got)
		}
		if len(got) > max {
			t.Fatalf("max=%d got %d bytes", max, len(got))
		}
		if max >= len(s) && got != s {
			t.Fatalf("max=%d truncated full string", max)
		}
	}
}

func TestTitleAndOneLine(t *testing.T) {
	if got := Title("\n\n  hello   world\nsecond"); got != "hello   world" {
		t.Fatalf("Title=%q", got)
	}
	if got := Title("  \n\t"); got != "(no prompt)" {
		t.Fatalf("empty title=%q", got)
	}
	if got := OneLine("a\n\n b\tc"); got != "a b c" {
		t.Fatalf("OneLine=%q", got)
	}
}

func TestBuildBoundsDisplay(t *testing.T) {
	changes := []Change{
		{Path: "a", Insertions: 2, Deletions: 1},
		{Path: "b", Insertions: 3},
		{Path: "c"},
	}
	d := Build(strings.Repeat("p", 100), strings.Repeat("a", 100), changes, Limits{
		MaxPromptBytes: 10, MaxAssistantBytes: 20, MaxFiles: 2, MaxTextChars: 30,
	})
	if len(d.UserText) > 10 || len(d.AssistantText) > 20 {
		t.Fatalf("text not bounded: %+v", d)
	}
	if len(d.Files) != 2 || d.Files[0] != "a" || d.Files[1] != "b" {
		t.Fatalf("files=%v", d.Files)
	}
	if d.FileSummary != "3 files, +5 -1" {
		t.Fatalf("summary=%q", d.FileSummary)
	}
	if d.Title == "" {
		t.Fatal("title empty")
	}
}

func TestFileSummarySingular(t *testing.T) {
	if got := FileSummary(1, 0, 1); got != "1 file, +0 -1" {
		t.Fatalf("got %q", got)
	}
}
