package trust

import (
	"bytes"
	"errors"
	"io"
	"os"
	"syscall"
)

// step 7
func requireWorkingFile(repoPath string, blob []byte) error {
	f, err := openNoFollow(repoPath, definitionsFile)
	switch {
	case errors.Is(err, syscall.ELOOP):
		return fail(ReasonSymlink, "working %s is a symlink", definitionsFile)
	case errors.Is(err, syscall.ENOENT):
		return fail(ReasonUncommittedEdits, "working %s missing", definitionsFile)
	case err != nil:
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fail(ReasonFileMode, "working %s is %s", definitionsFile, info.Mode().Type())
	}
	content, err := io.ReadAll(io.LimitReader(f, int64(len(blob))+1))
	if err != nil {
		return err
	}
	if !bytes.Equal(content, blob) {
		return fail(ReasonUncommittedEdits, "working %s differs from commit", definitionsFile)
	}
	return nil
}

// O_NONBLOCK: planted fifo must not hang us
func openNoFollow(dir, name string) (*os.File, error) {
	dirFd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: dir, Err: err}
	}
	defer syscall.Close(dirFd)
	fd, err := syscall.Openat(dirFd, name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "openat", Path: name, Err: err}
	}
	return os.NewFile(uintptr(fd), name), nil
}
