package conformance

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	SeedImage  = "localhost/actionbox-conformance-seed:1"
	probeImage = "localhost/actionbox-conformance-probe:1"
	probeTag   = "localhost/actionbox-conformance-probe:2"
	probeCtr   = "actionbox-conformance-probe"
)

func WriteImageTar(path string) error {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	content := []byte(probeContent)
	if err := tw.WriteHeader(&tar.Header{Name: "conformance", Mode: 0o644, Size: int64(len(content))}); err != nil {
		return err
	}
	if _, err := tw.Write(content); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// seed "-": harness could not seed the project store
func podmanProbe(ctx context.Context, seed string) Line {
	if seed == "" {
		return fail("usage: podman <seed image|->")
	}
	if _, err := exec.LookPath("podman"); err != nil {
		return skip("podman not on PATH inside the job")
	}
	tarPath := filepath.Join(os.TempDir(), tmpPrefix+"image.tar")
	if err := WriteImageTar(tarPath); err != nil {
		return fail("write image tar: %v", err)
	}
	steps := [][]string{
		{"image", "ls"},
		{"import", tarPath, probeImage},
		{"tag", probeImage, probeTag},
		{"create", "--name", probeCtr, probeImage, "/conformance"},
		{"rm", probeCtr},
		{"rmi", probeTag},
	}
	if seed != "-" {
		steps = append(steps, []string{"rmi", "--force", seed})
	}
	var notes []string
	failed := false
	for _, s := range steps {
		out, err := exec.CommandContext(ctx, "podman", s...).CombinedOutput()
		if err != nil {
			failed = true
			notes = append(notes, fmt.Sprintf("%s: %v (%s)", s[0], err, lastLine(string(out))))
			continue
		}
		notes = append(notes, s[0]+": ok")
	}
	if failed {
		return fail("store not fully exercised: %s", strings.Join(notes, "; "))
	}
	return ok("%s", strings.Join(notes, "; "))
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
