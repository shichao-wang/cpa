package launch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Environ builds the child process environment: the inherited one, minus
// anything the plan removes, plus everything it sets.
func (p *Plan) Environ() []string {
	unset := map[string]bool{}
	for _, k := range p.Unset {
		unset[k] = true
	}
	out := make([]string, 0, len(os.Environ())+len(p.Env))
	seen := map[string]bool{}
	for _, kv := range os.Environ() {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if unset[name] {
			continue
		}
		if _, override := p.Env[name]; override {
			continue // re-added below with the plan's value
		}
		out = append(out, kv)
		seen[name] = true
	}
	names := make([]string, 0, len(p.Env))
	for name := range p.Env {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out = append(out, name+"="+p.Env[name])
	}
	return out
}

// CommandLine renders the launch for display (or for a shell).
func (p *Plan) CommandLine() string {
	parts := append([]string{p.Bin}, p.Args...)
	quoted := make([]string, len(parts))
	for i, a := range parts {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n\"'\\$`&|;<>()*?[]{}#!~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// writeSettings persists the settings document owner-only, and cleans up
// documents left behind by launches that ended abruptly.
func (p *Plan) writeSettings() error {
	dir := filepath.Dir(p.SettingsPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	sweepStaleSettings(dir)
	return os.WriteFile(p.SettingsPath, p.SettingsBlob, 0o600)
}

// sweepStaleSettings drops documents older than a day. They are only needed
// while the agent they launched is running.
func sweepStaleSettings(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "settings-") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}

// Run replaces the current process with the agent.
//
// Using exec on Unix means the agent inherits this process's terminal and
// signal handling directly: there is no wrapper process in the middle to
// mangle Ctrl-C or swallow the exit code.
func (p *Plan) Run() error {
	if len(p.SettingsBlob) > 0 {
		if err := p.writeSettings(); err != nil {
			return fmt.Errorf("writing launch settings: %w", err)
		}
	}
	bin, err := exec.LookPath(p.Bin)
	if err != nil {
		return fmt.Errorf("cannot find %q in PATH: %w", p.Bin, err)
	}
	argv := append([]string{p.Bin}, p.Args...)
	env := p.Environ()

	if runtime.GOOS != "windows" {
		if err := syscall.Exec(bin, argv, env); err != nil {
			return fmt.Errorf("exec %s: %w", bin, err)
		}
		return nil // unreachable
	}

	// Windows has no exec; run as a child and forward the exit code.
	cmd := exec.Command(bin, p.Args...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if ok := asExitError(err, &exitErr); ok {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	return nil
}

func asExitError(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok {
		*target = e
		return true
	}
	return false
}

// marshalNoEscape renders JSON without Go's default HTML escaping, so
// endpoints and descriptions stay readable ("http://host" rather than
// "http:\/\/host", "->" rather than "\u003e").
func marshalNoEscape(v interface{}, indent string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent != "" {
		enc.SetIndent("", indent)
	}
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func lookupEnv(key string) (string, bool) { return os.LookupEnv(key) }

// PrettyJSON indents a document for display, prefixing every line with a
// comment marker so --dry-run output can be pasted into a shell as-is.
func PrettyJSON(v interface{}, prefix string) ([]byte, error) {
	blob, err := marshalNoEscape(v, "  ")
	if err != nil {
		return nil, err
	}
	if prefix == "" {
		return blob, nil
	}
	lines := strings.Split(string(blob), "\n")
	for i, l := range lines {
		if l == "" {
			continue
		}
		lines[i] = prefix + l
	}
	return []byte(strings.Join(lines, "\n")), nil
}
