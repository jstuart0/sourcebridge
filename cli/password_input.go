// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

// PasswordSource enumerates how the CLI obtained an admin password.
// Returned by ReadNonInteractivePassword so callers can include the
// source in audit logs / error messages without restating the
// precedence rule themselves.
type PasswordSource int

const (
	// PasswordSourceNone means no non-interactive vector was set; the
	// caller should fall back to interactive prompting.
	PasswordSourceNone PasswordSource = iota
	// PasswordSourceStdin came from --password-stdin (one line read).
	PasswordSourceStdin
	// PasswordSourceFile came from --password-file <path>.
	PasswordSourceFile
	// PasswordSourceEnv came from SOURCEBRIDGE_PASSWORD.
	PasswordSourceEnv
)

// String returns a human-friendly label for use in errors and help.
func (s PasswordSource) String() string {
	switch s {
	case PasswordSourceStdin:
		return "--password-stdin"
	case PasswordSourceFile:
		return "--password-file"
	case PasswordSourceEnv:
		return "SOURCEBRIDGE_PASSWORD env"
	default:
		return "interactive"
	}
}

// PasswordInputFlags collects the three non-interactive vectors the CLI
// supports, in strict precedence order: --password-stdin >
// --password-file > SOURCEBRIDGE_PASSWORD env. Callers attach this to
// each subcommand that accepts a password (sourcebridge login,
// sourcebridge setup admin, …) via RegisterFlags.
//
// Why these three (and explicitly NOT --password <value>): the value
// vector would leak into shell history, /proc/<pid>/cmdline, and the
// `ps` listing. Stdin and file cover every legitimate non-interactive
// case; env is the last resort for environments where neither is
// available (e.g. container orchestrators that inject secrets as env
// vars). Tester report 2026-04-30 (Pazaryna) Issue 5 / CA-127.
type PasswordInputFlags struct {
	Stdin   bool
	File    string
	envName string // overridable for tests; defaults to SOURCEBRIDGE_PASSWORD
}

// RegisterFlags wires --password-stdin and --password-file onto cmd's
// flag set. The env-var vector is read at resolve time and needs no
// flag.
//
// We register against pflag rather than a single helper struct so each
// caller can position the flags consistently with its own set (and so
// help text appears in the right command's --help). Flag names match
// the docker / kubectl / git-credential conventions so muscle memory
// transfers.
func (p *PasswordInputFlags) RegisterFlags(register func(*bool, string, bool, string), registerStr func(*string, string, string, string)) {
	register(&p.Stdin, "password-stdin", false,
		"Read the admin password from stdin (one line). Recommended for CI: "+
			"`echo \"$ADMIN_PASSWORD\" | sourcebridge ... --password-stdin`. "+
			"The password never appears in shell history or process listings.")
	registerStr(&p.File, "password-file", "",
		"Read the admin password from a file (single line). Warns if the "+
			"file's mode is more permissive than 0600. Use this when stdin "+
			"is already consumed (chained pipelines, supervisord, …).")
}

// ResolveNonInteractive returns (password, source, nil) when one of the
// three non-interactive vectors is set, (\"\", PasswordSourceNone, nil)
// when none is set, or (\"\", PasswordSourceNone, err) when more than
// one is set or the chosen vector failed to read.
//
// `stdin` is the io.Reader to use for the --password-stdin vector
// (production passes os.Stdin; tests pass strings.NewReader). Strict
// "exactly one source" enforcement: if a user sets two vectors at once,
// we refuse rather than silently picking precedence — they almost
// certainly made a mistake and a quiet pick would be confusing.
func (p *PasswordInputFlags) ResolveNonInteractive(stdin io.Reader) (string, PasswordSource, error) {
	envName := p.envName
	if envName == "" {
		envName = "SOURCEBRIDGE_PASSWORD"
	}
	envVal, envSet := os.LookupEnv(envName)
	envSet = envSet && envVal != ""

	// Count the vectors that were explicitly engaged.
	var engaged []string
	if p.Stdin {
		engaged = append(engaged, "--password-stdin")
	}
	if p.File != "" {
		engaged = append(engaged, "--password-file")
	}
	if envSet {
		engaged = append(engaged, envName)
	}

	if len(engaged) > 1 {
		return "", PasswordSourceNone, fmt.Errorf(
			"more than one password source is set (%s); pick exactly one",
			strings.Join(engaged, ", "),
		)
	}
	if len(engaged) == 0 {
		return "", PasswordSourceNone, nil
	}

	switch engaged[0] {
	case "--password-stdin":
		return readStdinLine(stdin)
	case "--password-file":
		return readPasswordFile(p.File)
	default: // env
		return strings.TrimRight(envVal, "\r\n"), PasswordSourceEnv, nil
	}
}

// readStdinLine reads exactly the first line from r, trims trailing
// CR/LF (so `echo $PW` works the same as `printf '%s' "$PW"`), and
// returns it. An empty line is rejected so the caller doesn't send
// "" to the server. The reader is closed-over in the caller's
// process-stdin sense; we only consume what we need so chained
// pipelines (e.g. `cmd | sourcebridge --password-stdin && rest`)
// don't lose data, but in practice the CLI exits right after.
func readStdinLine(r io.Reader) (string, PasswordSource, error) {
	if r == nil {
		r = os.Stdin
	}
	br := bufio.NewReader(r)
	line, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", PasswordSourceNone, fmt.Errorf("reading password from stdin: %w", err)
	}
	pw := strings.TrimRight(line, "\r\n")
	if pw == "" {
		return "", PasswordSourceNone, fmt.Errorf(
			"--password-stdin received an empty line; pipe a non-empty password " +
				"(e.g. `echo \"$ADMIN_PASSWORD\" | sourcebridge ...`)",
		)
	}
	return pw, PasswordSourceStdin, nil
}

// readPasswordFile reads the password from the given path. Single-line
// only (any trailing newline is trimmed). On Unix, warns to stderr if
// the file mode is more permissive than 0600 — the user might be
// shipping the password in their dotfiles repo by accident.
//
// The mode check is best-effort: skipped on Windows (where Unix-style
// modes don't carry the same meaning) and skipped if Stat fails.
func readPasswordFile(path string) (string, PasswordSource, error) {
	if path == "" {
		return "", PasswordSourceNone, fmt.Errorf("--password-file requires a path")
	}
	fi, statErr := os.Stat(path)
	if statErr != nil {
		return "", PasswordSourceNone, fmt.Errorf("--password-file %q: %w", path, statErr)
	}
	if fi.IsDir() {
		return "", PasswordSourceNone, fmt.Errorf("--password-file %q is a directory, not a file", path)
	}
	if runtime.GOOS != "windows" {
		// 0o077 catches "any group or other access bits set" — i.e. the
		// file is more permissive than 0600.
		if fi.Mode().Perm()&0o077 != 0 {
			fmt.Fprintf(os.Stderr,
				"warning: --password-file %q has mode %#o (recommended: 0600). "+
					"Other users on this host may be able to read your admin password.\n",
				path, fi.Mode().Perm())
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", PasswordSourceNone, fmt.Errorf("--password-file %q: %w", path, err)
	}
	// Single-line: take everything up to the first newline; trim trailing
	// \r so files written on Windows still work.
	text := string(data)
	if i := strings.IndexAny(text, "\r\n"); i >= 0 {
		text = text[:i]
	}
	if text == "" {
		return "", PasswordSourceNone, fmt.Errorf("--password-file %q is empty", path)
	}
	return text, PasswordSourceFile, nil
}
