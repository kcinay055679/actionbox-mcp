package conformance

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// E13 method
func socketInventory(root, procNetUnix string, allowed []string) Line {
	onDisk := walkSockets(root)
	f, err := os.Open(procNetUnix)
	if err != nil {
		return fail("read %s: %v", procNetUnix, err)
	}
	defer f.Close()
	paths, abstract, err := ParseProcNetUnix(f)
	if err != nil {
		return fail("parse %s: %v", procNetUnix, err)
	}

	var unexpected []string
	for _, p := range append(onDisk, paths...) {
		if !slices.Contains(allowed, p) && !slices.Contains(unexpected, p) {
			unexpected = append(unexpected, p)
		}
	}
	detail := fmt.Sprintf("fs: [%s], /proc/net/unix paths: [%s], abstract: [%s]",
		strings.Join(onDisk, ","), strings.Join(paths, ","), strings.Join(abstract, ","))
	if len(unexpected) > 0 {
		return fail("unexpected sockets [%s]; %s", strings.Join(unexpected, ","), detail)
	}
	return ok("%s", detail)
}

func walkSockets(root string) []string {
	skip := []string{filepath.Join(root, "proc"), filepath.Join(root, "sys")}
	var found []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() && slices.Contains(skip, p) {
			return fs.SkipDir
		}
		if d.Type()&fs.ModeSocket != 0 {
			found = append(found, p)
		}
		return nil
	})
	return found
}

func ParseProcNetUnix(r io.Reader) (paths, abstract []string, err error) {
	sc := bufio.NewScanner(r)
	header := true
	for sc.Scan() {
		if header {
			header = false
			if !strings.HasPrefix(sc.Text(), "Num") {
				return nil, nil, errors.New("missing header")
			}
			continue
		}
		fields := strings.Fields(sc.Text())
		if len(fields) < 8 {
			continue
		}
		name := fields[7]
		switch {
		case strings.HasPrefix(name, "@"):
			if !slices.Contains(abstract, name) {
				abstract = append(abstract, name)
			}
		case !slices.Contains(paths, name):
			paths = append(paths, name)
		}
	}
	return paths, abstract, sc.Err()
}
