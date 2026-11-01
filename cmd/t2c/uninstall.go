package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"t2c/internal/auth"
	"t2c/internal/config"
	"t2c/internal/shell"
)

// cmdUninstall removes everything t2c added to this machine.
func cmdUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "don't ask for confirmation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	configDir, err := config.Dir()
	if err != nil {
		return err
	}
	sessions, err := config.SessionsDir()
	if err != nil {
		return err
	}
	stateDir := filepath.Dir(sessions)
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(binary); err == nil {
		binary = resolved
	}

	if !*yes {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("run this in a terminal to confirm, or add --yes")
		}
		fmt.Println("This removes:")
		fmt.Println("  - the t2c lines in your shell's startup files")
		fmt.Printf("  - your saved logins and settings in %s\n", configDir)
		fmt.Printf("  - saved command output in %s\n", stateDir)
		fmt.Printf("  - the program at %s\n", binary)
		answer, err := ask("Continue? [y/N]: ")
		if err != nil {
			return err
		}
		if a := strings.ToLower(answer); a != "y" && a != "yes" {
			fmt.Println("Nothing was removed.")
			return nil
		}
	}

	// End the ChatGPT session and keep any note about keys before deleting the login.
	store, err := config.LoadStore()
	if err != nil {
		return err
	}
	if s := store[config.ChatGPT].ChatGPT; s != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		auth.RevokeChatGPT(ctx, s) // The local copy is removed either way.
	}
	_, hadOpenRouter := store[config.OpenRouter]

	changed, leftover, err := shell.Remove()
	if err != nil {
		return err
	}
	for _, f := range changed {
		fmt.Printf("Updated %s\n", f)
	}
	for _, dir := range []string{configDir, stateDir} {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		fmt.Printf("Removed %s\n", dir)
	}
	if err := os.Remove(binary); err != nil {
		fmt.Printf("Couldn't remove the program. To remove it yourself, run:\n  sudo rm %q\n", binary)
	} else {
		fmt.Printf("Removed %s\n", binary)
	}

	for _, f := range leftover {
		fmt.Printf("%s still mentions t2c init. It looks hand-edited, so please remove that line yourself.\n", f)
	}
	if hadOpenRouter {
		fmt.Println("Your OpenRouter key stays active until you delete it at openrouter.ai/settings/keys.")
	}
	fmt.Println("Done. Open a new terminal to finish.")
	return nil
}
