// Package gitx runs git and reads the repository facts the other packages need.
package gitx

import (
	"bytes"
	"context"
	"os/exec"
	"strings"

	"github.com/brynbellomy/go-utils/errors"
)

// Repo runs git in one working directory.
type Repo struct {
	Dir string
}

// Run executes git with args and returns its stdout. A failure carries git's
// stderr.
func (r Repo) Run(ctx context.Context, args ...string) (string, error) {
	return run(ctx, r.Dir, nil, "git", args...)
}

// RunInput is Run with stdin.
func (r Repo) RunInput(ctx context.Context, stdin string, args ...string) (string, error) {
	return run(ctx, r.Dir, strings.NewReader(stdin), "git", args...)
}

// Command runs any program in dir and returns its stdout.
func Command(ctx context.Context, dir, name string, args ...string) (string, error) {
	return run(ctx, dir, nil, name, args...)
}

func run(ctx context.Context, dir string, stdin *strings.Reader, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), errors.With(err, "command failed").
			Set("cmd", name+" "+strings.Join(args, " "), "stderr", strings.TrimSpace(stderr.String())).Err()
	}
	return stdout.String(), nil
}

// Show returns the contents of path at rev. A path absent at rev returns ok
// false.
func (r Repo) Show(ctx context.Context, rev, path string) (data string, ok bool, err error) {
	if _, err := r.Run(ctx, "cat-file", "-e", rev+":"+path); err != nil {
		return "", false, nil
	}
	out, err := r.Run(ctx, "show", rev+":"+path)
	if err != nil {
		return "", false, err
	}
	return out, true, nil
}

// Config returns a git config value, or def when unset.
func (r Repo) Config(ctx context.Context, key, def string) string {
	out, err := r.Run(ctx, "config", "--get", key)
	if err != nil {
		return def
	}
	return strings.TrimSpace(out)
}

// ConfigBool returns a boolean git config value, or def when unset or invalid.
func (r Repo) ConfigBool(ctx context.Context, key string, def bool) bool {
	out, err := r.Run(ctx, "config", "--type=bool", "--get", key)
	if err != nil {
		return def
	}
	return strings.TrimSpace(out) == "true"
}

// Remote returns the remote the tools publish to: beadstools.remote, else
// origin.
func (r Repo) Remote(ctx context.Context) string {
	return r.Config(ctx, "beadstools.remote", "origin")
}

// MainBranch returns the branch that counts as merged: beadstools.mainBranch,
// else the remote's HEAD, else main.
func (r Repo) MainBranch(ctx context.Context) string {
	if b := r.Config(ctx, "beadstools.mainBranch", ""); b != "" {
		return b
	}
	remote := r.Remote(ctx)
	if out, err := r.Run(ctx, "symbolic-ref", "--short", "refs/remotes/"+remote+"/HEAD"); err == nil {
		return strings.TrimPrefix(strings.TrimSpace(out), remote+"/")
	}
	return "main"
}

// CurrentBranch returns the checked-out branch, or "" when HEAD is detached.
func (r Repo) CurrentBranch(ctx context.Context) string {
	out, err := r.Run(ctx, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// TopLevel returns the working tree root.
func (r Repo) TopLevel(ctx context.Context) (string, error) {
	out, err := r.Run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// GitPath resolves a path inside the git directory (hooks, MERGE_HEAD, …),
// honouring worktrees and core.hooksPath.
func (r Repo) GitPath(ctx context.Context, name string) (string, error) {
	out, err := r.Run(ctx, "rev-parse", "--path-format=absolute", "--git-path", name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
