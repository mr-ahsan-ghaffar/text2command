// EDNS is off because some DNS forwarders, including VMware's NAT, return malformed
// replies to it, and this build's own resolver then reports "no such host".
//
//go:debug netedns0=0

// Command t2c turns plain language into shell commands.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"t2c/internal/command"
	"t2c/internal/config"
	"t2c/internal/llm"
	"t2c/internal/shell"
)

// version is set at build time.
var version = "dev"

const usage = `t2c turns plain language into shell commands.

Type what you want at your normal prompt and press Ctrl+G. Your words are
replaced with a command. Press Enter to run it, or Ctrl+G again to undo.
If a command fails, press Ctrl+G on the empty line to get a corrected one.

Usage:
  t2c login [provider]        Connect an AI provider
  t2c login --push user@host  Copy your login to a server over SSH
  t2c logout [provider]       Remove a saved login
  t2c status                  Show the current provider and model
  t2c model [name]            Show or change the model
  t2c setup [shell]           Add the Ctrl+G shortcut to your shell
  t2c init <shell>            Print the shell hook (--key ctrl-x to change the shortcut)
  t2c "your request"          Print a command without using the shortcut
  t2c uninstall               Remove t2c and everything it added
  t2c version                 Show the version

Providers: chatgpt, openrouter, openai, anthropic, gemini, ollama, custom
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "t2c: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	case "version", "--version":
		fmt.Println("t2c " + version)
		return nil
	case "login":
		return cmdLogin(args[1:])
	case "logout":
		return cmdLogout(args[1:])
	case "status":
		return cmdStatus()
	case "model":
		return cmdModel(args[1:])
	case "setup":
		return cmdSetup(args[1:])
	case "init":
		return cmdInit(args[1:])
	case "uninstall":
		return cmdUninstall(args[1:])
	case "_gen":
		return cmdGen(args[1:])
	case "_tee":
		return cmdTee(args[1:])
	case "_import":
		return cmdImport()
	case "--":
		args = args[1:]
	}
	return cmdAsk(args)
}

func signalContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	return ctx, func() { cancel(); stop() }
}

// activeClient builds a client from the saved login.
func activeClient() (llm.Client, config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, cfg, err
	}
	if cfg.Provider == "" {
		return nil, cfg, errors.New("not signed in. Run: t2c login")
	}
	store, err := config.LoadStore()
	if err != nil {
		return nil, cfg, err
	}
	client, err := llm.New(cfg, store[cfg.Provider])
	return client, cfg, err
}

// generate asks the AI for a command and returns it with any warning.
func generate(ctx context.Context, prompt string) (cmd, warning string, err error) {
	client, _, err := activeClient()
	if err != nil {
		return "", "", err
	}
	reply, err := client.Complete(ctx, command.SystemPrompt, prompt)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", "", errors.New("the AI took too long to answer. Try again")
		}
		return "", "", err
	}
	if cmd, err = command.Clean(reply); err != nil {
		return "", "", err
	}
	return cmd, command.Warn(cmd), nil
}

func cmdAsk(args []string) error {
	request := joinWords(args)
	ctx, cancel := signalContext(generateTimeout)
	defer cancel()
	cmd, warning, err := generate(ctx, command.ConvertPrompt(command.Detect(shell.Detect()), request))
	if err != nil {
		return err
	}
	if warning != "" {
		fmt.Fprintln(os.Stderr, "t2c: "+warning)
	}
	fmt.Println(cmd)
	return nil
}
