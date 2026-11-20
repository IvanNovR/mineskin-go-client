package mineskin

import (
	"os"
	"path/filepath"
	"strings"
)

// parseDotEnv parses KEY=VALUE lines. Blank lines and lines starting with #
// are ignored; an optional "export " prefix is accepted; a value may be wrapped
// in one pair of single or double quotes (then it is taken literally). An
// unquoted value ends at an inline " #" comment. It never runs the file as
// shell code. Malformed lines (no "=" or an empty key) are skipped.
func parseDotEnv(content string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			continue
		}
		out[key] = dotEnvValue(strings.TrimSpace(value))
	}
	return out
}

func dotEnvValue(v string) string {
	if v != "" && (v[0] == '"' || v[0] == '\'') {
		// A quoted value ends at the matching quote; only whitespace and a #
		// comment may follow it. Anything else is not a quoted value.
		if end := strings.IndexByte(v[1:], v[0]); end >= 0 {
			rest := strings.TrimSpace(v[end+2:])
			if rest == "" || strings.HasPrefix(rest, "#") {
				return v[1 : end+1]
			}
		}
	}
	if before, _, found := strings.Cut(v, " #"); found {
		v = before
	}
	return strings.TrimSpace(v)
}

// envOrFile returns the process environment variable key when it is set and
// non-empty, otherwise the value of key in the dotenv file at path (a missing
// or unreadable file counts as empty). The environment always wins.
func envOrFile(key, path string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return ""
	}
	return parseDotEnv(string(b))[key]
}
