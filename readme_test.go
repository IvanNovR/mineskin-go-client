package mineskin_test

import (
	"os"
	"strings"
	"testing"
)

// exampleQuickStartBody returns the body of Example_quickStart as the README
// shows it: without the example-only lines and the Output comment, and with
// one leading tab removed.
func exampleQuickStartBody(t *testing.T, src string) []string {
	t.Helper()
	_, rest, ok := strings.Cut(src, "func Example_quickStart() {\n")
	if !ok {
		t.Fatal("example_test.go has no Example_quickStart")
	}
	body, _, ok := strings.Cut(rest, "\n}\n")
	if !ok {
		t.Fatal("Example_quickStart has no closing brace")
	}
	var lines []string
	for line := range strings.SplitSeq(body, "\n") {
		if strings.Contains(line, "// example-only") || strings.HasPrefix(strings.TrimSpace(line), "// Output:") {
			continue
		}
		lines = append(lines, strings.TrimPrefix(line, "\t"))
	}
	return lines
}

// readmeQuickStart returns the lines between the quick start markers of the
// README without the code fences.
func readmeQuickStart(t *testing.T, readme string) []string {
	t.Helper()
	_, rest, ok := strings.Cut(readme, "<!-- quickstart:start -->\n")
	if !ok {
		t.Fatal("README.md has no quickstart:start marker")
	}
	block, _, ok := strings.Cut(rest, "<!-- quickstart:end -->")
	if !ok {
		t.Fatal("README.md has no quickstart:end marker")
	}
	var lines []string
	for line := range strings.SplitSeq(strings.TrimSuffix(block, "\n"), "\n") {
		if strings.HasPrefix(line, "```") {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func TestREADMEQuickStartMatchesExample(t *testing.T) {
	t.Parallel()
	example, err := os.ReadFile("example_test.go")
	if err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}

	want := exampleQuickStartBody(t, string(example))
	got := readmeQuickStart(t, string(readme))

	for i := range max(len(want), len(got)) {
		var w, g string
		if i < len(want) {
			w = want[i]
		}
		if i < len(got) {
			g = got[i]
		}
		if w != g || (i >= len(want)) != (i >= len(got)) {
			t.Fatalf("README quick start differs from Example_quickStart at line %d:\nREADME:  %q\nExample: %q", i+1, g, w)
		}
	}
}
