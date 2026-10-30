// Package privdrop lets a service start as root only to take ownership of its
// data directory and then permanently switch to an unprivileged user (the
// "gosu" pattern). Needed because some platforms, e.g. ZimaOS, create volume
// directories on the host as root:root, which the non-root service cannot write.
package privdrop

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// UID and GID of the unprivileged user (distroless "nonroot").
const (
	UID = 65532
	GID = 65532
)

// OwnTree makes dir and everything below it owned by uid:gid. Symbolic links
// are never followed (Lstat, Lchown), so planted links cannot redirect the
// change to files outside dir. Contents are changed before their directory:
// root without CAP_DAC_READ_SEARCH can no longer list a 0700 directory once
// it belongs to someone else. A tree whose root already has the right owner
// is left alone (the service itself only ever creates files as uid:gid).
func OwnTree(dir string, uid, gid int) error {
	if owned(dir, uid, gid) {
		return nil
	}
	return ownPostOrder(dir, uid, gid)
}

func ownPostOrder(path string, uid, gid int) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	var contentErr error
	if info.IsDir() {
		// Without CAP_DAC_READ_SEARCH (deliberately not granted) root cannot
		// list a 0700 directory of another user. The directory itself can
		// still be taken over, which is all a fresh data directory needs.
		entries, err := os.ReadDir(path)
		if err != nil {
			contentErr = fmt.Errorf("contents of %s left unchanged: %w", path, err)
		}
		for _, e := range entries {
			if err := ownPostOrder(filepath.Join(path, e.Name()), uid, gid); err != nil && contentErr == nil {
				contentErr = err
			}
		}
	}
	if err := os.Lchown(path, uid, gid); err != nil {
		return errors.Join(err, contentErr)
	}
	return contentErr
}

func owned(path string, uid, gid int) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == uid && int(st.Gid) == gid
}

// Drop permanently switches the whole process to uid:gid and removes all
// supplementary groups. Linux clears the remaining capabilities on this
// transition. Since Go 1.16 the change applies to every thread.
func Drop(uid, gid int) error {
	if err := syscall.Setgroups([]int{}); err != nil {
		return fmt.Errorf("setgroups: %w", err)
	}
	if err := syscall.Setgid(gid); err != nil {
		return fmt.Errorf("setgid: %w", err)
	}
	if err := syscall.Setuid(uid); err != nil {
		return fmt.Errorf("setuid: %w", err)
	}
	if os.Getuid() != uid || os.Geteuid() != uid || os.Getgid() != gid || os.Getegid() != gid {
		return fmt.Errorf("privileges not dropped (uid=%d euid=%d)", os.Getuid(), os.Geteuid())
	}
	return nil
}
