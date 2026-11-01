# t2c shell integration for zsh
typeset -g _t2c_dir="${XDG_STATE_HOME:-$HOME/.local/state}/t2c/s/$$"
command mkdir -p "$_t2c_dir" 2>/dev/null && command chmod 700 "$_t2c_dir" 2>/dev/null
typeset -g _t2c_gen="" _t2c_orig="" _t2c_last_cmd="" _t2c_last_exit=0 _t2c_errfd=""
typeset -gi _t2c_armed=0 _t2c_capturing=0 _t2c_ran=0

_t2c_preexec() {
  _t2c_ran=1
  _t2c_last_cmd=$1
  _t2c_gen=""
  : >| "$_t2c_dir/stderr" 2>/dev/null
  if (( _t2c_armed )); then
    # Keep a copy of the error output, for generated commands only.
    _t2c_armed=0
    _t2c_capturing=1
    exec {_t2c_errfd}>&2
    exec 2> >(command t2c _tee "$_t2c_dir/stderr")
  fi
}

_t2c_precmd() {
  local s=$?
  if (( _t2c_capturing )); then
    _t2c_capturing=0
    exec 2>&$_t2c_errfd
    exec {_t2c_errfd}>&-
  fi
  if (( _t2c_ran )); then
    _t2c_ran=0
    _t2c_last_exit=$s
  fi
  return $s
}

_t2c_widget() {
  emulate -L zsh
  # Pressing the shortcut on a generated command brings back the user's words.
  if [[ -n $_t2c_gen && $BUFFER == "$_t2c_gen" ]]; then
    BUFFER=$_t2c_orig
    CURSOR=$#BUFFER
    _t2c_gen=""
    _t2c_armed=0
    zle -M ""
    return
  fi
  zle -M "t2c: working..."
  zle -R
  local input=$BUFFER out rc warn cmd
  out=$(command t2c _gen --shell zsh --dir "$_t2c_dir" --last-cmd "$_t2c_last_cmd" --last-exit "$_t2c_last_exit" -- "$input" 2>/dev/null </dev/null)
  rc=$?
  if (( rc != 0 )); then
    zle -M "t2c: ${out:-cancelled}"
    return
  fi
  warn=${out%%$'\n'*}
  cmd=${out#*$'\n'}
  _t2c_orig=$input
  _t2c_gen=$cmd
  _t2c_armed=1
  BUFFER=$cmd
  CURSOR=$#BUFFER
  zle -M "${warn:+t2c: $warn}"
}

zle -N _t2c_widget
bindkey -M emacs '{{KEY_ZSH}}' _t2c_widget
bindkey -M viins '{{KEY_ZSH}}' _t2c_widget
autoload -Uz add-zsh-hook
add-zsh-hook preexec _t2c_preexec
# Runs first so it sees the command's real exit code.
precmd_functions=(_t2c_precmd ${precmd_functions:#_t2c_precmd})
