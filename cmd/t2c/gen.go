package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"t2c/internal/command"
)

const (
	generateTimeout = 60 * time.Second
	fixPrefix       = "fix:"
	exitInterrupted = 130

	teeKeep  = 8 << 10
	teeLimit = 64 << 10
)

// lastRequest remembers what the current shell last asked for.
type lastRequest struct {
	Request string `json:"request"`
	Command string `json:"command"`
}

func joinWords(args []string) string {
	return strings.TrimSpace(strings.Join(args, " "))
}

// cmdGen is what the shell shortcut calls. On success it prints a warning line
// (often empty) and then the command. On failure it prints only a message, so
// the shell can show it without changing what the user typed.
func cmdGen(args []string) error {
	fs := flag.NewFlagSet("_gen", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	shellName := fs.String("shell", "", "")
	dir := fs.String("dir", "", "")
	lastCmd := fs.String("last-cmd", "", "")
	lastExit := fs.Int("last-exit", 0, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	input := strings.TrimSpace(fs.Arg(0))

	prompt, request, err := buildPrompt(*shellName, *dir, input, strings.TrimSpace(*lastCmd), *lastExit)
	if err == nil {
		ctx, cancel := signalContext(generateTimeout)
		defer cancel()
		var cmd, warning string
		if cmd, warning, err = generate(ctx, prompt); err == nil {
			saveLast(*dir, lastRequest{Request: request, Command: cmd})
			fmt.Printf("%s\n%s\n", warning, cmd)
			return nil
		}
		if errors.Is(err, context.Canceled) {
			os.Exit(exitInterrupted)
		}
	}
	fmt.Println(err.Error())
	os.Exit(1)
	return nil
}

// buildPrompt decides what the shortcut means here. An empty line asks to fix
// the last failure, "fix: ..." does the same with a hint, and anything else is
// a new request. It also returns the request to remember for later fixes.
func buildPrompt(shellName, dir, input, lastCmd string, lastExit int) (prompt, request string, err error) {
	env := command.Detect(shellName)
	hint, isFix := strings.CutPrefix(input, fixPrefix)
	if input != "" && !isFix {
		return command.ConvertPrompt(env, input), input, nil
	}
	hint = strings.TrimSpace(hint)

	if lastCmd == "" {
		return "", "", errors.New("type what you want first, then press the shortcut")
	}
	if input == "" && lastExit == 0 {
		return "", "", errors.New(`the last command worked. To change it, type "fix: what's wrong" and press the shortcut`)
	}
	if input == "" && lastExit == exitInterrupted {
		return "", "", errors.New(`you cancelled the last command. To change it, type "fix: what's wrong" and press the shortcut`)
	}

	last := loadLast(dir)
	output := readCapture(dir)
	// The original words only help if they led to this command. Captured output
	// means the shortcut generated it, even if it was edited before running.
	if last.Command == lastCmd || output != "" {
		request = last.Request
	}
	return command.FixPrompt(env, command.Failure{
		Request:  request,
		Command:  lastCmd,
		ExitCode: lastExit,
		Output:   output,
		Hint:     hint,
	}), request, nil
}

func loadLast(dir string) lastRequest {
	var l lastRequest
	if dir != "" {
		if data, err := os.ReadFile(filepath.Join(dir, "last.json")); err == nil {
			json.Unmarshal(data, &l)
		}
	}
	return l
}

func saveLast(dir string, l lastRequest) {
	if dir == "" {
		return
	}
	if data, err := json.Marshal(l); err == nil {
		os.WriteFile(filepath.Join(dir, "last.json"), data, 0o600)
	}
}

func readCapture(dir string) string {
	if dir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, "stderr"))
	if err != nil {
		return ""
	}
	return string(data)
}

// cmdTee passes error output through to the terminal unchanged and keeps a copy
// of the most recent part. The size limit stops a noisy command filling the disk.
func cmdTee(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: t2c _tee <file>")
	}
	// Ctrl+C is meant for the command. This must outlive it to print its last words.
	signal.Ignore(os.Interrupt, syscall.SIGHUP, syscall.SIGPIPE)
	f, err := os.OpenFile(args[0], os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		// Without a copy the output must still reach the terminal.
		_, err = io.Copy(os.Stderr, os.Stdin)
		return err
	}
	defer f.Close()

	var tail []byte
	size := 0
	buf := make([]byte, 32<<10)
	for {
		n, readErr := os.Stdin.Read(buf)
		if n > 0 {
			os.Stderr.Write(buf[:n])
			f.Write(buf[:n])
			size += n
			tail = append(tail, buf[:n]...)
			if len(tail) > teeKeep {
				tail = tail[len(tail)-teeKeep:]
			}
			if size > teeLimit {
				if f.Truncate(0) == nil {
					f.Write(tail)
					size = len(tail)
				}
			}
		}
		if readErr != nil {
			return nil
		}
	}
}
