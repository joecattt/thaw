package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func shellInitCmd() *cobra.Command {
	return &cobra.Command{
		Use: "shell-init [zsh|bash]", Short: "Print the shell hook code (for eval in your rc file)",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "zsh":
				os.Stdout.WriteString(zshInit)
			case "bash":
				os.Stdout.WriteString(bashInit)
			default:
				return fmt.Errorf("unsupported: %s (use zsh or bash)", args[0])
			}
			return nil
		},
	}
}

const zshInit = `# thaw — terminal workspace memory
_thaw_log="${XDG_STATE_HOME:-$HOME/.local/state}/thaw"
[[ -d "$_thaw_log" ]] || mkdir -p "$_thaw_log" 2>/dev/null
zshexit() { command -v thaw &>/dev/null && thaw freeze --source=shutdown 2>/dev/null &! }
_thaw_preexec() {
  # Replace newlines with spaces (heredocs, continuations).
  local cmd="${1//$'\n'/ }"
  # Route through 'thaw log-cmd' so credentials are SCRUBBED before they touch disk.
  # Backgrounded (&!) to keep the prompt instant; the log write is append-atomic.
  command -v thaw &>/dev/null && thaw log-cmd "$$" "$PWD" "$cmd" 2>/dev/null &!
  # Idle gap detection — if >30min since last command, show context
  if [[ -f "$_thaw_log/.last_cmd_time" ]]; then
    local last_t=$(cat "$_thaw_log/.last_cmd_time" 2>/dev/null)
    local now_t=$(date +%s)
    if [[ -n "$last_t" ]] && (( now_t - last_t > 1800 )); then
      local gap_min=$(( (now_t - last_t) / 60 ))
      echo "thaw: ${gap_min}m idle gap detected"
      if [[ -d "$PWD/.git" ]] && command -v thaw &>/dev/null; then
        thaw context "$PWD" 2>/dev/null
      fi
    fi
  fi
  echo "$(date +%s)" > "$_thaw_log/.last_cmd_time" 2>/dev/null
}
_thaw_chpwd() {
  command -v thaw &>/dev/null && thaw log-cmd "$$" "$PWD" "cd $PWD" 2>/dev/null &!
  # Show context when entering a tracked project dir
  if [[ -d "$PWD/.git" ]] && command -v thaw &>/dev/null; then
    thaw context "$PWD" 2>/dev/null
  fi
  if command -v thaw &>/dev/null; then
    local _thaw_lbl="$(thaw label "$PWD" 2>/dev/null)"
    if [[ -n "$_thaw_lbl" && "$_thaw_lbl" != "$_thaw_win_label" ]]; then
      # Name the tmux window after the project — tmux-only, cosmetic.
      if [[ -n "$TMUX" ]]; then
        tmux rename-window "$_thaw_lbl" 2>/dev/null
      fi
      # Session write-up boundary: closes out the OLD project (AI write-up
      # if THAW_SUMMARIZE_CMD is set and the session was real — >3min,
      # something actually changed; a no-op otherwise) and opens the NEW
      # one (prints the last write-up for it, if any — free, just a log
      # lookup). Not tmux-gated: this is about project identity, not
      # terminal cosmetics. Backgrounded — "end" can take seconds for a
      # real summarizer round trip and must never block the prompt.
      if [[ -n "$_thaw_win_label" && -n "$_thaw_session_dir" ]]; then
        thaw session-note end "$_thaw_session_dir" 2>/dev/null &!
      fi
      # start is fast (a log lookup, no summarizer call) — runs in the foreground so
      # its "last time..." line shows up right when you cd in, not popping
      # in unpredictably later like a backgrounded job would.
      thaw session-note start "$PWD" 2>/dev/null
      _thaw_session_dir="$PWD"
      _thaw_win_label="$_thaw_lbl"
    fi
  fi
}
autoload -U add-zsh-hook
add-zsh-hook preexec _thaw_preexec
add-zsh-hook chpwd _thaw_chpwd
# Autostash (opt-in): only wire it if capture.autostash = true. Silently git-stashing
# on 'cd' is a footgun, so it is off by default and gated here, not run unconditionally.
if command -v thaw &>/dev/null && [[ "$(thaw config get capture.autostash 2>/dev/null)" == "true" ]]; then
  _thaw_autostash_dir=""
  _thaw_pre_chpwd() {
    if [[ -n "$_thaw_autostash_dir" ]] && [[ "$_thaw_autostash_dir" != "$PWD" ]]; then
      if [[ -d "$_thaw_autostash_dir/.git" ]]; then
        local dirty=$(git -C "$_thaw_autostash_dir" status --porcelain 2>/dev/null)
        if [[ -n "$dirty" ]]; then
          git -C "$_thaw_autostash_dir" stash push -m "thaw-auto-$(date +%s)" -q 2>/dev/null
          echo "thaw: auto-stashed changes in $(basename $_thaw_autostash_dir)"
        fi
      fi
    fi
    _thaw_autostash_dir="$PWD"
  }
  add-zsh-hook chpwd _thaw_pre_chpwd
fi
# One-time heartbeat check per shell session
if [[ -f "$_thaw_log/daemon.heartbeat" ]]; then
  _thaw_hb_age=$(( $(date +%s) - $(cat "$_thaw_log/daemon.heartbeat" 2>/dev/null || echo 0) ))
  if (( _thaw_hb_age > 900 )); then
    echo "thaw: daemon may be stopped (last heartbeat $(( _thaw_hb_age / 60 ))m ago) — run: thaw daemon start"
  fi
  unset _thaw_hb_age
fi
# Morning briefing — once per day, first terminal only (opt-in: voice.morning_briefing = true)
_thaw_brief="$_thaw_log/.briefed-$(date +%Y%m%d)"
if [[ ! -f "$_thaw_brief" ]] && command -v thaw &>/dev/null; then
  if [[ "$(thaw config get voice.morning_briefing 2>/dev/null)" == "true" ]]; then
    touch "$_thaw_brief"
    thaw recap --briefing &>/dev/null &!
  fi
fi
unset _thaw_brief
`

const bashInit = `# thaw — terminal workspace memory
_thaw_log="${XDG_STATE_HOME:-$HOME/.local/state}/thaw"
[[ -d "$_thaw_log" ]] || mkdir -p "$_thaw_log" 2>/dev/null
trap 'command -v thaw &>/dev/null && thaw freeze --source=shutdown 2>/dev/null &' EXIT
_thaw_last_cmd=""
_thaw_prompt() {
  local cmd="$(HISTTIMEFORMAT= history 1 | sed 's/^[ ]*[0-9]*[ ]*//')"
  if [ -n "$cmd" ] && [ "$cmd" != "$_thaw_last_cmd" ]; then
    _thaw_last_cmd="$cmd"
    cmd="${cmd//$'\n'/ }"
    # Route through 'thaw log-cmd' so credentials are scrubbed before disk (backgrounded).
    command -v thaw &>/dev/null && thaw log-cmd "$$" "$PWD" "$cmd" 2>/dev/null &
  fi
  # Name the tmux window after the project + session write-up boundary, on
  # first prompt in a new project. Same design as the zsh hook above:
  # tmux-rename is tmux-only (cosmetic); the session-note start/end pair
  # isn't tmux-gated (project identity, not terminal cosmetics), and
  # "start" runs in the foreground (fast, no API call) while "end" is
  # backgrounded (a real summarizer round trip when THAW_SUMMARIZE_CMD is
  # set — a no-op otherwise — and must never block the prompt).
  if [ "$PWD" != "$_thaw_last_pwd" ] && command -v thaw &>/dev/null; then
    _thaw_last_pwd="$PWD"
    local lbl="$(thaw label "$PWD" 2>/dev/null)"
    if [ -n "$lbl" ] && [ "$lbl" != "$_thaw_win_label" ]; then
      if [ -n "$TMUX" ]; then
        tmux rename-window "$lbl" 2>/dev/null
      fi
      if [ -n "$_thaw_win_label" ] && [ -n "$_thaw_session_dir" ]; then
        thaw session-note end "$_thaw_session_dir" 2>/dev/null &
      fi
      thaw session-note start "$PWD" 2>/dev/null
      _thaw_session_dir="$PWD"
      _thaw_win_label="$lbl"
    fi
  fi
}
# One-time heartbeat check
if [ -f "$_thaw_log/daemon.heartbeat" ]; then
  _thaw_hb_age=$(( $(date +%s) - $(cat "$_thaw_log/daemon.heartbeat" 2>/dev/null || echo 0) ))
  if [ "$_thaw_hb_age" -gt 900 ] 2>/dev/null; then
    echo "thaw: daemon may be stopped (last heartbeat $(( _thaw_hb_age / 60 ))m ago) — run: thaw daemon start"
  fi
  unset _thaw_hb_age
fi
PROMPT_COMMAND="_thaw_prompt;${PROMPT_COMMAND}"
`
