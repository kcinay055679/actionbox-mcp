package definitions

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
)

var ProtectedPaths = []string{
	".git", ".agent-actions.toml", ".gitattributes", ".gitmodules", ".gitconfig",
	".idea", ".vscode", ".envrc", ".mcp.json", ".claude",
	"mvnw", "mvnw.cmd", ".mvn", ".npmrc",
	".bashrc", ".bash_profile", ".zshrc", ".zprofile", ".profile", ".ripgreprc",
}

func IsProtectedName(name string) bool {
	return slices.Contains(ProtectedPaths, name)
}

const globChars = `*?[]{}\`

func validateWrites(writes []string) error {
	seen := make(map[string]bool, len(writes))
	for _, w := range writes {
		if err := validateWritesDir(w); err != nil {
			return fmt.Errorf("writes %q: %w", w, err)
		}
		if seen[w] {
			return fmt.Errorf("writes %q: duplicate", w)
		}
		seen[w] = true
	}
	return nil
}

func validateWritesDir(w string) error {
	switch {
	case w == "" || w == ".":
		return errors.New("must name a directory below the project root")
	case strings.ContainsRune(w, 0):
		return errors.New("contains NUL")
	case path.IsAbs(w):
		return errors.New("must be relative")
	case slices.Contains(strings.Split(w, "/"), ".."):
		return errors.New("must not contain ..")
	case strings.ContainsAny(w, globChars):
		return errors.New("must not contain glob characters")
	case path.Clean(w) != w:
		return fmt.Errorf("must be in clean form %q", path.Clean(w))
	}
	for _, p := range ProtectedPaths {
		if overlaps(w, p) {
			return fmt.Errorf("overlaps protected path %q", p)
		}
	}
	return nil
}

func overlaps(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}
