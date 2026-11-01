# t2c: text to command

Stop looking up flags. Type what you want at your normal prompt, press **Ctrl+G**, and your words turn into a command. You read it, then press Enter to run it.

```text
$ find all PDFs bigger than 10MB in this folder     ← press Ctrl+G
$ find . -name "*.pdf" -size +10M                   ← press Enter to run
```

Nothing runs until you press Enter. t2c works inside your existing terminal on macOS and Linux, including over SSH.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/mr-ahsan-ghaffar/text2command/main/install.sh | sh
```

Then open a new terminal and connect an AI model:

```bash
t2c login
```

To build from source instead, you need Go:

```bash
make build
```

Then copy `bin/t2c` somewhere on your `PATH` and run `t2c setup`.

## Using it

| You do | What happens |
|---|---|
| Type a request and press Ctrl+G | Your words are replaced with a command |
| Press Enter | The command runs |
| Edit the command | It works like anything you typed yourself |
| Press Ctrl+G again | Your original words come back |
| Press Ctrl+G on an empty line after a failure | You get a corrected command |
| Type `fix: only in Documents` and press Ctrl+G | The last command is corrected using your hint, even if it didn't fail |

You can also run `t2c "your request"` to print a command without the shortcut. To use a different shortcut, change the line t2c added to your shell's startup file, for example to `t2c init zsh --key ctrl-t`.

### When a command fails

```text
$ find . -name "*.pdf" -size +10MB
find: invalid -size argument: '+10MB'
$                                     ← press Ctrl+G
$ find . -name "*.pdf" -size +10M     ← press Enter to run
```

t2c never re-runs anything by itself. The corrected command waits for you to press Enter.

### Danger warnings

Commands that look destructive, like `rm -rf`, `dd`, or `git push --force`, get a warning before you press Enter. These checks are pattern-based and can miss things, so you still need to read the command.

## Connecting a model

Run `t2c login` and choose, or name a provider directly:

| Provider | How you sign in | How it's paid for |
|---|---|---|
| `chatgpt` | Browser | Your ChatGPT Plus or Pro plan |
| `openrouter` | Browser | Prepaid OpenRouter credits, with access to many models including Claude and Gemini |
| `openai` | API key | OpenAI API billing |
| `anthropic` | API key | Anthropic API billing |
| `gemini` | API key | Google AI Studio |
| `ollama` | None | Free, runs on your machine |
| `custom` | Address and API key | Any OpenAI-compatible service |

**Why not Claude or Gemini subscriptions?** Anthropic and Google don't allow third-party apps to sign in with those subscriptions, and accounts can be restricted for doing so. Use an API key or OpenRouter for those models.

**ChatGPT sign-in is a preview.** It relies on OpenAI's program for open-source apps, which may change.

For scripts, pass the key on standard input so it stays out of your shell history:

```bash
t2c login openai --key-stdin --model gpt-6.1-sol < keyfile
```

Change models any time with `t2c model`. The default for Anthropic is `claude-opus-5-5`. `claude-haiku-5-5` is cheaper and often answers faster.

### On a server

API keys work directly on a server: run `t2c login` there.

Browser sign-in needs a browser, so do one of these:

- **Copy your login.** Install t2c on both machines, sign in on your computer, then run `t2c login --push user@server`.
- **Tunnel it.** Run `t2c login` on the server and follow the `ssh -L` instructions it prints.

ChatGPT logins renew by replacing their token, so sharing one login between machines can sign one of them out. If that happens, run `t2c login chatgpt` on that machine.

## Privacy

| Sent to your AI provider | When |
|---|---|
| What you typed, your OS, shell, and current folder path | Each time you press Ctrl+G |
| The failed command, its exit code, and the last 20 lines of its error output (2 KB at most) | Only when you ask for a fix |

Your file contents and shell history aren't sent. Text that looks like a password or key is removed from the failed command and error output first, but this can't catch everything, so don't rely on it for highly sensitive output.

Your login is stored in `~/.config/t2c/`, readable only by your user account. It's a plain file rather than the system keychain, so it also works on servers. The error output of the last generated command is kept in `~/.local/state/t2c/`, also private to you. It's removed the next time you open a terminal after that shell has closed.

## Limitations

- **Shells:** zsh, bash 4.1+, and fish are supported. macOS ships bash 3.2, which can't replace text on the prompt, so there use zsh or `t2c "your request"`.
- **Fish:** a fix sends the command and exit code without error text.
- **Commands you typed yourself:** a fix gets the command and exit code, but not the error text. Recording everything would break interactive tools, so t2c only records output for commands it generated.
- **Progress bars:** while a generated command runs, its error output goes through t2c rather than straight to the terminal. Some tools, like `git clone`, hide their progress bars or colors when this happens.

## Uninstall

```bash
t2c uninstall
```

This removes the program, your saved logins and settings, saved command output, and the lines t2c added to your shell's startup files. Everything else in those files stays as it was. An OpenRouter key stays active until you delete it on OpenRouter's site.

## Development

```bash
make test
```

```bash
make e2e
```

`make e2e` drives real shells through a pseudo-terminal against a fake AI, so it needs no API key. Tagging a commit `v*` builds the release files through GitHub Actions.

## License

MIT
