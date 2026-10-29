package trust

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

const testSig = "-----BEGIN PGP SIGNATURE-----\n\niHUEABYKAB0WIQ\n=abcd\n-----END PGP SIGNATURE-----\n"

func signedCommit(headerName, sig string) string {
	var b strings.Builder
	b.WriteString("tree 1e0cde859c715285d82ef683f2c3f04828ab2e44\n")
	b.WriteString("author A <a@test> 1767225600 +0000\n")
	b.WriteString("committer A <a@test> 1767225600 +0000\n")
	b.WriteString(foldHeader(headerName, sig))
	b.WriteString("\nmessage\n")
	return b.String()
}

func foldHeader(name, value string) string {
	lines := strings.Split(strings.TrimSuffix(value, "\n"), "\n")
	return name + " " + strings.Join(lines, "\n ") + "\n"
}

const unsignedCommit = "tree 1e0cde859c715285d82ef683f2c3f04828ab2e44\n" +
	"author A <a@test> 1767225600 +0000\n" +
	"committer A <a@test> 1767225600 +0000\n" +
	"\nmessage\n"

func TestHashObject(t *testing.T) {
	// git hash-object -t blob /dev/null
	if got := hashObject("blob", nil); got != "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391" {
		t.Fatalf("got %s", got)
	}
}

func TestSplitSignature(t *testing.T) {
	payload, sig, err := splitSignature([]byte(signedCommit("gpgsig", testSig)))
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != unsignedCommit {
		t.Errorf("payload:\n%q\nwant\n%q", payload, unsignedCommit)
	}
	if string(sig) != testSig {
		t.Errorf("sig:\n%q\nwant\n%q", sig, testSig)
	}
}

func TestSplitSignatureKeepsOtherHeaders(t *testing.T) {
	mergetag := foldHeader("mergetag", "object abc\ntype commit\n")
	sha256Sig := foldHeader("gpgsig-sha256", testSig)
	body := strings.Replace(signedCommit("gpgsig", testSig), "committer", mergetag+sha256Sig+"committer", 1)

	payload, _, err := splitSignature([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(unsignedCommit, "committer", mergetag+sha256Sig+"committer", 1)
	if string(payload) != want {
		t.Errorf("payload:\n%q\nwant\n%q", payload, want)
	}
}

func TestSplitSignatureRejects(t *testing.T) {
	sshSig := "-----BEGIN SSH SIGNATURE-----\nU1NIU0lH\n-----END SSH SIGNATURE-----\n"
	twoSigs := strings.Replace(signedCommit("gpgsig", testSig), "\n\nmessage", "\n"+foldHeader("gpgsig", testSig)+"\nmessage", 1)
	cases := map[string]string{
		"no signature":      unsignedCommit,
		"two signatures":    twoSigs,
		"ssh armor":         signedCommit("gpgsig", sshSig),
		"only sha256 sig":   signedCommit("gpgsig-sha256", testSig),
		"truncated armor":   signedCommit("gpgsig", "-----BEGIN PGP SIGNATURE-----\nabc\n"),
		"gpgsig in message": unsignedCommit + foldHeader("gpgsig", testSig),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := splitSignature([]byte(body))
			assertReason(t, err, ReasonSignatureStatus)
		})
	}
}

func TestParseCommit(t *testing.T) {
	body := "tree 1e0cde859c715285d82ef683f2c3f04828ab2e44\n" +
		"parent 50a92f747422e7ffcfe071a613a0e7179b316d40\n" +
		"parent d5fab654518e3acfadbd72b78dc8935bab382e62\n" +
		"author A <a@test> 1767225600 +0000\n" +
		"\nparent 0000000000000000000000000000000000000000\n"

	c, err := parseCommit([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if c.tree != "1e0cde859c715285d82ef683f2c3f04828ab2e44" {
		t.Errorf("tree %s", c.tree)
	}
	want := []string{"50a92f747422e7ffcfe071a613a0e7179b316d40", "d5fab654518e3acfadbd72b78dc8935bab382e62"}
	if strings.Join(c.parents, ",") != strings.Join(want, ",") {
		t.Errorf("parents %v", c.parents)
	}
}

func TestParseCommitRejectsMissingTree(t *testing.T) {
	if _, err := parseCommit([]byte("author A <a@test> 0 +0000\n\nmsg\n")); err == nil {
		t.Fatal("want error")
	}
}

func treeEntryBytes(mode, name, id string) []byte {
	raw, _ := hex.DecodeString(id)
	return append([]byte(mode+" "+name+"\x00"), raw...)
}

func TestParseTree(t *testing.T) {
	body := bytes.Join([][]byte{
		treeEntryBytes("100644", ".agent-actions.toml", "d22c25d2b403fd572fe1f17274b917de4cfebc87"),
		treeEntryBytes("40000", "src", "1e0cde859c715285d82ef683f2c3f04828ab2e44"),
	}, nil)

	entries, err := parseTree(body)
	if err != nil {
		t.Fatal(err)
	}
	want := []treeEntry{
		{mode: "100644", name: ".agent-actions.toml", id: "d22c25d2b403fd572fe1f17274b917de4cfebc87"},
		{mode: "40000", name: "src", id: "1e0cde859c715285d82ef683f2c3f04828ab2e44"},
	}
	if len(entries) != len(want) {
		t.Fatalf("got %v", entries)
	}
	for i := range want {
		if entries[i] != want[i] {
			t.Errorf("entry %d: got %+v want %+v", i, entries[i], want[i])
		}
	}
}

func TestParseTreeRejectsTruncated(t *testing.T) {
	full := treeEntryBytes("100644", "a", "d22c25d2b403fd572fe1f17274b917de4cfebc87")
	for _, body := range [][]byte{full[:len(full)-1], []byte("100644 a"), []byte("100644")} {
		if _, err := parseTree(body); err == nil {
			t.Errorf("want error for %q", body)
		}
	}
}

func TestDefinitionsEntry(t *testing.T) {
	id := "d22c25d2b403fd572fe1f17274b917de4cfebc87"
	cases := []struct {
		mode, name string
		reason     string
	}{
		{"100644", definitionsFile, ""},
		{"120000", definitionsFile, ReasonSymlink},
		{"100755", definitionsFile, ReasonFileMode},
		{"40000", definitionsFile, ReasonFileMode},
		{"100644", "other.toml", ReasonMissingDefinitions},
	}
	for _, c := range cases {
		t.Run(c.mode+" "+c.name, func(t *testing.T) {
			got, err := definitionsEntry([]treeEntry{{mode: c.mode, name: c.name, id: id}})
			if c.reason == "" {
				if err != nil || got != id {
					t.Fatalf("got %q, %v", got, err)
				}
				return
			}
			assertReason(t, err, c.reason)
		})
	}
}
