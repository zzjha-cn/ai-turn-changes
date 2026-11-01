package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLockMetadataIsVersioned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	lock, err := TryAcquire(path, "test")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["schemaVersion"] != float64(LockSchemaVersion) {
		t.Fatalf("schemaVersion=%v in %s", raw["schemaVersion"], data)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	info, err := LockInfoPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.ReleasedAt == nil {
		t.Fatal("release marker was not recorded")
	}
}
