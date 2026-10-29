// Package engine runs one job inside podman unshare + bwrap (spec §8.7, §8.8).
package engine

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"actionbox/internal/definitions"
)

// protectedDepth mirrors Fence's DefaultMaxDangerousFileDepth (spec §8.3).
const protectedDepth = 3

const firstExtraFD = 3

// Bind is one fd-based bind; Dest is the same path on host and in the sandbox.
type Bind struct {
	Dest     string
	ReadOnly bool
	// FD is the fd number in the child process (exec.Cmd.ExtraFiles index + 3).
	FD   int
	Dev  uint64
	Ino  uint64
	file *os.File
}

type Binds struct {
	Repo      Bind
	Writes    []Bind
	Protected []Bind
}

var resolveFlags uint64 = unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_BENEATH

// PrepareBinds opens the repo, its writes dirs (created if missing) and every existing protected
// path without following symlinks (R7, R26). Writes are bound parent-first, protected paths last.
func PrepareBinds(repo string, writes []string) (_ *Binds, err error) {
	b := &Binds{}
	protected := map[string]Bind{}
	defer func() {
		if err != nil {
			b.Close()
			for _, p := range protected {
				_ = p.file.Close()
			}
		}
	}()

	rootFD, err := unix.Open(repo, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open repo %s: %w", repo, err)
	}
	if b.Repo, err = newBind(rootFD, repo, true); err != nil {
		return nil, err
	}

	sorted := slices.Clone(writes)
	slices.Sort(sorted)
	for _, w := range sorted {
		fd, err := openWritesDir(rootFD, w)
		if err != nil {
			return nil, err
		}
		bind, err := newBind(fd, repo+"/"+w, false)
		if err != nil {
			return nil, err
		}
		b.Writes = append(b.Writes, bind)
		if err := findProtected(fd, repo+"/"+w, 1, protected); err != nil {
			return nil, err
		}
	}
	for _, name := range definitions.ProtectedPaths {
		if err := addProtected(rootFD, name, repo+"/"+name, protected); err != nil {
			return nil, err
		}
	}

	for _, dest := range slices.Sorted(maps.Keys(protected)) {
		b.Protected = append(b.Protected, protected[dest])
	}
	b.assignFDs()
	return b, nil
}

func (b *Binds) all() []*Bind {
	out := []*Bind{&b.Repo}
	for i := range b.Writes {
		out = append(out, &b.Writes[i])
	}
	for i := range b.Protected {
		out = append(out, &b.Protected[i])
	}
	return out
}

func (b *Binds) assignFDs() {
	for i, bind := range b.all() {
		bind.FD = firstExtraFD + i
	}
}

// Files returns the fds for exec.Cmd.ExtraFiles, in FD order.
func (b *Binds) Files() []*os.File {
	var out []*os.File
	for _, bind := range b.all() {
		out = append(out, bind.file)
	}
	return out
}

func (b *Binds) Mounts() []Mount {
	var out []Mount
	for _, bind := range b.all() {
		out = append(out, Mount{Path: bind.Dest, Dev: bind.Dev, Ino: bind.Ino})
	}
	return out
}

func (b *Binds) Close() {
	for _, bind := range b.all() {
		if bind.file != nil {
			_ = bind.file.Close()
		}
	}
}

func newBind(fd int, dest string, readOnly bool) (Bind, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return Bind{}, fmt.Errorf("stat %s: %w", dest, err)
	}
	return Bind{Dest: dest, ReadOnly: readOnly, Dev: st.Dev, Ino: st.Ino, file: os.NewFile(uintptr(fd), dest)}, nil
}

// openWritesDir walks w one component at a time, creating missing dirs with mkdirat.
func openWritesDir(rootFD int, w string) (int, error) {
	cur := -1
	parent := rootFD
	walked := ""
	for _, comp := range strings.Split(w, "/") {
		walked = path.Join(walked, comp)
		fd, err := openDirComponent(parent, comp, walked)
		if cur != -1 {
			_ = unix.Close(cur)
		}
		if err != nil {
			return -1, fmt.Errorf("writes %q: %w", w, err)
		}
		cur, parent = fd, fd
	}
	return cur, nil
}

func openDirComponent(parent int, name, rel string) (int, error) {
	fd, err := openNoFollow(parent, name)
	if errors.Is(err, unix.ENOENT) {
		if err := unix.Mkdirat(parent, name, 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
			return -1, fmt.Errorf("create %q: %w", rel, err)
		}
		fd, err = openNoFollow(parent, name)
	}
	if err != nil {
		return -1, fmt.Errorf("open %q: %w", rel, err)
	}
	kind, err := fileType(fd)
	if err != nil || kind != unix.S_IFDIR {
		_ = unix.Close(fd)
		if err == nil {
			err = describeType(rel, kind)
		}
		return -1, err
	}
	return fd, nil
}

func describeType(rel string, kind uint32) error {
	if kind == unix.S_IFLNK {
		return fmt.Errorf("%q is a symlink", rel)
	}
	return fmt.Errorf("%q is not a directory", rel)
}

// O_PATH|O_NOFOLLOW + RESOLVE_NO_SYMLINKS returns a trailing symlink itself, caller must check type
func openNoFollow(dirFD int, name string) (int, error) {
	return unix.Openat2(dirFD, name, &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_NOFOLLOW | unix.O_CLOEXEC,
		Resolve: resolveFlags,
	})
}

func fileType(fd int) (uint32, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return 0, err
	}
	return st.Mode & unix.S_IFMT, nil
}

func addProtected(dirFD int, name, dest string, found map[string]Bind) error {
	if _, ok := found[dest]; ok {
		return nil
	}
	fd, err := openNoFollow(dirFD, name)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("protected path %s: %w", dest, err)
	}
	if kind, err := fileType(fd); err != nil || kind == unix.S_IFLNK {
		_ = unix.Close(fd)
		if err == nil {
			err = errors.New("is a symlink")
		}
		return fmt.Errorf("protected path %s: %w", dest, err)
	}
	bind, err := newBind(fd, dest, true)
	if err != nil {
		return err
	}
	found[dest] = bind
	return nil
}

// findProtected binds entries with protected names at depth <= protectedDepth below dirFD.
// Symlinked dirs are not descended: what they point to is outside the bound tree.
func findProtected(dirFD int, dirDest string, depth int, found map[string]Bind) error {
	names, err := readDirNames(dirFD)
	if err != nil {
		return fmt.Errorf("list %s: %w", dirDest, err)
	}
	for _, name := range names {
		dest := dirDest + "/" + name
		if definitions.IsProtectedName(name) {
			if err := addProtected(dirFD, name, dest, found); err != nil {
				return err
			}
			continue
		}
		if depth >= protectedDepth {
			continue
		}
		child, err := unix.Openat2(dirFD, name, &unix.OpenHow{
			Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC,
			Resolve: resolveFlags,
		})
		if err != nil {
			continue // not a dir, a symlink or gone
		}
		err = findProtected(child, dest, depth+1, found)
		_ = unix.Close(child)
		if err != nil {
			return err
		}
	}
	return nil
}

func readDirNames(dirFD int) ([]string, error) {
	fd, err := unix.Openat2(dirFD, ".", &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: resolveFlags,
	})
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), ".")
	defer f.Close()
	return f.Readdirnames(-1)
}
