# One bwrap engine for all actions, not Fence

All jobs run in a single engine, `podman unshare -- bwrap`, and `podman system service` starts only for actions with `containers = true`. Fence was the obvious choice for non-container actions, since the agent already runs in it. We dropped it because:

- Podman can't run inside Fence (E10), so a Fence engine would mean maintaining two engines with separate conformance tests.
- Fence reads the whole host by default, so a test could copy `~/.ssh` into a writes dir.
- Fence version changes would silently change job isolation.

## Considered Options

- Fence for plain actions and bwrap for container actions, with a cross-engine conformance test. Rejected for the reasons above.
