package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultAndEnsure(t *testing.T) {
	cfg := Default()
	if cfg.Retention.MaxRetainedTurns != 10 || cfg.Retention.CleanAfterDays != 30 {
		t.Fatalf("defaults: %+v", cfg)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	got, err := Ensure(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != SchemaVersion {
		t.Fatalf("schema=%d", got.SchemaVersion)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("perm=%o", info.Mode().Perm())
	}
	got2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Retention.MaxRetainedTurns != 10 {
		t.Fatalf("loaded=%+v", got2)
	}
}

func TestLoadMissingAndMalformed(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "none.json"))
	if err != nil || cfg.Retention.MaxRetainedTurns != 10 {
		t.Fatalf("missing load: %+v %v", cfg, err)
	}
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if cfg.Retention.MaxRetainedTurns != 10 {
		t.Fatalf("malformed load did not return defaults: %+v", cfg)
	}
}

func TestSaveNormalizesZeroValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, Config{}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SchemaVersion != SchemaVersion || cfg.Retention.MaxRetainedTurns != 10 || cfg.Limits.MaxPromptBytes == 0 {
		t.Fatalf("not normalized: %+v", cfg)
	}
}
