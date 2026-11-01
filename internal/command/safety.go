package command

import "regexp"

type risk struct {
	re   *regexp.Regexp
	note string
}

// risks are checked in order and the first match wins. This is a best-effort
// warning for the user reading the command, not a sandbox.
var risks = []risk{
	{regexp.MustCompile(`:\(\)\s*\{.*:\|:.*\}`), "fork bomb, can freeze the machine"},
	{regexp.MustCompile(`\brm\s+(-[a-zA-Z-]+\s+)*(/|/\*|~|~/\*?|\$HOME/?\*?)(\s|$)`), "deletes your home folder or the whole system"},
	{regexp.MustCompile(`\brm\s+(-[a-zA-Z-]+\s+)*-*[a-zA-Z]*(r[a-zA-Z]*f|f[a-zA-Z]*r)\b`), "permanently deletes files and folders"},
	{regexp.MustCompile(`\brm\s+(-[a-zA-Z-]+\s+)*(-[rR]\b|--recursive\b)`), "permanently deletes folders"},
	{regexp.MustCompile(`\bfind\b.*(\s-delete\b|-exec(dir)?\s+(sudo\s+)?rm\b)`), "permanently deletes every file it finds"},
	{regexp.MustCompile(`\bxargs\b.*\brm\b`), "permanently deletes every file it's given"},
	{regexp.MustCompile(`\bdd\b.*\bof=`), "overwrites the target file or disk"},
	{regexp.MustCompile(`\b(mkfs(\.\w+)?|fdisk|parted|wipefs|shred)\b`), "can erase a disk or its data"},
	{regexp.MustCompile(`\bdiskutil\s+(erase|reformat|partition)`), "erases a disk"},
	{regexp.MustCompile(`>\s*/dev/(sd|nvme|disk|hd|vd)`), "overwrites a disk"},
	{regexp.MustCompile(`\b(curl|wget)\b[^|]*\|\s*(sudo\s+)?(ba|z|fi|da)?sh\b`), "runs a script from the internet without showing it"},
	{regexp.MustCompile(`\bchmod\s+(-[a-zA-Z-]+\s+)*(-R|--recursive)\b`), "changes permissions on everything inside a folder"},
	{regexp.MustCompile(`\bchown\s+(-[a-zA-Z-]+\s+)*(-R|--recursive)\b`), "changes the owner of everything inside a folder"},
	{regexp.MustCompile(`\bgit\s+(\S+\s+)*push\b.*(\s-f\b|--force)`), "overwrites history on the remote"},
	{regexp.MustCompile(`\bgit\s+reset\s+.*--hard\b`), "discards uncommitted changes"},
	{regexp.MustCompile(`\bgit\s+clean\b.*-[a-zA-Z]*f`), "permanently deletes untracked files"},
	{regexp.MustCompile(`\bgit\s+checkout\s+(--\s+)?\.(\s|$)`), "discards uncommitted changes"},
	{regexp.MustCompile(`(?i)\b(drop\s+(table|database|schema)|truncate\s+table)\b`), "permanently deletes database data"},
	{regexp.MustCompile(`(?i)\bdelete\s+from\s+\S+\s*(;|"|'|$)`), "deletes every row in the table"},
	{regexp.MustCompile(`(^|[;&|]\s*|\bsudo\s+|\bssh\s+\S+\s+)(shutdown|reboot|halt|poweroff)\b`), "shuts down or restarts the machine"},
	{regexp.MustCompile(`\bkill\s+(-\w+\s+)*-1\b|\bkillall\b`), "stops many processes at once"},
	{regexp.MustCompile(`\biptables\s+(-F|--flush)\b|\bufw\s+(disable|reset)\b`), "changes firewall rules and can lock you out"},
	{regexp.MustCompile(`\bdocker\s+(system|volume|image|container)\s+prune\b`), "permanently deletes Docker data"},
	{regexp.MustCompile(`\bkubectl\s+delete\b`), "deletes cluster resources"},
	{regexp.MustCompile(`\bterraform\s+destroy\b`), "destroys infrastructure"},
	{regexp.MustCompile(`\bcrontab\s+-r\b`), "deletes all your scheduled jobs"},
}

// Warn returns a short warning when a command looks destructive, or "".
func Warn(cmd string) string {
	for _, r := range risks {
		if r.re.MatchString(cmd) {
			return "Careful: " + r.note + "."
		}
	}
	return ""
}

var secrets = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(-----END [A-Z ]*PRIVATE KEY-----|$)`), "[REDACTED]"},
	{regexp.MustCompile(`\b(sk|pk|rk)-[A-Za-z0-9_-]{16,}`), "[REDACTED]"},
	{regexp.MustCompile(`\b(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}|\bgithub_pat_[A-Za-z0-9_]{20,}`), "[REDACTED]"},
	{regexp.MustCompile(`\b(AKIA|ASIA)[A-Z0-9]{16}\b`), "[REDACTED]"},
	{regexp.MustCompile(`\bxox[a-z]-[A-Za-z0-9-]{10,}`), "[REDACTED]"},
	{regexp.MustCompile(`\bAIza[A-Za-z0-9_-]{30,}`), "[REDACTED]"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}(\.[A-Za-z0-9_-]+)?`), "[REDACTED]"},
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`), "$1 [REDACTED]"},
	{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^/\s:@]+):[^/\s@]+@`), "$1:[REDACTED]@"},
	{regexp.MustCompile(`(?i)([a-z0-9_.-]*(password|passwd|pwd|secret|token|api[_-]?key|apikey|access[_-]?key|credential)[a-z0-9_.-]*"?\s*[=:]\s*)("[^"\n]*"|'[^'\n]*'|[^\s"',;&]+)`), "${1}[REDACTED]"},
	{regexp.MustCompile(`(\s--?(password|passwd|pass|token|api-key|secret)\s+)(\S+)`), "${1}[REDACTED]"},
	{regexp.MustCompile(`(\b(mysql|mysqldump|psql|mariadb)\b[^\n]*\s-p)([^\s-]\S*)`), "${1}[REDACTED]"},
}

// Redact removes text that looks like a password or key. It can't catch
// everything, so it reduces exposure rather than guaranteeing none.
func Redact(s string) string {
	for _, r := range secrets {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}
