package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"t2c/internal/auth"
	"t2c/internal/config"
	"t2c/internal/llm"
	"t2c/internal/shell"
)

type providerInfo struct {
	id, label string
}

var providers = []providerInfo{
	{config.ChatGPT, "ChatGPT Plus or Pro (use your subscription, browser sign-in)"},
	{config.OpenRouter, "OpenRouter (many models including Claude and Gemini, prepaid credits, browser sign-in)"},
	{config.OpenAI, "OpenAI API key"},
	{config.Anthropic, "Anthropic API key (Claude)"},
	{config.Gemini, "Google Gemini API key"},
	{config.Ollama, "Ollama (local models, no key)"},
	{config.Custom, "Another OpenAI-compatible service (address and key)"},
}

var stdin = bufio.NewReader(os.Stdin)

func cmdLogin(args []string) error {
	var provider string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		provider, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	keyStdin := fs.Bool("key-stdin", false, "read the API key from standard input")
	model := fs.String("model", "", "model to use")
	baseURL := fs.String("base-url", "", "API address (custom and ollama)")
	push := fs.String("push", "", "copy the current login to this SSH host")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *push != "" {
		return pushLogin(*push)
	}
	if provider == "" {
		var err error
		if provider, err = chooseProvider(); err != nil {
			return err
		}
	}
	if !slices.ContainsFunc(providers, func(p providerInfo) bool { return p.id == provider }) {
		return fmt.Errorf("unknown provider %q. Choose from: chatgpt, openrouter, openai, anthropic, gemini, ollama, custom", provider)
	}

	ctx, cancel := signalContext(10 * time.Minute)
	defer cancel()

	cfg := config.Config{Provider: provider, Model: *model, BaseURL: *baseURL}
	if cfg.BaseURL == "" && provider == config.Custom {
		v, err := ask("API address (for example https://host/v1): ")
		if err != nil {
			return err
		}
		cfg.BaseURL = v
	}
	var creds config.Credentials
	switch provider {
	case config.ChatGPT:
		session, err := auth.LoginChatGPT(ctx, os.Stdout)
		if err != nil {
			return err
		}
		creds.ChatGPT = session
	case config.OpenRouter:
		if *keyStdin {
			key, err := readKey(true, provider)
			if err != nil {
				return err
			}
			creds.APIKey = key
			break
		}
		key, err := auth.LoginOpenRouter(ctx, os.Stdout)
		if err != nil {
			return err
		}
		creds.APIKey = key
	case config.Ollama:
	default:
		key, err := readKey(*keyStdin, provider)
		if err != nil {
			return err
		}
		creds.APIKey = key
	}

	// ChatGPT reads its login from the store, so it has to be saved before the
	// model list can be fetched. Other providers only save once the key works.
	if provider == config.ChatGPT {
		if err := saveCredentials(provider, creds); err != nil {
			return err
		}
	}
	client, err := llm.New(cfg, creds)
	if err != nil {
		return err
	}
	models, err := client.Models(ctx)
	if err != nil {
		return fmt.Errorf("couldn't confirm this login: %w", err)
	}
	if cfg.Model == "" {
		if cfg.Model, err = chooseModel(provider, models, *keyStdin); err != nil {
			return err
		}
	}
	if err := saveCredentials(provider, creds); err != nil {
		return err
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Printf("Signed in to %s, using model %s.\n", provider, cfg.Model)
	if d, err := config.Dir(); err == nil {
		fmt.Printf("Your login is saved in %s, readable only by your user account.\n", d)
	}
	return nil
}

func saveCredentials(provider string, creds config.Credentials) error {
	unlock, err := config.LockCredentials()
	if err != nil {
		return err
	}
	defer unlock()
	store, err := config.LoadStore()
	if err != nil {
		return err
	}
	store[provider] = creds
	return config.SaveStore(store)
}

func chooseProvider() (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("name a provider, for example: t2c login openai --key-stdin")
	}
	fmt.Println("Choose how t2c connects to an AI model:")
	fmt.Println()
	for i, p := range providers {
		fmt.Printf("  %d. %s\n", i+1, p.label)
	}
	fmt.Println()
	fmt.Println("Claude and Gemini subscriptions can't sign in here because Anthropic and")
	fmt.Println("Google don't allow it in other apps. Use an API key or OpenRouter for those.")
	fmt.Println()
	for {
		v, err := ask("Number: ")
		if err != nil {
			return "", err
		}
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= len(providers) {
			return providers[n-1].id, nil
		}
		fmt.Println("Please enter a number from the list.")
	}
}

func chooseModel(provider string, models []string, scripted bool) (string, error) {
	options := models
	switch provider {
	case config.Anthropic:
		options = llm.AnthropicModels
	case config.OpenRouter:
		options = nil // There are hundreds, so start from the automatic router.
	case config.OpenAI, config.Gemini:
		options = textModels(models)
	}
	fallback := llm.DefaultModel(provider)
	if fallback == "" && len(options) > 0 {
		fallback = options[0]
	}
	if fallback == "" {
		if provider == config.Ollama {
			return "", errors.New("Ollama has no models yet. Install one with: ollama pull <model>")
		}
		return "", errors.New("no models found. Choose one with --model")
	}
	if scripted || !term.IsTerminal(int(os.Stdin.Fd())) || len(options) < 2 {
		return fallback, nil
	}
	return promptModel(options, fallback)
}

// textModels drops models that can't write text and puts the newest names first,
// so that the default isn't simply the oldest model in the list.
func textModels(models []string) []string {
	skip := []string{"embed", "audio", "image", "realtime", "transcribe", "tts", "moderation", "whisper", "dall-e", "sora", "veo", "imagen"}
	out := slices.DeleteFunc(slices.Clone(models), func(m string) bool {
		return slices.ContainsFunc(skip, func(s string) bool { return strings.Contains(m, s) })
	})
	slices.Sort(out)
	slices.Reverse(out)
	return out
}

func promptModel(options []string, current string) (string, error) {
	const maxShown = 30
	fmt.Println()
	fmt.Println("Choose a model:")
	for i, m := range options {
		if i == maxShown {
			fmt.Printf("  ... and %d more. You can type any model's name.\n", len(options)-maxShown)
			break
		}
		fmt.Printf("  %d. %s\n", i+1, m)
	}
	v, err := ask(fmt.Sprintf("Number or name [%s]: ", current))
	if err != nil {
		return "", err
	}
	if v == "" {
		return current, nil
	}
	if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= len(options) {
		return options[n-1], nil
	}
	return v, nil
}

func ask(prompt string) (string, error) {
	fmt.Print(prompt)
	line, err := stdin.ReadString('\n')
	if err != nil && (line == "" || !errors.Is(err, io.EOF)) {
		return "", errors.New("no answer was given")
	}
	return strings.TrimSpace(line), nil
}

// readKey reads an API key without showing it or leaving it in shell history.
func readKey(fromStdin bool, provider string) (string, error) {
	var key string
	if !fromStdin && term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Printf("Paste your %s API key (it won't be shown): ", provider)
		data, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", err
		}
		key = string(data)
	} else {
		line, err := stdin.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		key = line
	}
	key = strings.TrimSpace(key)
	if key == "" && provider != config.Custom {
		return "", errors.New("no API key was entered")
	}
	return key, nil
}

func cmdLogout(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	provider := cfg.Provider
	if len(args) > 0 {
		provider = args[0]
	}
	if provider == "" {
		return errors.New("not signed in")
	}
	unlock, err := config.LockCredentials()
	if err != nil {
		return err
	}
	defer unlock()
	store, err := config.LoadStore()
	if err != nil {
		return err
	}
	if s := store[provider].ChatGPT; s != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		auth.RevokeChatGPT(ctx, s) // The local copy is removed either way.
	}
	delete(store, provider)
	if err := config.SaveStore(store); err != nil {
		return err
	}
	if cfg.Provider == provider {
		if err := config.Save(config.Config{}); err != nil {
			return err
		}
	}
	fmt.Printf("Removed the saved login for %s.\n", provider)
	if provider == config.OpenRouter {
		fmt.Println("The key itself stays active until you delete it at openrouter.ai/settings/keys.")
	}
	return nil
}

func cmdStatus() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Provider == "" {
		fmt.Println("Not signed in. Run: t2c login")
		return nil
	}
	fmt.Printf("Provider: %s\nModel:    %s\n", cfg.Provider, cfg.Model)
	if cfg.BaseURL != "" {
		fmt.Printf("Address:  %s\n", cfg.BaseURL)
	}
	store, err := config.LoadStore()
	if err != nil {
		return err
	}
	if s := store[cfg.Provider].ChatGPT; s != nil && s.Email != "" {
		fmt.Printf("Account:  %s\n", s.Email)
	}
	return nil
}

func cmdModel(args []string) error {
	client, cfg, err := activeClient()
	if err != nil {
		return err
	}
	if len(args) > 0 {
		cfg.Model = args[0]
	} else {
		fmt.Printf("Current model: %s\n", cfg.Model)
		ctx, cancel := signalContext(30 * time.Second)
		defer cancel()
		models, err := client.Models(ctx)
		if err != nil {
			return err
		}
		if cfg.Provider == config.Anthropic {
			models = llm.AnthropicModels
		}
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			for _, m := range models {
				fmt.Println(m)
			}
			return nil
		}
		if cfg.Model, err = promptModel(models, cfg.Model); err != nil {
			return err
		}
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Printf("Using model %s.\n", cfg.Model)
	return nil
}

func cmdSetup(args []string) error {
	name := shell.Detect()
	if len(args) > 0 {
		name = args[0]
	}
	file, changed, err := shell.Setup(name)
	if err != nil {
		return err
	}
	if !changed {
		fmt.Printf("The shortcut is already set up in %s.\n", file)
		return nil
	}
	fmt.Printf("Added the shortcut to %s. Open a new terminal to start using it.\n", file)
	return nil
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	key := fs.String("key", "ctrl-g", "shortcut to use")
	var name string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if name == "" {
		return errors.New("name a shell: bash, zsh, or fish")
	}
	script, err := shell.Init(name, *key)
	if err != nil {
		return err
	}
	shell.PruneSessions()
	fmt.Print(script)
	return nil
}

// transfer is what --push sends to the other machine. The machine's host ID
// isn't included, since OpenAI asks that each machine keeps its own.
type transfer struct {
	Config      config.Config      `json:"config"`
	Credentials config.Credentials `json:"credentials"`
}

func pushLogin(target string) error {
	if strings.HasPrefix(target, "-") {
		return errors.New("that doesn't look like an SSH host")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Provider == "" {
		return errors.New("sign in on this machine first. Run: t2c login")
	}
	store, err := config.LoadStore()
	if err != nil {
		return err
	}
	data, err := json.Marshal(transfer{Config: cfg, Credentials: store[cfg.Provider]})
	if err != nil {
		return err
	}
	// Non-interactive SSH sessions often leave these out of PATH.
	remote := `PATH="$HOME/.local/bin:/usr/local/bin:$PATH" t2c _import`
	cmd := exec.Command("ssh", "--", target, remote)
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("couldn't copy the login. Check that you can SSH to %s and that t2c is installed there", target)
	}
	fmt.Printf("Copied your %s login to %s.\n", cfg.Provider, target)
	if cfg.Provider == config.ChatGPT {
		fmt.Println("ChatGPT logins renew by replacing their token, so one copy can stop working.")
		fmt.Println("If this machine asks you to sign in again, run: t2c login chatgpt")
	}
	return nil
}

func cmdImport() error {
	var t transfer
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&t); err != nil {
		return err
	}
	if t.Config.Provider == "" {
		return errors.New("nothing to import")
	}
	if err := saveCredentials(t.Config.Provider, t.Credentials); err != nil {
		return err
	}
	if s := t.Credentials.ChatGPT; s != nil {
		if err := config.WriteFile("chatgpt-client-id", []byte(s.ClientID)); err != nil {
			return err
		}
		if _, err := config.HostID(); err != nil {
			return err
		}
	}
	return config.Save(t.Config)
}
