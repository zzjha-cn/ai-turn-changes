package workspace

import (
	"strings"
	"testing"
)

func TestHashOIDKnownVector(t *testing.T) {
	// SHA-256("abc")
	const want = "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := HashOID([]byte("abc")); got != want {
		t.Fatalf("HashOID(abc)=%s want %s", got, want)
	}
	digest, err := ParseOID(want)
	if err != nil || len(digest) != 64 {
		t.Fatalf("ParseOID: %q %v", digest, err)
	}
	for _, bad := range []string{"", "sha256:abc", "md5:abcd", "sha256:" + strings.Repeat("g", 64), "sha256:" + strings.Repeat("A", 64)} {
		if _, err := ParseOID(bad); err == nil {
			t.Errorf("ParseOID(%q) succeeded", bad)
		}
	}
}

func TestIsBinary(t *testing.T) {
	if IsBinary([]byte("plain text\n")) {
		t.Fatal("text marked binary")
	}
	if !IsBinary([]byte("a\x00b")) {
		t.Fatal("NUL byte not detected")
	}
	// NUL after the 8000-byte probe window is not detected.
	data := append([]byte(strings.Repeat("a", 9000)), 0)
	if IsBinary(data) {
		t.Fatal("NUL beyond probe window was detected")
	}
}

func TestCountLines(t *testing.T) {
	cases := map[string]int{"": 0, "a": 1, "a\n": 1, "a\nb": 2, "a\nb\n": 2, "\n": 1}
	for in, want := range cases {
		if got := CountLines([]byte(in)); got != want {
			t.Errorf("CountLines(%q)=%d want %d", in, got, want)
		}
	}
}
