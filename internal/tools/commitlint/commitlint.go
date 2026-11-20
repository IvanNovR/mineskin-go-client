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
	// subjectPattern accepts the Conventional Commits superset of types, or the
	// subject git generates for `git revert`: Revert "<original subject>".
	subjectPattern = regexp.MustCompile(`^(?:(?:feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert)(?:\([^\r\n()]+\))?!?: \S.*|Revert "[^\r\n]*\S[^\r\n]*")$`)
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

// report is the outcome of check.
type report struct {
	Violations []string // problems that fail the run
	Warnings   []string // notable conditions that do not fail the run
	Checked    int      // number of commit subjects checked
}

// check lints the non-merge commits in base..head (the full history of head
// when base is empty or all zeros) and a non-blank prTitle. Invalid SHAs and
// git failures are violations. When base is not reachable, for example after a
// force-push, only head is linted and a warning is recorded.
func check(ctx context.Context, git gitLog, base, head, prTitle string) report {
	base, head = strings.TrimSpace(base), strings.TrimSpace(head)
	if !shaPattern.MatchString(head) {
		return report{Violations: []string{"HEAD_SHA must be a 40-character lowercase hex SHA"}}
	}
	hasBase := base != "" && base != zeroSHA
	if hasBase && !shaPattern.MatchString(base) {
		return report{Violations: []string{"BASE_SHA must be empty, all zeros, or a 40-character lowercase hex SHA"}}
	}

	var rep report
	logArgs := []string{"log", "--no-merges", logFormat, head}
	if hasBase {
		if _, err := git(ctx, "cat-file", "-e", base+"^{commit}"); err != nil {
			rep.Warnings = append(rep.Warnings, "BASE_SHA is not reachable (force-push?); linting only HEAD")
			logArgs = []string{"log", "--no-merges", "-1", logFormat, head}
			hasBase = false
		} else {
			logArgs[len(logArgs)-1] = base + ".." + head
		}
	}

	commits, err := git(ctx, logArgs...)
	if err != nil {
		return report{Violations: []string{fmt.Sprintf("git log %s failed: %v", logArgs[len(logArgs)-1], err)}}
	}
	rep.Checked = len(commits)
	if hasBase && len(commits) == 0 {
		rep.Warnings = append(rep.Warnings, "BASE_SHA..HEAD_SHA contains no commits to check")
	}
	for _, c := range commits {
		if !subjectPattern.MatchString(c.Subject) {
			rep.Violations = append(rep.Violations, fmt.Sprintf("commit %s: %q is not a Conventional Commit subject", shortSHA(c.SHA), c.Subject))
		}
	}
	if strings.TrimSpace(prTitle) != "" && !subjectPattern.MatchString(prTitle) {
		rep.Violations = append(rep.Violations, fmt.Sprintf("PR title %q is not a Conventional Commit subject", prTitle))
	}
	return rep
}

func shortSHA(sha string) string {
	return sha[:min(len(sha), 7)]
}

// run executes the check using env for inputs, writes the report to out and
// returns the process exit code.
func run(ctx context.Context, env func(string) string, git gitLog, out io.Writer) int {
	rep := check(ctx, git, env("BASE_SHA"), env("HEAD_SHA"), env("PR_TITLE"))
	for _, w := range rep.Warnings {
		_, _ = fmt.Fprintln(out, "commitlint: warning:", w)
	}
	if len(rep.Violations) == 0 {
		_, _ = fmt.Fprintf(out, "commitlint: ok (%d commits checked)\n", rep.Checked)
		return 0
	}
	for _, v := range rep.Violations {
		_, _ = fmt.Fprintln(out, "commitlint:", v)
	}
	_, _ = fmt.Fprintln(out, "Expected: type(scope)!: description, with type one of feat, fix, docs, style, refactor, perf, test, build, ci, chore, revert, or a git Revert \"...\" subject")
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
	for line := range strings.SplitSeq(string(raw), "\n") {
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
