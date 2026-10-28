# One bwrap engine for all actions, not Fence

All jobs run in a single engine, `podman unshare -- bwrap`, and `podman system service` starts only for actions with `containers = true`. Fence was the obvious choice for non-container actions, since the agent already runs in it. We dropped it because:

- Podman can't run inside Fence (E10), so a Fence engine would mean maintaining two engines with separate conformance tests.
- Fence reads the whole host by default, so a test could copy `~/.ssh` into a writes dir.
- Fence version changes would silently change job isolation.

## Consequences

- The engine depends on bwrap directly, so it must be a version with the 0.12.0 symlink-escape fix. We require ≥ 0.13.1, configured by path, instead of the distro's 0.6.1.
- Project paths are bound from fds the broker has already checked (`--bind-fd`), which closes the check/mount race.

## Considered Options

- Fence for plain actions and bwrap for container actions, with a cross-engine conformance test. Rejected for the reasons above.
