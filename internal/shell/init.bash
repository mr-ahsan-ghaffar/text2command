# t2c shell integration for bash
if [[ $- == *i* ]]; then
if (( BASH_VERSINFO[0] < 4 || (BASH_VERSINFO[0] == 4 && BASH_VERSINFO[1] < 1) )); then
  # Bash 3.2, which macOS ships, can't replace the text on the prompt.
  printf 't2c: the shortcut needs bash 4.1 or newer, and this is bash %s. Use zsh, or run: t2c "your request"\n' "$BASH_VERSION" >&2
else
__t2c_dir="${XDG_STATE_HOME:-$HOME/.local/state}/t2c/s/$$"
command mkdir -p "$__t2c_dir" 2>/dev/null && command chmod 700 "$__t2c_dir" 2>/dev/null
__t2c_gen="" __t2c_orig="" __t2c_last_cmd="" __t2c_last_exit=0 __t2c_histnum=""
__t2c_armed=0 __t2c_trap_set=0 __t2c_clear=0 __t2c_capturing=0 __t2c_captured=0 __t2c_status=0 __t2c_errfd=""

# Runs before a generated command. The trap only exists while one is waiting.
__t2c_debug() {
  (( __t2c_armed )) || return 0
  case $BASH_COMMAND in *__t2c_*) return 0 ;; esac
  [[ -n ${COMP_LINE:-} ]] && return 0
  __t2c_armed=0
  __t2c_capturing=1
  : >| "$__t2c_dir/stderr" 2>/dev/null
  exec {__t2c_errfd}>&2
  exec 2> >(command t2c _tee "$__t2c_dir/stderr")
}

__t2c_arm() {
  if (( ! __t2c_trap_set )); then
    # Leave another tool's DEBUG trap alone. Its command runs without capture.
    [[ -z $(trap -p DEBUG) ]] || return 0
    __t2c_trap_set=1
    trap '__t2c_debug' DEBUG
  fi
  __t2c_armed=1
  __t2c_clear=0
}

# Bash puts a DEBUG trap back when a function removes it, so this only asks.
# __t2c_prompt removes the trap at the top level.
__t2c_disarm() {
  __t2c_armed=0
  (( __t2c_trap_set )) && __t2c_clear=1
}

__t2c_precmd() {
  local s=$? line num
  (( __t2c_armed )) || __t2c_disarm
  if (( __t2c_capturing )); then
    __t2c_capturing=0
    exec 2>&$__t2c_errfd
    exec {__t2c_errfd}>&-
    __t2c_captured=1
  else
    __t2c_captured=0
  fi
  line=$(HISTTIMEFORMAT= builtin history 1)
  read -r num _ <<<"$line"
  # An empty Enter adds no history, so it mustn't clear what the last command left.
  if [[ $num != "$__t2c_histnum" ]]; then
    __t2c_histnum=$num
    __t2c_last_exit=$s
    __t2c_gen=""
    line=${line#*[0-9] }
    __t2c_last_cmd=${line#"${line%%[![:space:]]*}"}
    (( __t2c_captured )) || : >| "$__t2c_dir/stderr" 2>/dev/null
  fi
  __t2c_status=$s
}

__t2c_restore_status() {
  return $__t2c_status
}

__t2c_widget() {
  if [[ -n $__t2c_gen && $READLINE_LINE == "$__t2c_gen" ]]; then
    READLINE_LINE=$__t2c_orig
    READLINE_POINT=${#READLINE_LINE}
    __t2c_gen=""
    __t2c_disarm
    return
  fi
  local input=$READLINE_LINE out rc warn cmd
  printf '\rt2c: working...\033[K' >/dev/tty
  out=$(command t2c _gen --shell bash --dir "$__t2c_dir" --last-cmd "$__t2c_last_cmd" --last-exit "$__t2c_last_exit" -- "$input" 2>/dev/null </dev/null)
  rc=$?
  printf '\r\033[K' >/dev/tty
  if (( rc != 0 )); then
    printf 't2c: %s\n' "${out:-cancelled}" >/dev/tty
    return
  fi
  warn=${out%%$'\n'*}
  cmd=${out#*$'\n'}
  [[ -n $warn ]] && printf 't2c: %s\n' "$warn" >/dev/tty
  __t2c_orig=$input
  __t2c_gen=$cmd
  __t2c_arm
  READLINE_LINE=$cmd
  READLINE_POINT=${#cmd}
}

bind -m emacs -x '"{{KEY_BASH}}": __t2c_widget'
bind -m vi-insert -x '"{{KEY_BASH}}": __t2c_widget'

# Runs first so it sees the command's real exit code, which later hooks still get.
__t2c_prompt='__t2c_precmd; if (( __t2c_clear )); then __t2c_clear=0 __t2c_trap_set=0; trap - DEBUG; fi; __t2c_restore_status'
if [[ $(declare -p PROMPT_COMMAND 2>/dev/null) == "declare -a"* ]]; then
  PROMPT_COMMAND=("$__t2c_prompt" "${PROMPT_COMMAND[@]}")
else
  PROMPT_COMMAND="$__t2c_prompt${PROMPT_COMMAND:+;$PROMPT_COMMAND}"
fi
fi
fi
fi
