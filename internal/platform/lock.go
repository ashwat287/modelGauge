package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Lock is a per-user host lock, independent of the chosen results directory.
func Lock(name string) (func(), error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	directory := filepath.Join(home, ".local", "share", "modelgauge", "locks")
	return LockFile(filepath.Join(directory, name+".lock"))
}

// LockFile also supports controller journals on their persistent volume.
func LockFile(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("another ModelGauge process owns %s: %w", filepath.Base(path), err)
	}
	return func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() }, nil
}
