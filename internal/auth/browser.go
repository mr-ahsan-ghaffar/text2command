// Package auth handles browser logins for providers that support them.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"time"
)

const loginTimeout = 5 * time.Minute

func randomString() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func newPKCE() (verifier, challenge string, err error) {
	if verifier, err = randomString(); err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// Remote reports whether this machine likely has no browser of its own.
func Remote() bool {
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
		return true
	}
	return runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == ""
}

// Show opens the login page, or explains how to reach it from a remote machine.
func Show(out io.Writer, target string, port int) {
	if Remote() {
		fmt.Fprintf(out, "This machine has no browser. On your computer, open a second terminal and run:\n\n")
		fmt.Fprintf(out, "  ssh -N -L %d:127.0.0.1:%d <this-server>\n\n", port, port)
		fmt.Fprintf(out, "Then open this link in your computer's browser:\n\n  %s\n\n", target)
		fmt.Fprintf(out, "Or sign in on your computer and copy the login here with:\n\n  t2c login --push <user@this-server>\n\n")
		return
	}
	fmt.Fprintf(out, "Opening your browser to sign in. If it doesn't open, use this link:\n\n  %s\n\n", target)
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, target)
	if err := cmd.Start(); err == nil {
		go cmd.Wait()
	}
}

const donePage = `<!doctype html><meta charset="utf-8"><title>t2c</title><p style="font:16px system-ui;margin:3em">%s You can close this tab and go back to your terminal.</p>`

// waitForCallback serves the local redirect and returns its query parameters.
// When state is non-empty, the redirect must carry the same value.
func waitForCallback(ctx context.Context, ln net.Listener, path, state string) (url.Values, error) {
	ctx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()

	type result struct {
		params url.Values
		err    error
	}
	done := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var err error
		switch {
		case state != "" && q.Get("state") != state:
			http.Error(w, "This sign-in link doesn't match. Please try again.", http.StatusBadRequest)
			return // A stray request must not end a real login.
		case q.Get("error") == "access_denied":
			err = errors.New("sign-in was cancelled")
		case q.Get("error") != "":
			err = errors.New("sign-in failed: " + q.Get("error"))
		case q.Get("code") == "":
			http.Error(w, "No sign-in code was received.", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err != nil {
			fmt.Fprintf(w, donePage, "Sign-in didn't finish.")
		} else {
			fmt.Fprintf(w, donePage, "You're signed in to t2c.")
		}
		select {
		case done <- result{q, err}:
		default:
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer srv.Close()

	select {
	case r := <-done:
		return r.params, r.err
	case <-ctx.Done():
		return nil, errors.New("timed out waiting for the browser sign-in")
	}
}
