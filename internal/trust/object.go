package trust

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	pgpBegin = "-----BEGIN PGP SIGNATURE-----\n"
	pgpEnd   = "-----END PGP SIGNATURE-----\n"
)

type commit struct {
	tree    string
	parents []string
}

type treeEntry struct {
	mode string
	name string
	id   string
}

func hashObject(typ string, body []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "%s %d\x00", typ, len(body))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

func isObjectID(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

func headerLines(body []byte) (lines [][]byte, rest []byte) {
	for len(body) > 0 && body[0] != '\n' {
		end := bytes.IndexByte(body, '\n')
		if end < 0 {
			end = len(body) - 1
		}
		lines = append(lines, body[:end+1])
		body = body[end+1:]
	}
	return lines, body
}

// payload = body minus gpgsig header incl. continuation lines, as git signed it
func splitSignature(body []byte) (payload, sig []byte, err error) {
	lines, rest := headerLines(body)
	var signatures int
	inSig := false
	for _, line := range lines {
		switch {
		case bytes.HasPrefix(line, []byte("gpgsig ")):
			signatures++
			inSig = true
			sig = append(sig, line[len("gpgsig "):]...)
		case inSig && line[0] == ' ':
			sig = append(sig, line[1:]...)
		default:
			inSig = false
			payload = append(payload, line...)
		}
	}
	payload = append(payload, rest...)

	if signatures != 1 {
		return nil, nil, fail(ReasonSignatureStatus, "%d gpgsig headers", signatures)
	}
	if !bytes.HasPrefix(sig, []byte(pgpBegin)) || !bytes.HasSuffix(sig, []byte(pgpEnd)) {
		return nil, nil, fail(ReasonSignatureStatus, "signature not OpenPGP-armored")
	}
	return payload, sig, nil
}

func parseCommit(body []byte) (commit, error) {
	var c commit
	lines, _ := headerLines(body)
	for i, line := range lines {
		key, value, _ := strings.Cut(strings.TrimSuffix(string(line), "\n"), " ")
		switch {
		case i == 0 && key == "tree" && isObjectID(value):
			c.tree = value
		case key == "parent" && isObjectID(value):
			c.parents = append(c.parents, value)
		case key == "parent" || i == 0:
			return commit{}, fmt.Errorf("trust: malformed commit header %q", line)
		}
	}
	if c.tree == "" {
		return commit{}, errors.New("trust: commit without tree")
	}
	return c, nil
}

func parseTree(body []byte) ([]treeEntry, error) {
	var entries []treeEntry
	for len(body) > 0 {
		sp := bytes.IndexByte(body, ' ')
		nul := bytes.IndexByte(body, 0)
		if sp < 0 || nul < sp || len(body) < nul+1+sha1.Size {
			return nil, errors.New("trust: malformed tree")
		}
		entries = append(entries, treeEntry{
			mode: string(body[:sp]),
			name: string(body[sp+1 : nul]),
			id:   hex.EncodeToString(body[nul+1 : nul+1+sha1.Size]),
		})
		body = body[nul+1+sha1.Size:]
	}
	return entries, nil
}

func definitionsEntry(entries []treeEntry) (string, error) {
	for _, e := range entries {
		if e.name != definitionsFile {
			continue
		}
		switch e.mode {
		case "100644":
			return e.id, nil
		case "120000":
			return "", fail(ReasonSymlink, "committed %s is a symlink", definitionsFile)
		default:
			return "", fail(ReasonFileMode, "committed %s has mode %s", definitionsFile, e.mode)
		}
	}
	return "", fail(ReasonMissingDefinitions, "no %s in root tree", definitionsFile)
}
