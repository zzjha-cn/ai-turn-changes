package store

import (
	"os"

	"github.com/zzjha-cn/codex-rewind/internal/fsutil"
)

// Atomic helpers are re-exported so packages that already depend on store do
// not need a second import.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	return fsutil.WriteFileAtomic(path, data, perm)
}

func WriteJSON(path string, v any, perm os.FileMode) error {
	return fsutil.WriteJSON(path, v, perm)
}

func ReadJSON(path string, v any) error {
	return fsutil.ReadJSON(path, v)
}

func ReadJSONIfExists(path string, v any) (bool, error) {
	return fsutil.ReadJSONIfExists(path, v)
}
