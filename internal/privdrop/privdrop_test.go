package privdrop

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestOwnTreeDoesNotFollowSymlinks(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to change ownership")
	}
	dir := t.TempDir()
	os.Chmod(dir, 0o700) // like mktemp: not listable by root once it changes owner
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("x"), 0o600)
	os.WriteFile(filepath.Join(dir, "session.key"), []byte("k"), 0o600)
	os.Symlink(outside, filepath.Join(dir, "evil"))
	os.MkdirAll(filepath.Join(dir, "sub"), 0o700)
	os.WriteFile(filepath.Join(dir, "sub", "f"), []byte("x"), 0o600)

	if err := OwnTree(dir, UID, GID); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dir, filepath.Join(dir, "session.key"), filepath.Join(dir, "sub"), filepath.Join(dir, "sub", "f")} {
		st, _ := os.Stat(p)
		if st.Sys().(*syscall.Stat_t).Uid != UID {
			t.Errorf("%s not owned by %d", p, UID)
		}
	}
	st, _ := os.Stat(outside)
	if st.Sys().(*syscall.Stat_t).Uid == UID {
		t.Fatal("symlink target outside the data directory was changed")
	}
}

func TestOwnTreeSkipsAlreadyOwnedTree(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to change ownership")
	}
	dir := t.TempDir()
	os.Chown(dir, UID, GID)
	os.Chmod(dir, 0o700) // root could not list it any more
	if err := OwnTree(dir, UID, GID); err != nil {
		t.Fatalf("already owned tree must be a no-op: %v", err)
	}
}

func TestOwnTreeTakesOverUnlistableDirectory(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to change ownership")
	}
	// A 0700 directory of another (non-root) user, like a CI runner's mktemp.
	// The test process has CAP_DAC_READ_SEARCH, so the listing failure cannot be
	// reproduced here; this checks that the directory itself is taken over.
	dir := t.TempDir()
	os.Chown(dir, 1001, 1001)
	os.Chmod(dir, 0o700)
	_ = OwnTree(dir, UID, GID)
	st, _ := os.Stat(dir)
	if st.Sys().(*syscall.Stat_t).Uid != UID {
		t.Fatal("directory not taken over")
	}
}
