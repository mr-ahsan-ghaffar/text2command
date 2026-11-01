// Package config stores t2c's settings and credentials on disk.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Provider IDs.
const (
	ChatGPT    = "chatgpt"
	OpenRouter = "openrouter"
	OpenAI     = "openai"
	Anthropic  = "anthropic"
	Gemini     = "gemini"
	Ollama     = "ollama"
	Custom     = "custom"
)

// Config holds the active provider. It contains no secrets.
type Config struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	BaseURL  string `json:"base_url,omitempty"`
}

// ChatGPTSession is one Sign in with ChatGPT login.
type ChatGPTSession struct {
	Email        string    `json:"email,omitempty"`
	Subject      string    `json:"subject,omitempty"`
	ClientID     string    `json:"client_id"`
	IDToken      string    `json:"id_token,omitempty"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	Scopes       []string  `json:"scopes,omitempty"`
}

// Credentials holds the secret for one provider.
type Credentials struct {
	APIKey  string          `json:"api_key,omitempty"`
	ChatGPT *ChatGPTSession `json:"chatgpt,omitempty"`
}

// Store maps provider IDs to their credentials.
type Store map[string]Credentials

// Dir returns the private directory for settings and credentials.
func Dir() (string, error) {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "t2c"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "t2c"), nil
}

// SessionsDir returns the directory holding per-shell state.
func SessionsDir() (string, error) {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "t2c", "s"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "t2c", "s"), nil
}

func path(name string) (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, name), nil
}

// Load reads the settings. A missing file gives an empty Config.
func Load() (Config, error) {
	var c Config
	err := readJSON("config.json", &c)
	return c, err
}

// Save writes the settings.
func Save(c Config) error {
	return writeJSON("config.json", c)
}

// LoadStore reads the credentials. A missing file gives an empty Store.
func LoadStore() (Store, error) {
	s := Store{}
	err := readJSON("credentials.json", &s)
	return s, err
}

// SaveStore writes the credentials.
func SaveStore(s Store) error {
	return writeJSON("credentials.json", s)
}

func readJSON(name string, v any) error {
	p, err := path(name)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s is damaged: %w", p, err)
	}
	return nil
}

func writeJSON(name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteFile(name, append(data, '\n'))
}

// ReadFile reads a file from the config directory. A missing file gives "".
func ReadFile(name string) (string, error) {
	p, err := path(name)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(data), err
}

// WriteFile atomically writes a file readable only by the current user.
func WriteFile(name string, data []byte) error {
	p, err := path(name)
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, name+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// HostID returns this machine's stable random ID, creating it on first use.
func HostID() (string, error) {
	p, err := path("host-id")
	if err != nil {
		return "", err
	}
	if data, err := os.ReadFile(p); err == nil && len(data) > 0 {
		return string(data), nil
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	id := fmt.Sprintf("urn:uuid:%s-%s-%s-%s-%s", h[:8], h[8:12], h[12:16], h[16:20], h[20:])
	if err := WriteFile("host-id", []byte(id)); err != nil {
		return "", err
	}
	return id, nil
}

// LockCredentials takes an exclusive lock so that two shells can't refresh
// a rotating token at the same time. Call the returned function to release it.
func LockCredentials() (func(), error) {
	p, err := path("credentials.lock")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}
