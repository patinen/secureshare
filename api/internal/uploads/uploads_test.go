package uploads

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTempCleanup(t *testing.T) {
	t.Run("private_random_spool_removed_immediately", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "uploads")
		f, remove, err := Create(dir)
		if err != nil {
			t.Fatal(err)
		}
		info, err := f.Stat()
		if err != nil || info.Mode().Perm() != 0600 || !pattern.MatchString(filepath.Base(f.Name())) {
			t.Fatal("unsafe spool")
		}
		dirInfo, _ := os.Stat(dir)
		if dirInfo.Mode().Perm() != 0700 {
			t.Fatal("unsafe directory")
		}
		remove()
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Fatal("spool leaked")
		}
	})
	t.Run("stale_only_owned_regular_files", func(t *testing.T) {
		parent := t.TempDir()
		dir := filepath.Join(parent, "uploads")
		if Ensure(dir) != nil {
			t.Fatal("directory")
		}
		old := time.Now().Add(-2 * time.Hour)
		before := time.Now().Add(-time.Hour)
		stale := "secureshare-upload-" + strings.Repeat("a", 32)
		fresh := "secureshare-upload-" + strings.Repeat("b", 32)
		link := "secureshare-upload-" + strings.Repeat("c", 32)
		folder := "secureshare-upload-" + strings.Repeat("d", 32)
		for _, name := range []string{stale, fresh, "unrelated", "secureshare-upload-invalid"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		os.Chtimes(filepath.Join(dir, stale), old, old)
		os.Chtimes(filepath.Join(dir, "unrelated"), old, old)
		outside := filepath.Join(parent, "outside")
		os.WriteFile(outside, []byte("keep"), 0600)
		os.Chtimes(outside, old, old)
		if err := os.Symlink(outside, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
		os.Mkdir(filepath.Join(dir, folder), 0700)
		os.Chtimes(filepath.Join(dir, folder), old, old)
		n, failed, err := Sweep(dir, before)
		if err != nil || n != 1 || failed != 0 {
			t.Fatal("unsafe sweep")
		}
		for _, path := range []string{outside, filepath.Join(dir, fresh), filepath.Join(dir, link), filepath.Join(dir, folder), filepath.Join(dir, "unrelated"), filepath.Join(dir, "secureshare-upload-invalid")} {
			if _, err := os.Lstat(path); err != nil {
				t.Fatal("unowned entry removed")
			}
		}
		n, failed, err = Sweep(dir, before)
		if err != nil || n != 0 || failed != 0 {
			t.Fatal("already disappeared not safe")
		}
	})
	t.Run("symlink_directory_rejected", func(t *testing.T) {
		parent := t.TempDir()
		real := filepath.Join(parent, "real")
		os.Mkdir(real, 0700)
		link := filepath.Join(parent, "uploads")
		if os.Symlink(real, link) != nil {
			t.Fatal("symlink")
		}
		if _, _, err := Sweep(link, time.Now()); err == nil {
			t.Fatal("followed directory symlink")
		}
	})
	t.Run("permissive_directory_rejected", func(t *testing.T) {
		dir := t.TempDir()
		os.Chmod(dir, 0755)
		if Ensure(dir) == nil {
			t.Fatal("accepted public spool")
		}
	})
}
