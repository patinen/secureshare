// Package uploads owns a private, non-recursively cleaned upload spool.
package uploads

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

var pattern = regexp.MustCompile(`^secureshare-upload-[0-9a-f]{32}$`)

func Directory(dir string) string {
	if dir == "" {
		return filepath.Join(os.TempDir(), "secureshare-uploads")
	}
	return dir
}

// Pin the directory handle and reject a symlink or permissive directory.
// Root operations stay confined even if the directory is subsequently renamed.
func open(dir string) (*os.Root, error) {
	dir = Directory(dir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, errors.New("upload directory unavailable")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || !owned(info) {
		return nil, errors.New("upload directory must be private")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, errors.New("upload directory unavailable")
	}
	pinned, err := root.Stat(".")
	if err != nil || !os.SameFile(info, pinned) {
		root.Close()
		return nil, errors.New("upload directory changed")
	}
	return root, nil
}

func Ensure(dir string) error {
	root, err := open(dir)
	if root != nil {
		root.Close()
	}
	return err
}

func Create(dir string) (*os.File, func(), error) {
	root, err := open(dir)
	if err != nil {
		return nil, nil, err
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		root.Close()
		return nil, nil, err
	}
	name := "secureshare-upload-" + hex.EncodeToString(random[:])
	f, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		root.Close()
		return nil, nil, errors.New("upload spool unavailable")
	}
	return f, func() { f.Close(); _ = root.Remove(name); root.Close() }, nil
}

func Sweep(dir string, before time.Time) (removed, failures int, err error) {
	root, err := open(dir)
	if err != nil {
		return 0, 0, err
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return 0, 0, errors.New("upload spool unavailable")
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return 0, 0, errors.New("upload spool unavailable")
	}
	for _, entry := range entries {
		if !pattern.MatchString(entry.Name()) {
			continue
		}
		info, err := root.Lstat(entry.Name())
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			failures++
			continue
		}
		if !info.Mode().IsRegular() || !info.ModTime().Before(before) {
			continue
		}
		// Remove unlinks this entry; it never follows a replacement symlink.
		if err = root.Remove(entry.Name()); err == nil {
			removed++
		} else if !errors.Is(err, os.ErrNotExist) {
			failures++
		}
	}
	return removed, failures, nil
}
