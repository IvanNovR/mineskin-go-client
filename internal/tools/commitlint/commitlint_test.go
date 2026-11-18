package main

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
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
	commits []commit
	err     error
	calls   [][]string
}

func (f *fakeGit) log(_ context.Context, args ...string) ([]commit, error) {
	f.calls = append(f.calls, args)
	return f.commits, f.err
}

func TestSubjectPattern(t *testing.T) {
	valid := []string{
		"feat!: x", "fix(ci): x", "chore(main): release 0.1.0", "feat(api)!: break",
		"revert: undo", "docs: update ✨", "build: deps",
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

	got := check(context.Background(), g.log, shaA, shaB, "")

	if len(got) != 0 {
		t.Fatalf("unexpected violations: %v", got)
	}
	want := []string{"log", "--format=%H%x00%s", shaA + ".." + shaB}
	if len(g.calls) != 1 || strings.Join(g.calls[0], " ") != strings.Join(want, " ") {
		t.Fatalf("git calls = %v, want %v", g.calls, want)
	}
}

func TestCheckReportsAllInvalidSubjectsIncludingBehindValidTip(t *testing.T) {
	g := &fakeGit{commits: []commit{
		{shaB, "feat: valid tip"},
		{shaA, "oops one"},
		{"cccccccccccccccccccccccccccccccccccccccc", "Another bad"},
	}}

	got := check(context.Background(), g.log, shaA, shaB, "")

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
	if got := check(context.Background(), g.log, shaA, shaB, ""); len(got) != 0 {
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
			got := check(context.Background(), g.log, shaA, shaB, tc.title)
			if len(got) != tc.wantN {
				t.Fatalf("want %d violations, got %v", tc.wantN, got)
			}
		})
	}
}

func TestCheckFullHistoryWhenBaseMissingOrZero(t *testing.T) {
	for _, base := range []string{"", zeroSHA, "  "} {
		g := &fakeGit{commits: []commit{{shaA, subjectA}}}
		got := check(context.Background(), g.log, base, shaB, "")
		if len(got) != 0 {
			t.Fatalf("base %q: unexpected violations %v", base, got)
		}
		if len(g.calls) != 1 || g.calls[0][len(g.calls[0])-1] != shaB {
			t.Fatalf("base %q: want full-history range %q, got %v", base, shaB, g.calls)
		}
	}
}

func TestCheckRejectsBadSHAs(t *testing.T) {
	bad := []string{"abc", "--output=x", strings.Repeat("g", 40), strings.Repeat("a", 41), "main; rm -rf /", strings.ToUpper(shaA)}
	for _, s := range bad {
		g := &fakeGit{}
		if got := check(context.Background(), g.log, s, shaB, ""); len(got) == 0 {
			t.Errorf("bad base %q accepted", s)
		}
		if got := check(context.Background(), g.log, shaA, s, ""); len(got) == 0 {
			t.Errorf("bad head %q accepted", s)
		}
		if len(g.calls) != 0 {
			t.Errorf("git must not run for bad SHA %q", s)
		}
	}
}

func TestCheckEmptyHeadRejected(t *testing.T) {
	g := &fakeGit{}
	if got := check(context.Background(), g.log, shaA, "", ""); len(got) == 0 {
		t.Fatal("empty head accepted")
	}
}

func TestCheckSurfacesGitError(t *testing.T) {
	g := &fakeGit{err: errors.New("boom")}
	got := check(context.Background(), g.log, shaA, shaB, "")
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
		{"ok", map[string]string{"BASE_SHA": shaA, "HEAD_SHA": shaB}, []commit{{shaA, subjectA}}, 0, "ok"},
		{"invalid commit", map[string]string{"BASE_SHA": shaA, "HEAD_SHA": shaB}, []commit{{shaA, "nope"}}, 1, "nope"},
		{"invalid title", map[string]string{"BASE_SHA": shaA, "HEAD_SHA": shaB, "PR_TITLE": "bad title"}, []commit{{shaA, subjectA}}, 1, "bad title"},
		{"missing head", map[string]string{}, nil, 1, "HEAD_SHA"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := &fakeGit{commits: tc.commits}
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
