package main

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const (
	shaA     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB     = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	subjectA = "feat: add thing"
)

// fakeGit records the arguments of every call and returns canned results.
type fakeGit struct {
	commits     []commit
	err         error
	unreachable bool // cat-file fails, as for a base SHA missing after a force-push
	calls       [][]string
}

func (f *fakeGit) log(_ context.Context, args ...string) ([]commit, error) {
	f.calls = append(f.calls, args)
	if args[0] == "cat-file" {
		if f.unreachable {
			return nil, errors.New("bad object")
		}
		return nil, nil
	}
	return f.commits, f.err
}

// logCalls returns only the `git log` invocations.
func (f *fakeGit) logCalls() [][]string {
	var out [][]string
	for _, c := range f.calls {
		if c[0] == "log" {
			out = append(out, c)
		}
	}
	return out
}

func violationsOf(git gitLog, base, head, prTitle string) []string {
	return check(context.Background(), git, base, head, prTitle).Violations
}

func TestSubjectPattern(t *testing.T) {
	valid := []string{
		"feat!: x", "fix(ci): x", "chore(main): release 0.1.0", "feat(api)!: break",
		"revert: undo", "docs: update ✨", "build: deps",
		`Revert "feat: add thing"`, `Revert "Revert "fix: x""`, `Revert "not conventional but reverted"`,
	}
	invalid := []string{
		"", "bad title", "feat:x", "feat: ", "Feat: x", "feat(): x", "feat(a(b)): x",
		"feature: x", "feat : x", "fix(a\nb): x", "feat: x\ny",
	}
	for _, s := range valid {
		if !subjectPattern.MatchString(s) {
			t.Errorf("expected %q to be valid", s)
		}
	}
	for _, s := range invalid {
		if subjectPattern.MatchString(s) {
			t.Errorf("expected %q to be invalid", s)
		}
	}
}

func TestCheckPassesRangeToGit(t *testing.T) {
	g := &fakeGit{commits: []commit{{shaA, subjectA}}}

	got := violationsOf(g.log, shaA, shaB, "")

	if len(got) != 0 {
		t.Fatalf("unexpected violations: %v", got)
	}
	want := []string{"log", "--no-merges", "--format=%H%x00%s", shaA + ".." + shaB}
	if len(g.logCalls()) != 1 || strings.Join(g.logCalls()[0], " ") != strings.Join(want, " ") {
		t.Fatalf("git calls = %v, want %v", g.logCalls(), want)
	}
}

func TestCheckReportsAllInvalidSubjectsIncludingBehindValidTip(t *testing.T) {
	g := &fakeGit{commits: []commit{
		{shaB, "feat: valid tip"},
		{shaA, "oops one"},
		{"cccccccccccccccccccccccccccccccccccccccc", "Another bad"},
	}}

	got := violationsOf(g.log, shaA, shaB, "")

	if len(got) != 2 {
		t.Fatalf("want 2 violations, got %v", got)
	}
	if !strings.Contains(got[0], "oops one") || !strings.Contains(got[1], "Another bad") {
		t.Errorf("violations missing subjects: %v", got)
	}
	if !strings.Contains(got[0], shaA[:7]) {
		t.Errorf("violation should name the short SHA: %q", got[0])
	}
}

func TestCheckAcceptsConventionalSubjects(t *testing.T) {
	g := &fakeGit{commits: []commit{
		{shaA, "feat!: x"}, {shaB, "fix(ci): x"}, {shaA, "chore(main): release 0.1.0"},
	}}
	if got := violationsOf(g.log, shaA, shaB, ""); len(got) != 0 {
		t.Fatalf("unexpected violations: %v", got)
	}
}

func TestCheckPRTitle(t *testing.T) {
	tests := []struct {
		name    string
		commits []commit
		title   string
		wantN   int
	}{
		{"valid commits invalid title", []commit{{shaA, subjectA}}, "bad title", 1},
		{"invalid commit valid title", []commit{{shaA, "bad"}}, "feat: ok", 1},
		{"both invalid", []commit{{shaA, "bad"}}, "bad title", 2},
		{"both valid", []commit{{shaA, subjectA}}, "fix: ok", 0},
		{"empty title skipped", []commit{{shaA, subjectA}}, "", 0},
		{"blank title skipped", []commit{{shaA, subjectA}}, "   ", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := &fakeGit{commits: tc.commits}
			got := violationsOf(g.log, shaA, shaB, tc.title)
			if len(got) != tc.wantN {
				t.Fatalf("want %d violations, got %v", tc.wantN, got)
			}
		})
	}
}

func TestCheckFullHistoryWhenBaseMissingOrZero(t *testing.T) {
	for _, base := range []string{"", zeroSHA, "  "} {
		g := &fakeGit{commits: []commit{{shaA, subjectA}}}
		got := violationsOf(g.log, base, shaB, "")
		if len(got) != 0 {
			t.Fatalf("base %q: unexpected violations %v", base, got)
		}
		if len(g.logCalls()) != 1 || g.logCalls()[0][len(g.logCalls()[0])-1] != shaB {
			t.Fatalf("base %q: want full-history range %q, got %v", base, shaB, g.calls)
		}
	}
}

func TestCheckRejectsBadSHAs(t *testing.T) {
	bad := []string{"abc", "--output=x", strings.Repeat("g", 40), strings.Repeat("a", 41), "main; rm -rf /", strings.ToUpper(shaA)}
	for _, s := range bad {
		g := &fakeGit{}
		if got := violationsOf(g.log, s, shaB, ""); len(got) == 0 {
			t.Errorf("bad base %q accepted", s)
		}
		if got := violationsOf(g.log, shaA, s, ""); len(got) == 0 {
			t.Errorf("bad head %q accepted", s)
		}
		if len(g.calls) != 0 {
			t.Errorf("git must not run for bad SHA %q", s)
		}
	}
}

func TestCheckEmptyHeadRejected(t *testing.T) {
	g := &fakeGit{}
	if got := violationsOf(g.log, shaA, "", ""); len(got) == 0 {
		t.Fatal("empty head accepted")
	}
}

func TestCheckSurfacesGitError(t *testing.T) {
	g := &fakeGit{err: errors.New("boom")}
	got := violationsOf(g.log, shaA, shaB, "")
	if len(got) != 1 || !strings.Contains(got[0], "boom") {
		t.Fatalf("git error not surfaced: %v", got)
	}
}

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestRunExitCodesAndOutput(t *testing.T) {
	tests := []struct {
		name     string
		env      map[string]string
		commits  []commit
		wantCode int
		wantOut  string
	}{
		{"ok", map[string]string{"BASE_SHA": shaA, "HEAD_SHA": shaB}, []commit{{shaA, subjectA}}, 0, "commitlint: ok (1 commits checked)"},
		{"ok counts every commit", map[string]string{"BASE_SHA": shaA, "HEAD_SHA": shaB}, []commit{{shaA, subjectA}, {shaB, "fix: y"}}, 0, "commitlint: ok (2 commits checked)"},
		{"zero commits in non-empty range warns", map[string]string{"BASE_SHA": shaA, "HEAD_SHA": shaB}, nil, 0, "warning: BASE_SHA..HEAD_SHA contains no commits to check"},
		{"unreachable base warns", map[string]string{"BASE_SHA": shaA, "HEAD_SHA": shaB, "UNREACHABLE": "1"}, []commit{{shaB, subjectA}}, 0, "warning: BASE_SHA is not reachable"},
		{"invalid commit", map[string]string{"BASE_SHA": shaA, "HEAD_SHA": shaB}, []commit{{shaA, "nope"}}, 1, "nope"},
		{"invalid title", map[string]string{"BASE_SHA": shaA, "HEAD_SHA": shaB, "PR_TITLE": "bad title"}, []commit{{shaA, subjectA}}, 1, "bad title"},
		{"missing head", map[string]string{}, nil, 1, "HEAD_SHA"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := &fakeGit{commits: tc.commits, unreachable: tc.env["UNREACHABLE"] != ""}
			var out bytes.Buffer

			code := run(context.Background(), envOf(tc.env), g.log, &out)

			if code != tc.wantCode {
				t.Errorf("exit = %d, want %d (output %q)", code, tc.wantCode, out.String())
			}
			if !strings.Contains(out.String(), tc.wantOut) {
				t.Errorf("output %q lacks %q", out.String(), tc.wantOut)
			}
		})
	}
}

func TestParseLog(t *testing.T) {
	raw := shaA + "\x00feat: one\n" + shaB + "\x00fix: two\n\n"
	got, err := parseLog([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (commit{shaA, "feat: one"}) || got[1] != (commit{shaB, "fix: two"}) {
		t.Fatalf("parsed %v", got)
	}
	if got, err := parseLog(nil); err != nil || len(got) != 0 {
		t.Fatalf("empty output: %v %v", got, err)
	}
	if _, err := parseLog([]byte("no-separator\n")); err == nil {
		t.Fatal("malformed line accepted")
	}
}

func TestExecGitAgainstTempRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		// #nosec G204 -- test-only, fixed binary and constant arguments
		cmd := exec.CommandContext(context.Background(), "git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("commit", "-q", "--allow-empty", "-m", "feat: first")
	git("commit", "-q", "--allow-empty", "-m", "not conventional")

	got, err := execGit(dir)(context.Background(), "log", "--format=%H%x00%s", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Subject != "not conventional" || got[1].Subject != "feat: first" {
		t.Fatalf("unexpected commits %v", got)
	}
	if _, err := execGit(dir)(context.Background(), "log", "--format=%H%x00%s", shaA); err == nil {
		t.Fatal("expected git error for unknown revision")
	}
}

func TestCheckSkipsMergesViaGitFlag(t *testing.T) {
	g := &fakeGit{commits: []commit{{shaA, subjectA}}}

	violationsOf(g.log, shaA, shaB, "")
	violationsOf(g.log, "", shaB, "")

	for _, call := range g.logCalls() {
		if !slices.Contains(call, "--no-merges") {
			t.Errorf("git log call lacks --no-merges: %v", call)
		}
	}
}

func TestCheckUnreachableBase(t *testing.T) {
	tests := []struct {
		name        string
		unreachable bool
		wantRange   string
		wantWarning bool
		wantLimit   bool
	}{
		{"reachable base lints the range", false, shaA + ".." + shaB, false, false},
		{"unreachable base lints only HEAD", true, shaB, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := &fakeGit{commits: []commit{{shaB, subjectA}}, unreachable: tc.unreachable}

			rep := check(context.Background(), g.log, shaA, shaB, "")

			if len(rep.Violations) != 0 {
				t.Fatalf("unexpected violations: %v", rep.Violations)
			}
			if len(g.calls) < 2 || strings.Join(g.calls[0], " ") != "cat-file -e "+shaA+"^{commit}" {
				t.Fatalf("base reachability not probed first: %v", g.calls)
			}
			logs := g.logCalls()
			if len(logs) != 1 || logs[0][len(logs[0])-1] != tc.wantRange {
				t.Fatalf("log calls = %v, want range %q", logs, tc.wantRange)
			}
			if slices.Contains(logs[0], "-1") != tc.wantLimit {
				t.Errorf("-1 present = %v, want %v (%v)", !tc.wantLimit, tc.wantLimit, logs[0])
			}
			if (len(rep.Warnings) == 1 && strings.Contains(rep.Warnings[0], "not reachable")) != tc.wantWarning {
				t.Errorf("warnings = %v, wantWarning %v", rep.Warnings, tc.wantWarning)
			}
		})
	}
}

func TestCheckReportCountsCommits(t *testing.T) {
	g := &fakeGit{commits: []commit{{shaA, subjectA}, {shaB, "bad"}}}

	rep := check(context.Background(), g.log, shaA, shaB, "")

	if rep.Checked != 2 || len(rep.Violations) != 1 {
		t.Fatalf("report = %+v, want 2 checked and 1 violation", rep)
	}
}

func TestCheckNoZeroCommitWarningForFullHistory(t *testing.T) {
	g := &fakeGit{}

	rep := check(context.Background(), g.log, "", shaB, "")

	if len(rep.Warnings) != 0 {
		t.Fatalf("unexpected warnings for empty base: %v", rep.Warnings)
	}
}

func TestExecGitSkipsMergeCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		// #nosec G204 -- test-only, fixed binary and constant arguments
		cmd := exec.CommandContext(context.Background(), "git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "feat: base")
	git("checkout", "-q", "-b", "side")
	git("commit", "-q", "--allow-empty", "-m", "fix: side")
	git("checkout", "-q", "main")
	git("commit", "-q", "--allow-empty", "-m", "feat: main work")
	git("merge", "-q", "--no-ff", "-m", "Merge branch 'side'", "side")

	got, err := execGit(dir)(context.Background(), "log", "--no-merges", logFormat, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got {
		if strings.HasPrefix(c.Subject, "Merge") {
			t.Errorf("merge commit not skipped: %v", c)
		}
	}
	if len(got) != 3 {
		t.Fatalf("want 3 non-merge commits, got %v", got)
	}
	if _, err := execGit(dir)(context.Background(), "cat-file", "-e", shaA+"^{commit}"); err == nil {
		t.Fatal("cat-file must fail for an unknown commit")
	}
}
