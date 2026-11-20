package mineskin

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseDotEnv(t *testing.T) {
	t.Parallel()
	const content = "# comment\n" +
		"\n" +
		"PLAIN=value\n" +
		"  SPACED = padded value  \n" +
		"DOUBLE=\"quoted # not a comment\"\n" +
		"SINGLE='single quoted'\n" +
		"export EXPORTED=yes\n" +
		"INLINE=before # after\n" +
		"EMPTY=\n" +
		"CRLF=windows\r\n" +
		"EQUALS=a=b=c\n" +
		"no equals sign\n" +
		"=novalue\n" +
		"SHELL=$(echo pwned)\n" +
		"UNBALANCED=\"half\n" +
		"QDC=\"v\" # c\n" +
		"QSC='v' # c\n" +
		"UQC=v # c\n" +
		"HASH=\"a#b\"\n" +
		"QCRLF=\"v\" # c\r\n" +
		"EXPQ=export \"x\"\n"
	want := map[string]string{
		"PLAIN":      "value",
		"SPACED":     "padded value",
		"DOUBLE":     "quoted # not a comment",
		"SINGLE":     "single quoted",
		"EXPORTED":   "yes",
		"INLINE":     "before",
		"EMPTY":      "",
		"CRLF":       "windows",
		"EQUALS":     "a=b=c",
		"SHELL":      "$(echo pwned)",
		"UNBALANCED": "\"half",
		"QDC":        "v",
		"QSC":        "v",
		"UQC":        "v",
		"HASH":       "a#b",
		"QCRLF":      "v",
		"EXPQ":       "export \"x\"",
	}

	if got := parseDotEnv(content); !reflect.DeepEqual(got, want) {
		t.Errorf("parseDotEnv() = %v, want %v", got, want)
	}
}

func TestEnvOrFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("FAKE_LIVE_KEY=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("file value when the variable is unset", func(t *testing.T) {
		t.Setenv("FAKE_LIVE_KEY", "")
		if got := envOrFile("FAKE_LIVE_KEY", path); got != "from-file" {
			t.Errorf("envOrFile() = %q, want from-file", got)
		}
	})
	t.Run("environment wins over the file", func(t *testing.T) {
		t.Setenv("FAKE_LIVE_KEY", "from-env")
		if got := envOrFile("FAKE_LIVE_KEY", path); got != "from-env" {
			t.Errorf("envOrFile() = %q, want from-env", got)
		}
	})
	t.Run("missing file and variable is empty", func(t *testing.T) {
		t.Setenv("FAKE_LIVE_KEY", "")
		if got := envOrFile("FAKE_LIVE_KEY", filepath.Join(t.TempDir(), "absent")); got != "" {
			t.Errorf("envOrFile() = %q, want empty", got)
		}
	})
}
