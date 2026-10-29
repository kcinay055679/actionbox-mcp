package config

import (
	"fmt"
	"os"
	"path/filepath"
)

type Paths struct {
	ConfigDir string
	DataDir   string
	StateDir  string
}

func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("locate home dir: %w", err)
	}
	return PathsForHome(home), nil
}

// XDG_* ignored: §8.2 fixes paths under ~, agent's Fence denies writes only there
func PathsForHome(home string) Paths {
	return Paths{
		ConfigDir: filepath.Join(home, ".config", "actionbox"),
		DataDir:   filepath.Join(home, ".local", "share", "actionbox"),
		StateDir:  filepath.Join(home, ".local", "state", "actionbox"),
	}
}

func (p Paths) ConfigFile() string { return filepath.Join(p.ConfigDir, "config.toml") }
func (p Paths) Keyring() string    { return filepath.Join(p.DataDir, "keyring.gpg") }

func (p Paths) ProjectDir(name string) string { return filepath.Join(p.DataDir, "projects", name) }
func (p Paths) MavenCache(name string) string { return filepath.Join(p.ProjectDir(name), "m2") }
func (p Paths) Ledger(name string) string     { return filepath.Join(p.ProjectDir(name), "ledger.jsonl") }
func (p Paths) NPMCache(name string) string   { return filepath.Join(p.ProjectDir(name), "npm") }
func (p Paths) Lock(name string) string       { return filepath.Join(p.ProjectDir(name), "lock") }

func (p Paths) ContainersStore(name string) string {
	return filepath.Join(p.ProjectDir(name), "containers", "store")
}

func (p Paths) ContainersRun(name string) string {
	return filepath.Join(p.ProjectDir(name), "containers", "run")
}

func (p Paths) Socket() string                 { return filepath.Join(p.StateDir, "broker.sock") }
func (p Paths) TrustFile(name string) string   { return filepath.Join(p.StateDir, "trust", name+".json") }
func (p Paths) FetchRequest(id string) string  { return filepath.Join(p.StateDir, "fetch", id+".json") }
func (p Paths) JobsDir(name string) string     { return filepath.Join(p.StateDir, "jobs", name) }
func (p Paths) JobDir(name, job string) string { return filepath.Join(p.JobsDir(name), job) }
