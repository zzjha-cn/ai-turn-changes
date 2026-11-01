//go:build windows

package store

import (
	"errors"
	"os"
)

var errWouldBlock = errors.New("file lock would block")

func flockExclusive(_ *os.File) error {
	return errors.New("repository locking is not implemented on windows yet")
}

func funlock(_ *os.File) error { return nil }

func processAlive(_ int) bool { return false }
