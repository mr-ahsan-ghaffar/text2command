# t2c shell integration for fish
if status is-interactive
    set -g __t2c_dir (string join / (set -q XDG_STATE_HOME; and echo $XDG_STATE_HOME; or echo $HOME/.local/state) t2c s $fish_pid)
    command mkdir -p $__t2c_dir 2>/dev/null; and command chmod 700 $__t2c_dir 2>/dev/null
    set -g __t2c_gen ""
    set -g __t2c_orig ""
    set -g __t2c_last_cmd ""
    set -g __t2c_last_exit 0

    function __t2c_postexec --on-event fish_postexec
        set -g __t2c_last_exit $status
        set -g __t2c_last_cmd $argv[1]
        set -g __t2c_gen ""
    end

    function __t2c_widget
        set -l input (commandline | string collect)
        # Pressing the shortcut on a generated command brings back the user's words.
        if test -n "$__t2c_gen"; and test "$input" = "$__t2c_gen"
            commandline -r -- "$__t2c_orig"
            set -g __t2c_gen ""
            commandline -f repaint
            return
        end
        echo
        echo "t2c: working..."
        set -l out (command t2c _gen --shell fish --dir $__t2c_dir --last-cmd "$__t2c_last_cmd" --last-exit "$__t2c_last_exit" -- "$input" </dev/null 2>/dev/null | string collect)
        set -l rc $pipestatus[1]
        if test $rc -ne 0
            test -n "$out"; or set out cancelled
            echo "t2c: $out"
            commandline -f repaint
            return
        end
        set -l parts (string split -m1 \n -- "$out")
        test -n "$parts[1]"; and echo "t2c: $parts[1]"
        set -g __t2c_orig "$input"
        set -g __t2c_gen "$parts[2]"
        commandline -r -- "$parts[2]"
        commandline -f repaint
    end

    bind {{KEY_FISH}} __t2c_widget
    bind -M insert {{KEY_FISH}} __t2c_widget 2>/dev/null
end
