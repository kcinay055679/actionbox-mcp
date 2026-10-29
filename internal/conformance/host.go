package conformance

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"actionbox/internal/engine"
)

// storage.conf identical to jobs' (spec §8.8, E14d)
type HostPodman struct {
	SelfExe string
}

func LookupPodman(selfExe string) Podman {
	if _, err := exec.LookPath("podman"); err != nil {
		return nil
	}
	return HostPodman{SelfExe: selfExe}
}

func (p HostPodman) Seed(ctx context.Context, containersDir, image string) error {
	confDir := filepath.Join(filepath.Dir(containersDir), "seed")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		return err
	}
	storage := filepath.Join(confDir, "storage.conf")
	containers := filepath.Join(confDir, "containers.conf")
	tarPath := filepath.Join(confDir, "image.tar")
	if err := os.WriteFile(storage, []byte(engine.StorageConf(containersDir)), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(containers, []byte(engine.ContainersConf()), 0o644); err != nil {
		return err
	}
	if err := WriteImageTar(tarPath); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "podman", "import", tarPath, image)
	cmd.Env = append(os.Environ(), "CONTAINERS_STORAGE_CONF="+storage, "CONTAINERS_CONF="+containers)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("podman import: %v: %s", err, tail(out))
	}
	return nil
}

// HashTree runs inside `podman unshare`: store files belong to subuids the owner can't read.
func (p HostPodman) HashTree(ctx context.Context, dir string) (string, error) {
	out, err := exec.CommandContext(ctx, "podman", "unshare", p.SelfExe, "probe", "hash-tree", dir).CombinedOutput()
	sum, parseErr := ParseHashLine(out)
	if parseErr != nil {
		return "", fmt.Errorf("hash %s: %v, %v: %s", dir, err, parseErr, tail(out))
	}
	return sum, nil
}

func (p HostPodman) RemoveAll(ctx context.Context, dir string) error {
	if out, err := exec.CommandContext(ctx, "podman", "unshare", "rm", "-rf", dir).CombinedOutput(); err != nil {
		return fmt.Errorf("podman unshare rm: %v: %s", err, tail(out))
	}
	return nil
}
