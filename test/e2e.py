#!/usr/bin/env python3
"""End-to-end test: drives real shells through a pseudo-terminal against a fake AI.

Usage: test/e2e.py <path-to-t2c> <zsh|bash|fish>[=/path/to/shell] ...
"""
import hashlib
import http.server
import json
import os
import pty
import re
import select
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time

CTRL_G = b"\x07"
CTRL_U = b"\x15"
REQUESTS = []

# What the fake AI answers, chosen by a word in the request.
REPLIES = {
    "make-marker": "echo generated-marker",
    "echo original-words": "echo generated-words",
    "show-a-failure": "ls /nonexistent-t2c-path",
    "leak-a-secret": "sh -c 'echo password=hunter2 >&2; exit 3'",
    "delete-things": "rm -rf /tmp/t2c-e2e-does-not-exist",
    "impossible": "T2C_ERROR: no command can do that",
    "check-status": "sh -c 'exit 7'",
    "tty-check": "sh -c 'test -t 1 && echo stdout-is-tty'",
    "multi-line": "echo line-one\necho line-two",
    "fenced": "```bash\necho from-fence\n```",
    "hide-escapes": "echo clean\x1b]0;evil\x07-output",
}


class FakeAI(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def send_json(self, value):
        data = json.dumps(value).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        self.send_json({"data": [{"id": "fake-model"}]})

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        prompt = body["messages"][1]["content"]
        REQUESTS.append(prompt)
        reply = "echo no-rule-matched"
        if "Command that needs fixing" in prompt:
            reply = "echo fixed-command"
        else:
            request = prompt.split("Request: ", 1)[1]
            if "slow-request" in request:
                time.sleep(20)
            for word, answer in REPLIES.items():
                if word in request:
                    reply = answer
        self.send_json({"choices": [{"message": {"content": reply}}]})


class Term:
    """A shell in a pseudo-terminal, with just enough of expect."""

    def __init__(self, argv, env):
        self.pid, self.fd = pty.fork()
        if self.pid == 0:
            os.execvpe(argv[0], argv, env)
        self.seen = b""

    def pump(self, seconds):
        end = time.time() + seconds
        while time.time() < end:
            ready, _, _ = select.select([self.fd], [], [], 0.05)
            if ready:
                try:
                    self.seen += os.read(self.fd, 65536)
                except OSError:
                    return

    def send(self, data):
        os.write(self.fd, data)

    def expect(self, text, timeout=10):
        text = text.encode()
        end = time.time() + timeout
        while time.time() < end:
            if text in self.seen:
                # Later checks only look at what comes after this match.
                self.seen = self.seen.split(text, 1)[1]
                return True
            self.pump(0.05)
        tail = self.seen[-800:].decode(errors="replace")
        raise AssertionError(f"never saw {text!r}. Recent output:\n{tail}")

    def absent(self, text, wait=0.5):
        self.pump(wait)
        if text.encode() in self.seen:
            raise AssertionError(f"unexpectedly saw {text!r}")

    def clear(self):
        self.pump(0.2)
        self.seen = b""

    def close(self):
        try:
            os.kill(self.pid, signal.SIGKILL)
            os.waitpid(self.pid, 0)
        except OSError:
            pass
        os.close(self.fd)


def fresh_env(t2c_dir, port):
    home = tempfile.mkdtemp(prefix="t2c-e2e-")
    env = {
        "HOME": home,
        "PATH": t2c_dir + ":/usr/bin:/bin:/usr/sbin:/sbin",
        "TERM": "xterm",
        "LANG": "en_US.UTF-8",
    }
    subprocess.run(
        ["t2c", "login", "custom", "--base-url", f"http://127.0.0.1:{port}/v1", "--key-stdin", "--model", "fake-model"],
        input=b"test-key\n", env=env, check=True, capture_output=True,
    )
    return env, home


def run_shell(name, path, t2c_dir, port):
    env, home = fresh_env(t2c_dir, port)
    env["SHELL"] = path
    subprocess.run(["t2c", "setup", name], env=env, check=True, capture_output=True)
    if name == "bash":
        argv = [path, "--noprofile", "--rcfile", os.path.join(home, ".bashrc"), "-i"]
    elif name == "fish":
        env["XDG_CONFIG_HOME"] = os.path.join(home, ".config")
        argv = [path, "-i"]
    else:
        env["ZDOTDIR"] = home
        # Ubuntu's global zshrc runs compinit, which can stop to ask a question
        # about folder permissions and swallow the test's first keystrokes.
        with open(os.path.join(home, ".zshenv"), "w") as f:
            f.write("skip_global_compinit=1\n")
        argv = [path, "-i"]
    term = Term(argv, env)
    term.pump(1.0)
    PROMPT = "READY> "
    if name == "fish":
        term.send(b"function fish_prompt; echo -n 'READY> '; end\r")
    else:
        term.send(b"PS1='READY> '\r")
    term.expect(PROMPT)
    term.clear()
    results = []

    def step(label, fn):
        try:
            term.clear()
            fn()
            results.append((label, None))
        except AssertionError as e:
            results.append((label, str(e)))
            term.send(CTRL_U)
            term.pump(0.3)

    def type_and_convert(text):
        term.send(text.encode())
        term.pump(0.2)
        term.send(CTRL_G)
        term.pump(0.5)

    def convert_and_run():
        type_and_convert("make-marker please")
        term.send(b"\r")
        term.expect("generated-marker\r\n")
        term.expect(PROMPT)

    def nothing_runs_before_enter():
        type_and_convert("make-marker please")
        term.pump(0.5)
        if b"generated-marker\r\n" in term.seen:
            raise AssertionError("the command ran without Enter")
        term.send(CTRL_U)
        term.pump(0.2)

    def undo():
        type_and_convert("echo original-words")
        term.send(CTRL_G)
        term.pump(0.3)
        term.send(b"\r")
        term.expect("original-words\r\n")
        term.expect(PROMPT)

    def edit_after_convert():
        type_and_convert("make-marker please")
        term.send(b"-edited\r")
        term.expect("generated-marker-edited\r\n")
        term.expect(PROMPT)

    def fix_with_error_text():
        REQUESTS.clear()
        type_and_convert("show-a-failure now")
        term.send(b"\r")
        term.expect("nonexistent-t2c-path")
        term.expect(PROMPT)
        term.clear()
        term.send(CTRL_G)
        term.pump(0.6)
        term.send(b"\r")
        term.expect("fixed-command\r\n")
        term.expect(PROMPT)
        prompt = REQUESTS[-1]
        assert "show-a-failure" in prompt, "original request wasn't sent:\n" + prompt
        assert "ls /nonexistent-t2c-path" in prompt, "failed command wasn't sent:\n" + prompt
        assert "Exit code: 0" not in prompt, "wrong exit code:\n" + prompt
        if name == "fish":
            assert "No error output" in prompt, prompt
        else:
            assert "No such file" in prompt, "error text wasn't sent:\n" + prompt

    def fix_hand_typed():
        REQUESTS.clear()
        term.send(b"ls /another-missing-path\r")
        term.expect("another-missing-path")
        term.expect(PROMPT)
        term.clear()
        term.send(CTRL_G)
        term.pump(0.6)
        term.send(b"\r")
        term.expect("fixed-command\r\n")
        term.expect(PROMPT)
        prompt = REQUESTS[-1]
        assert "ls /another-missing-path" in prompt, prompt
        assert "No error output" in prompt, "hand-typed output should not be captured:\n" + prompt
        assert "Original request" not in prompt, "stale request was sent:\n" + prompt

    def fix_hint_after_success():
        REQUESTS.clear()
        term.send(b"echo worked-fine\r")
        term.expect("worked-fine\r\n")
        term.expect(PROMPT)
        term.clear()
        term.send(CTRL_G)
        term.expect("the last command worked")
        term.send(b"fix: only in Documents")
        term.pump(0.2)
        term.send(CTRL_G)
        term.pump(0.6)
        term.send(b"\r")
        term.expect("fixed-command\r\n")
        term.expect(PROMPT)
        assert "only in Documents" in REQUESTS[-1], REQUESTS[-1]

    def secrets_removed():
        if name == "fish":
            return
        REQUESTS.clear()
        type_and_convert("leak-a-secret")
        term.send(b"\r")
        term.expect("password=hunter2")
        term.expect(PROMPT)
        term.clear()
        term.send(CTRL_G)
        term.pump(0.6)
        term.send(b"\r")
        term.expect("fixed-command\r\n")
        term.expect(PROMPT)
        assert "hunter2" not in REQUESTS[-1], REQUESTS[-1]
        assert "Exit code: 3" in REQUESTS[-1], REQUESTS[-1]

    def danger_warning():
        type_and_convert("delete-things")
        term.expect("Careful:")
        term.send(CTRL_U)
        term.pump(0.2)

    def ai_cannot_do_it():
        type_and_convert("impossible request")
        term.expect("no command can do that")
        term.send(CTRL_U)
        term.pump(0.2)

    def exit_status_preserved():
        type_and_convert("check-status")
        term.send(b"\r")
        term.expect(PROMPT)
        term.clear()
        status_var = b"echo status=$status\r" if name == "fish" else b"echo status=$?\r"
        term.send(status_var)
        term.expect("status=7\r\n")
        term.expect(PROMPT)

    def stdout_stays_tty():
        type_and_convert("tty-check")
        term.send(b"\r")
        term.expect("stdout-is-tty\r\n")
        term.expect(PROMPT)

    def stderr_works_after():
        # The shell's own stderr must be back to normal after a generated command.
        type_and_convert("show-a-failure now")
        term.send(b"\r")
        term.expect(PROMPT)
        term.clear()
        term.send(b"ls /third-missing-path\r")
        term.expect("third-missing-path")
        term.expect(PROMPT)

    def special_characters():
        REQUESTS.clear()
        text = """it's "quoted" $HOME `date` \\ * ; | & make-marker"""
        type_and_convert(text)
        term.send(b"\r")
        term.expect("generated-marker\r\n")
        term.expect(PROMPT)
        assert text in REQUESTS[-1], "request changed on the way:\n" + REQUESTS[-1]

    def ctrl_c_cancels():
        type_and_convert("slow-request")
        term.send(b"\x03")
        term.pump(1.0)
        term.send(CTRL_U)
        term.pump(0.2)
        term.send(b"echo alive-after-cancel\r")
        # The fake AI takes 20 seconds, so this only passes if Ctrl+C really cancelled.
        term.expect("alive-after-cancel\r\n", timeout=5)
        term.expect(PROMPT)

    def multi_line_command():
        type_and_convert("multi-line")
        term.send(b"\r")
        term.expect("line-one\r\n")
        term.expect("line-two\r\n")
        term.expect(PROMPT)

    def cleans_ai_output():
        type_and_convert("fenced")
        term.send(b"\r")
        term.expect("from-fence\r\n")
        term.expect(PROMPT)
        term.clear()
        type_and_convert("hide-escapes")
        if b"evil\x07" in term.seen:
            raise AssertionError("an escape sequence reached the terminal")
        term.send(b"\r")
        term.expect("clean-output\r\n")
        term.expect(PROMPT)

    def empty_line_with_no_history():
        # Runs in a new shell, where nothing has been run yet.
        fresh = Term(argv, env)
        fresh.pump(1.0)
        fresh.clear()
        fresh.send(CTRL_G)
        try:
            fresh.expect("type what you want first")
        finally:
            fresh.close()

    def signed_out_message():
        subprocess.run(["t2c", "logout"], env=env, check=True, capture_output=True)
        try:
            type_and_convert("make-marker")
            term.expect("not signed in")
            term.send(CTRL_U)
            term.pump(0.2)
        finally:
            subprocess.run(
                ["t2c", "login", "custom", "--base-url", f"http://127.0.0.1:{port}/v1", "--key-stdin", "--model", "fake-model"],
                input=b"test-key\n", env=env, check=True, capture_output=True,
            )

    for label, fn in [
        ("special characters reach the AI intact", special_characters),
        ("Ctrl+C cancels a slow request", ctrl_c_cancels),
        ("multi-line commands run", multi_line_command),
        ("AI output is cleaned", cleans_ai_output),
        ("message on empty line with no history", empty_line_with_no_history),
        ("message when signed out", signed_out_message),
        ("converts words and runs on Enter", convert_and_run),
        ("nothing runs before Enter", nothing_runs_before_enter),
        ("shortcut again undoes", undo),
        ("generated command can be edited", edit_after_convert),
        ("fix sends error text", fix_with_error_text),
        ("fix works on hand-typed commands", fix_hand_typed),
        ("message after success, fix: hint works", fix_hint_after_success),
        ("secrets removed from fix context", secrets_removed),
        ("danger warning shown", danger_warning),
        ("AI's reason shown when it can't", ai_cannot_do_it),
        ("exit code preserved", exit_status_preserved),
        ("stdout stays a terminal", stdout_stays_tty),
        ("stderr normal afterwards", stderr_works_after),
    ]:
        step(label, fn)

    term.close()
    shutil.rmtree(home, ignore_errors=True)
    return results


def fake_release(t2c):
    """Builds a folder laid out like a GitHub release, for the installer to use."""
    rel = tempfile.mkdtemp(prefix="t2c-rel-")
    system = "darwin" if sys.platform == "darwin" else "linux"
    arch = {"x86_64": "amd64", "amd64": "amd64", "arm64": "arm64", "aarch64": "arm64"}[os.uname().machine]
    name = f"t2c-{system}-{arch}"
    shutil.copy(t2c, os.path.join(rel, name))
    digest = hashlib.sha256(open(t2c, "rb").read()).hexdigest()
    with open(os.path.join(rel, "checksums.txt"), "w") as f:
        f.write(f"{digest}  {name}\n")
    return rel


def install_flow(name, path, t2c, port):
    """One install command must leave Ctrl+G working, with no other step."""
    results = []
    rel = fake_release(t2c)
    installer = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "install.sh"))
    home = tempfile.mkdtemp(prefix="t2c-inst-")
    env = {
        "HOME": home,
        "PATH": "/usr/bin:/bin:/usr/sbin:/sbin",
        "TERM": "xterm",
        "LANG": "en_US.UTF-8",
        "SHELL": path,
        "T2C_BASE_URL": "file://" + rel,
    }
    if name == "zsh":
        env["ZDOTDIR"] = home
        with open(os.path.join(home, ".zshenv"), "w") as f:
            f.write("skip_global_compinit=1\n")
    term = Term(["sh", "-c", f'cat "{installer}" | sh'], env)

    def check(label, fn):
        try:
            fn()
            results.append((label, None))
        except AssertionError as e:
            results.append((label, str(e)))

    def first_install():
        term.expect("Number: ", timeout=20)
        term.send(b"7\r")
        term.expect("API address")
        term.send(f"http://127.0.0.1:{port}/v1\r".encode())
        term.expect("API key")
        term.send(b"test-key\r")
        term.expect("Signed in to custom")
        term.expect("t2c is ready")
        term.absent("exec $SHELL")
        term.pump(1.0)
        term.send(b"function fish_prompt; echo -n 'READY> '; end\r" if name == "fish" else b"PS1='READY> '\r")
        term.expect("READY> ")
        term.clear()
        term.send(b"make-marker please")
        term.pump(0.2)
        term.send(CTRL_G)
        term.pump(0.6)
        term.send(b"\r")
        term.expect("generated-marker\r\n")
        term.expect("READY> ")

    def update_in_same_terminal():
        pid_var = b"$fish_pid" if name == "fish" else b"$$"
        term.clear()
        term.send(b"echo pid=" + pid_var + b"\r")
        term.expect("pid=")
        term.pump(0.3)
        before = term.seen.split(b"\r")[0]
        term.clear()
        term.send(f'cat "{installer}" | sh\r'.encode())
        term.expect("t2c is ready", timeout=20)
        term.absent("Number: ")
        term.expect("READY> ")
        term.clear()
        term.send(b"echo pid=" + pid_var + b"\r")
        term.expect("pid=")
        term.pump(0.3)
        after = term.seen.split(b"\r")[0]
        assert before == after and before, f"update started another shell: {before!r} then {after!r}"

    check(f"one install command leaves Ctrl+G working ({name})", first_install)
    check(f"updating doesn't ask again or add a shell ({name})", update_in_same_terminal)
    term.close()

    def no_terminal():
        h = tempfile.mkdtemp(prefix="t2c-inst-")
        e = {**env, "HOME": h}
        e.pop("ZDOTDIR", None)
        r = subprocess.run(["sh", installer], env=e, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=60, start_new_session=True)
        assert r.returncode == 0, r.stdout + r.stderr
        assert "run 't2c login'" in r.stdout, r.stdout
        shutil.rmtree(h, ignore_errors=True)

    check(f"install in a script doesn't wait for input ({name})", no_terminal)
    shutil.rmtree(home, ignore_errors=True)
    shutil.rmtree(rel, ignore_errors=True)
    return results


def bash_too_old(path, t2c_dir, port):
    env, home = fresh_env(t2c_dir, port)
    env["SHELL"] = path
    subprocess.run(["t2c", "setup", "bash"], env=env, check=True, capture_output=True)
    term = Term([path, "--noprofile", "--rcfile", os.path.join(home, ".bashrc"), "-i"], env)
    results = []
    try:
        term.expect("needs bash 4.1 or newer")
        term.send(b"PS1='READY> '\r")
        term.expect("READY> ")
        term.clear()
        term.send(b"echo still-usable\r")
        term.expect("still-usable\r\n")
        results.append(("old bash: clear message, shell still works", None))
    except AssertionError as e:
        results.append(("old bash: clear message, shell still works", str(e)))
    term.close()
    shutil.rmtree(home, ignore_errors=True)
    return results


def cli_checks(t2c_dir, port):
    results = []
    env, home = fresh_env(t2c_dir, port)

    def check(label, fn):
        try:
            fn()
            results.append((label, None))
        except AssertionError as e:
            results.append((label, str(e)))

    def run(*args, **kw):
        return subprocess.run(["t2c", *args], env=env, capture_output=True, text=True, **kw)

    def direct_mode():
        r = run("make-marker", "please")
        assert r.stdout.strip() == "echo generated-marker", r.stdout + r.stderr
        r = run("delete-things")
        assert r.stdout.strip().startswith("rm -rf"), r.stdout
        assert "Careful:" in r.stderr, "warning belongs on stderr:\n" + r.stderr

    def private_files():
        for name in ("config.json", "credentials.json"):
            mode = os.stat(os.path.join(home, ".config/t2c", name)).st_mode & 0o777
            assert mode == 0o600, f"{name} has mode {oct(mode)}"
        mode = os.stat(os.path.join(home, ".config/t2c")).st_mode & 0o777
        assert mode == 0o700, f"folder has mode {oct(mode)}"

    def key_not_shown():
        r = run("status")
        assert "test-key" not in r.stdout + r.stderr, r.stdout

    def bad_address_not_saved():
        before = open(os.path.join(home, ".config/t2c/config.json")).read()
        r = run("login", "custom", "--base-url", "http://127.0.0.1:9/v1", "--key-stdin", "--model", "x", input="k\n")
        assert r.returncode != 0, "a login that can't connect should fail"
        assert open(os.path.join(home, ".config/t2c/config.json")).read() == before, "failed login changed the settings"

    def setup_is_idempotent():
        for _ in range(2):
            r = run("setup", "zsh")
            assert r.returncode == 0, r.stderr
        text = open(os.path.join(home, ".zshrc")).read()
        assert text.count("t2c init") == 1, text

    def custom_shortcut():
        r = run("init", "zsh", "--key", "ctrl-t")
        assert "'^T'" in r.stdout, r.stdout
        r = run("init", "bash", "--key", "ctrl-t")
        assert r"\C-t" in r.stdout, r.stdout
        r = run("init", "zsh", "--key", "x; rm -rf /")
        assert r.returncode != 0, "a bad shortcut must be refused"

    def push_login():
        # A stand-in for ssh that runs the remote command against a second home.
        remote = tempfile.mkdtemp(prefix="t2c-remote-")
        fake_dir = tempfile.mkdtemp(prefix="t2c-fakessh-")
        fake = os.path.join(fake_dir, "ssh")
        with open(fake, "w") as f:
            f.write(f'#!/bin/sh\nshift 2\nHOME="{remote}" sh -c "$1"\n')
        os.chmod(fake, 0o755)
        r = subprocess.run(["t2c", "login", "--push", "me@server"], env={**env, "PATH": fake_dir + ":" + env["PATH"]}, capture_output=True, text=True)
        assert r.returncode == 0, r.stdout + r.stderr
        r = subprocess.run(["t2c", "status"], env={**env, "HOME": remote}, capture_output=True, text=True)
        assert "custom" in r.stdout, r.stdout
        mode = os.stat(os.path.join(remote, ".config/t2c/credentials.json")).st_mode & 0o777
        assert mode == 0o600, oct(mode)
        r = run("login", "--push", "-oProxyCommand=evil")
        assert r.returncode != 0, "an option must not be accepted as a host"
        shutil.rmtree(remote, ignore_errors=True)
        shutil.rmtree(fake_dir, ignore_errors=True)

    def session_cleanup():
        state = os.path.join(home, ".local/state/t2c/s")
        os.makedirs(os.path.join(state, "999999"))
        run("init", "zsh")
        assert not os.path.exists(os.path.join(state, "999999")), "closed shell's state was kept"

    def setup_adds_path_when_missing():
        h = tempfile.mkdtemp(prefix="t2c-path-")
        bin_dir = os.path.join(h, ".local/bin")
        os.makedirs(bin_dir)
        copy = os.path.join(bin_dir, "t2c")
        shutil.copy(shutil.which("t2c", path=env["PATH"]), copy)
        for shell_name, rc in (("bash", ".bashrc"), ("zsh", ".zshrc")):
            e = {"HOME": h, "PATH": "/usr/bin:/bin", "SHELL": "/bin/" + shell_name}
            subprocess.run([copy, "setup", shell_name], env=e, check=True, capture_output=True)
            text = open(os.path.join(h, rc)).read()
            assert bin_dir in text, "PATH line missing:\n" + text
            r = subprocess.run(["/bin/" + shell_name, "-c", f'source "{h}/{rc}" 2>/dev/null; command -v t2c'], env=e, capture_output=True, text=True)
            assert r.stdout.strip().endswith("/t2c"), "t2c not found after loading the startup file: " + r.stdout + r.stderr
        shutil.rmtree(h, ignore_errors=True)

    def uninstall_removes_everything():
        h = tempfile.mkdtemp(prefix="t2c-rm-")
        bin_dir = os.path.join(h, ".local/bin")
        os.makedirs(bin_dir)
        copy = os.path.join(bin_dir, "t2c")
        shutil.copy(shutil.which("t2c", path=env["PATH"]), copy)
        e = {"HOME": h, "PATH": "/usr/bin:/bin", "SHELL": "/bin/bash", "XDG_CONFIG_HOME": os.path.join(h, ".config")}
        before = "# my settings\nalias ll='ls -l'\n"
        for rc in (".bashrc", ".zshrc"):
            with open(os.path.join(h, rc), "w") as f:
                f.write(before)
        for shell_name in ("bash", "zsh", "fish"):
            subprocess.run([copy, "setup", shell_name], env=e, check=True, capture_output=True)
        subprocess.run([copy, "login", "custom", "--base-url", f"http://127.0.0.1:{port}/v1", "--key-stdin", "--model", "m"],
                       input=b"k\n", env=e, check=True, capture_output=True)
        state = os.path.join(h, ".local/state/t2c/s/123")
        os.makedirs(state)
        # Refuses without a person or --yes, and leaves everything alone.
        r = subprocess.run([copy, "uninstall"], env=e, stdin=subprocess.DEVNULL, capture_output=True, text=True)
        assert r.returncode != 0 and os.path.exists(copy), "uninstall ran without confirmation"
        r = subprocess.run([copy, "uninstall", "--yes"], env=e, capture_output=True, text=True)
        assert r.returncode == 0, r.stdout + r.stderr
        for rc in (".bashrc", ".zshrc"):
            text = open(os.path.join(h, rc)).read()
            assert text == before, f"{rc} wasn't restored exactly:\n{text!r}"
        for gone in (copy, os.path.join(h, ".config/t2c"), os.path.join(h, ".local/state/t2c"), os.path.join(h, ".config/fish/conf.d/t2c.fish")):
            assert not os.path.exists(gone), gone + " is still there"
        shutil.rmtree(h, ignore_errors=True)

    for label, fn in [
        ("setup adds t2c's folder to PATH if needed", setup_adds_path_when_missing),
        ("uninstall removes everything and keeps your lines", uninstall_removes_everything),
        ("direct t2c request prints a command", direct_mode),
        ("login files are private", private_files),
        ("status doesn't show the key", key_not_shown),
        ("failed login changes nothing", bad_address_not_saved),
        ("setup only adds itself once", setup_is_idempotent),
        ("shortcut can be changed", custom_shortcut),
        ("login --push copies the login", push_login),
        ("closed shells' state is removed", session_cleanup),
    ]:
        check(label, fn)
    shutil.rmtree(home, ignore_errors=True)
    return results


def main():
    t2c = os.path.abspath(sys.argv[1])
    t2c_dir = os.path.dirname(t2c)
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), FakeAI)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    port = server.server_address[1]

    failed = 0
    print("\ncommand line:")
    for label, err in cli_checks(t2c_dir, port):
        print(f"  {'ok  ' if err is None else 'FAIL'} {label}")
        if err:
            failed += 1
            print("       " + err.replace("\n", "\n       "))
    for spec in sys.argv[2:]:
        name, _, path = spec.partition("=")
        path = path or shutil.which(name)
        version = subprocess.run([path, "--version"], capture_output=True, text=True).stdout.splitlines()[0]
        print(f"\n{name}: {version}")
        old = name == "bash" and re.search(r"version (3\.|4\.0)", version)
        results = bash_too_old(path, t2c_dir, port) if old else run_shell(name, path, t2c_dir, port) + install_flow(name, path, t2c, port)
        for label, err in results:
            print(f"  {'ok  ' if err is None else 'FAIL'} {label}")
            if err:
                failed += 1
                print("       " + err.replace("\n", "\n       "))
    print(f"\n{'all passed' if not failed else str(failed) + ' failed'}")
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
