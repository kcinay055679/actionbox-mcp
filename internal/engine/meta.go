package engine

import (
	"encoding/json"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// Meta is what job-init needs inside the sandbox, written to $JOB/meta.json.
type Meta struct {
	Mounts []Mount `json:"mounts"`
	// PodmanSocket is empty for jobs without containers.
	PodmanSocket string `json:"podman_socket,omitempty"`
}

type Mount struct {
	Path string `json:"path"`
	Dev  uint64 `json:"dev"`
	Ino  uint64 `json:"ino"`
}

func MetaPath(jobDir string) string { return jobDir + "/meta.json" }

func WriteMeta(path string, m Meta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}
	return nil
}

func ReadMeta(path string) (Meta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Meta{}, fmt.Errorf("read meta: %w", err)
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, fmt.Errorf("parse meta %s: %w", path, err)
	}
	return m, nil
}

// RecheckMounts is the R7 backstop: bwrap already compares each fd with its mount point.
func RecheckMounts(mounts []Mount) error {
	for _, m := range mounts {
		var st unix.Stat_t
		if err := unix.Lstat(m.Path, &st); err != nil {
			return fmt.Errorf("mount point %s: %w", m.Path, err)
		}
		if st.Dev != m.Dev || st.Ino != m.Ino {
			return fmt.Errorf("mount point %s changed: (dev, inode) = (%d, %d), recorded (%d, %d)",
				m.Path, st.Dev, st.Ino, m.Dev, m.Ino)
		}
	}
	return nil
}
