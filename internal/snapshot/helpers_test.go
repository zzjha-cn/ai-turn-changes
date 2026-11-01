package snapshot

import (
	"os"
	"path/filepath"
)

func writeFile(root, rel, content string) error {
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0644)
}

func chmod(root, rel string, mode os.FileMode) error {
	return os.Chmod(filepath.Join(root, filepath.FromSlash(rel)), mode)
}

func removeFile(root, rel string) error {
	return os.Remove(filepath.Join(root, filepath.FromSlash(rel)))
}
