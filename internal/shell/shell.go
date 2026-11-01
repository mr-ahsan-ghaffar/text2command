// Package shell provides the hooks that connect t2c to bash, zsh, and fish.
package shell

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"t2c/internal/config"
)

//go:embed init.zsh
var zshInit string

//go:embed init.bash
var bashInit string

//go:embed init.fish
var fishInit string

var keyPattern = regexp.MustCompile(`^ctrl-[a-z]$`)

// Init returns the script a shell evaluates at startup. key looks like "ctrl-g".
func Init(shell, key string) (string, error) {
	if !keyPattern.MatchString(key) {
		return "", fmt.Errorf("unsupported shortcut %q. Use ctrl- plus a letter, like ctrl-g", key)
	}
	letter := key[len(key)-1:]
	r := strings.NewReplacer(
		"{{KEY_ZSH}}", "^"+strings.ToUpper(letter),
		"{{KEY_BASH}}", `\C-`+letter,
		"{{KEY_FISH}}", `\c`+letter,
	)
	switch shell {
	case "zsh":
		return r.Replace(zshInit), nil
	case "bash":
		return r.Replace(bashInit), nil
	case "fish":
		return r.Replace(fishInit), nil
	}
	return "", fmt.Errorf("unsupported shell %q. t2c works with bash, zsh, and fish", shell)
}

// Detect returns the user's login shell name.
func Detect() string {
	return filepath.Base(os.Getenv("SHELL"))
}

const (
	setupMarker  = "t2c init"
	setupComment = "# t2c: turn plain language into commands"
)

// setupBlock matches what Setup writes, including the blank line before it.
var setupBlock = regexp.MustCompile(`\n?` + regexp.QuoteMeta(setupComment) + `\n(.*# t2c path\n)?.*t2c init.*\n`)

// startupFile returns where a shell's t2c lines go.
func startupFile(shell string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch shell {
	case "zsh":
		dir := os.Getenv("ZDOTDIR")
		if dir == "" {
			dir = home
		}
		return filepath.Join(dir, ".zshrc"), nil
	case "bash":
		return filepath.Join(home, ".bashrc"), nil
	case "fish":
		dir := os.Getenv("XDG_CONFIG_HOME")
		if dir == "" {
			dir = filepath.Join(home, ".config")
		}
		return filepath.Join(dir, "fish", "conf.d", "t2c.fish"), nil
	}
	return "", fmt.Errorf("unsupported shell %q. t2c works with bash, zsh, and fish", shell)
}

// pathLine adds t2c's folder to PATH in new shells, when it isn't there already.
func pathLine(shell string) string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exe)
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if d == dir {
			return ""
		}
	}
	if shell == "fish" {
		return fmt.Sprintf("set -gx PATH %q $PATH # t2c path\n", dir)
	}
	return fmt.Sprintf("export PATH=%q:\"$PATH\" # t2c path\n", dir)
}

// Setup adds t2c to the shell's startup file. It reports the file and whether
// it had to change anything.
func Setup(shell string) (string, bool, error) {
	file, err := startupFile(shell)
	if err != nil {
		return "", false, err
	}
	line := fmt.Sprintf(`command -v t2c >/dev/null && eval "$(t2c init %s)"`, shell)
	if shell == "fish" {
		line = `command -q t2c; and t2c init fish | source`
	}
	existing, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	if strings.Contains(string(existing), setupMarker) {
		return file, false, nil
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return "", false, err
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "\n%s\n%s%s\n", setupComment, pathLine(shell), line)
	return file, err == nil, err
}

// Remove takes t2c's lines out of every shell's startup file, leaving everything
// else in place. It reports the files it changed and any that still mention t2c.
func Remove() (changed, leftover []string, err error) {
	for _, name := range []string{"zsh", "bash", "fish"} {
		file, err := startupFile(name)
		if err != nil {
			return changed, leftover, err
		}
		data, err := os.ReadFile(file)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return changed, leftover, err
		}
		updated := setupBlock.ReplaceAll(data, nil)
		// Fish gets a file of its own, so an empty one has no reason to stay.
		if name == "fish" && len(strings.TrimSpace(string(updated))) == 0 {
			if err := os.Remove(file); err != nil {
				return changed, leftover, err
			}
			changed = append(changed, file)
			continue
		}
		if len(updated) != len(data) {
			// Writing in place keeps the file's permissions and any symlink.
			if err := os.WriteFile(file, updated, 0o644); err != nil {
				return changed, leftover, err
			}
			changed = append(changed, file)
		}
		if strings.Contains(string(updated), setupMarker) {
			leftover = append(leftover, file)
		}
	}
	return changed, leftover, nil
}

// PruneSessions removes state left behind by shells that have closed, since it
// can hold a command's error output.
func PruneSessions() {
	dir, err := config.SessionsDir()
	if err != nil {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			os.RemoveAll(filepath.Join(dir, e.Name()))
			continue
		}
		// A reused process ID shouldn't keep old state around forever.
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > 30*24*time.Hour {
			os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}
