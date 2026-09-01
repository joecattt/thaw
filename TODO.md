# TODO

Tracked follow-ups. Inline-marker audit (2026-07-05): a full sweep for
`TODO|FIXME|HACK|XXX` found **zero** inline markers in Go source — the "~25"
count reported by `thaw progress` on this repo comes from the tool matching its
own pattern-definition strings (`internal/progress/progress.go`,
`internal/project/project.go`), not real debt markers.

**2026-07-12 audit:** re-verified every item below against current source.
The "critical path" section from the prior version of this file (recovery-
error swallowing, prune-error swallowing, tmux.go's error paths) was fully
resolved and has been removed — the fixes just never made it back into this
doc. Same for the version-drift item (now centralized in
`internal/buildinfo`, no hardcoded string left to drift). Also: `cmd/thaw`
was split from one 2,691-line `main.go` into one file per command
(`cmd_*.go`) plus `helpers.go`/`restore.go` — file references below are
updated accordingly.

## Nice-to-have

- **Telemetry double gate** — sending requires `telemetry.enabled = true` in
  config *and* the `telemetry-optin` marker file *and* a non-empty
  `firebase_url` (`internal/telemetry/telemetry.go`). Editing `config.toml`
  by hand (without `thaw config set`) never creates the marker, so telemetry
  stays off — safe, but surprising. Decide whether the marker file is still
  needed now that config gates it.
- **First-run detection** — bare `thaw` shows a welcome pointing at
  `thaw setup` when `config.toml` is missing (`cmd/thaw/main.go`). Users who
  only ran `thaw config set ...` (which creates the file) skip the welcome;
  acceptable, noted for completeness.
- **`internal/config.DataDir()` has no test-isolation override** — hardcoded
  to `~/.local/share/thaw`, unlike `commandLogPath`/heartbeat in
  `internal/daemon/daemon.go` which respect `XDG_STATE_HOME`. Found while
  adding daemon tests (2026-07-12): PID-file management (`writePIDFile`,
  `IsRunning`, `Stop`) couldn't be safely tested against the real path
  without risking a live daemon's state, so that coverage was skipped. Give
  `DataDir()` the same env-var override pattern and those become testable.
- **Test coverage** — was 9/40 packages (22%) as of 2026-07-12; `capture`
  (84.6%) and `daemon` (25.7%, capped by the item above) are now covered.
  `cmd/thaw`, `config`, `git`, `llm`, `memory` are still at 0%.
