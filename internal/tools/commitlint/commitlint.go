// Command commitlint verifies that every new commit subject and the pull
// request title follow the Conventional Commits format. It reads BASE_SHA,
// HEAD_SHA and PR_TITLE from the environment and treats all of them as
// untrusted: they are validated and printed, never executed.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
)

var (
	subjectPattern = regexp.MustCompile(`^(?:feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert)(?:\([^\r\n()]+\))?!?: \S.*$`)
	shaPattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

const (
	zeroSHA   = "0000000000000000000000000000000000000000"
	logFormat = "--format=%H%x00%s"
)

// commit is a commit SHA with its subject line.
type commit struct{ SHA, Subject string }

// gitLog runs `git log` with the given arguments and returns the commits.
type gitLog func(ctx context.Context, args ...string) ([]commit, error)

// check returns one message per violation: invalid SHAs, git failures, commit
// subjects in base..head (the full history of head when base is empty or all
// zeros) and an invalid, non-blank prTitle.
func check(ctx context.Context, git gitLog, base, head, prTitle string) []string {
	base, head = strings.TrimSpace(base), strings.TrimSpace(head)
	if !shaPattern.MatchString(head) {
		return []string{"HEAD_SHA must be a 40-character lowercase hex SHA"}
	}
	revRange := head
	if base != "" && base != zeroSHA {
		if !shaPattern.MatchString(base) {
			return []string{"BASE_SHA must be empty, all zeros, or a 40-character lowercase hex SHA"}
		}
		revRange = base + ".." + head
	}

	var violations []string
	commits, err := git(ctx, "log", logFormat, revRange)
	if err != nil {
		return []string{fmt.Sprintf("git log %s failed: %v", revRange, err)}
	}
	for _, c := range commits {
		if !subjectPattern.MatchString(c.Subject) {
			violations = append(violations, fmt.Sprintf("commit %s: %q is not a Conventional Commit subject", shortSHA(c.SHA), c.Subject))
		}
	}
	if strings.TrimSpace(prTitle) != "" && !subjectPattern.MatchString(prTitle) {
		violations = append(violations, fmt.Sprintf("PR title %q is not a Conventional Commit subject", prTitle))
	}
	return violations
}

func shortSHA(sha string) string {
	return sha[:min(len(sha), 7)]
}

// run executes the check using env for inputs, writes the report to out and
// returns the process exit code.
func run(ctx context.Context, env func(string) string, git gitLog, out io.Writer) int {
	violations := check(ctx, git, env("BASE_SHA"), env("HEAD_SHA"), env("PR_TITLE"))
	if len(violations) == 0 {
		_, _ = fmt.Fprintln(out, "commitlint: ok")
		return 0
	}
	for _, v := range violations {
		_, _ = fmt.Fprintln(out, "commitlint:", v)
	}
	_, _ = fmt.Fprintln(out, "Expected: type(scope)!: description, with type one of feat, fix, docs, style, refactor, perf, test, build, ci, chore, revert")
	return 1
}

// execGit returns a gitLog that runs the git binary in dir (the current
// directory when empty). No shell is involved.
func execGit(dir string) gitLog {
	return func(ctx context.Context, args ...string) ([]commit, error) {
		cmd := exec.CommandContext(ctx, "git", args...) // #nosec G204 -- fixed binary; arguments built from validated SHAs
		cmd.Dir = dir
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return parseLog(out)
	}
}

// parseLog parses `git log --format=%H%x00%s` output.
func parseLog(raw []byte) ([]commit, error) {
	var commits []commit
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			continue
		}
		sha, subject, ok := strings.Cut(line, "\x00")
		if !ok {
			return nil, errors.New("unexpected git log output")
		}
		commits = append(commits, commit{SHA: sha, Subject: subject})
	}
	return commits, nil
}
