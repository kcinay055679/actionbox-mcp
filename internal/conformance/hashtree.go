package conformance

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

func HashTree(root string) (sum string, entries int, err error) {
	h := sha256.New()
	if _, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
		return "absent", 0, nil
	}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		fi, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%q %v %d %d\n", rel, fi.Mode(), fi.Size(), fi.ModTime().UnixNano())
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			fmt.Fprintf(h, "owner %d:%d\n", st.Uid, st.Gid)
		}
		entries++
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "-> %q\n", target)
		case fi.Mode().IsRegular():
			return hashFile(h, p)
		}
		return nil
	})
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), entries, nil
}

func hashFile(w io.Writer, p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

func hashTreeProbe(root string) Line {
	if root == "" {
		return fail("usage: hash-tree <dir>")
	}
	sum, n, err := HashTree(root)
	if err != nil {
		return fail("hash %s: %v", root, err)
	}
	return ok("%s %d", sum, n)
}
