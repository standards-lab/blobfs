package blobfs_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/standards-lab/blobfs"
)

type azureRules struct{}

func (azureRules) ValidateKey(key string) error {
	switch {
	case key == "":
		return errors.New("empty key")
	case !utf8.ValidString(key):
		return errors.New("invalid UTF-8")
	case utf8.RuneCountInString(key) > 1024:
		return errors.New("over 1024 characters")
	case strings.ContainsFunc(key, unicode.IsControl):
		return errors.New("control character")
	case strings.HasSuffix(key, ".") || strings.HasSuffix(key, "/") || strings.HasSuffix(key, `\`):
		return errors.New("trailing . / or \\")
	}
	segments := strings.Split(key, "/")
	if len(segments) > 254 {
		return errors.New("over 254 segments")
	}
	for _, s := range segments {
		if strings.HasSuffix(s, ".") {
			return errors.New("segment ends in .")
		}
	}
	return nil
}

type limited int

func (l limited) ValidateKey(key string) error {
	if n := utf8.RuneCountInString(key); n > int(l) {
		return fmt.Errorf("%d characters exceeds the limit of %d", n, int(l))
	}
	return nil
}

type refusing struct{ err error }

func (r refusing) ValidateKey(string) error { return r.err }

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "report.pdf", "report.pdf"},
		{"unicode kept", "résumé.pdf", "résumé.pdf"},
		{"slash", "a/b.txt", "a_b.txt"},
		{"backslash", `a\b.txt`, "a_b.txt"},
		{"control", "a\x00b\tc", "a_b_c"},
		{"c1 control", "a\u0085b", "a_b"},
		{"trailing dot", "name.", "name"},
		{"trailing dots and spaces", "name. . ", "name"},
		{"trailing space", "name ", "name"},
		{"dot", ".", "file"},
		{"dot dot", "..", "file"},
		{"whitespace only", "   ", "file"},
		{"empty", "", "file"},
		{"invalid utf-8", "a\xffb", "a_b"},
		{"leading dot kept", ".hidden", ".hidden"},
		{"internal spaces kept", "my report.pdf", "my report.pdf"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := blobfs.SanitizeFilename(tt.in); got != tt.want {
				t.Errorf("SanitizeFilename(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizedKeysSatisfyAzure(t *testing.T) {
	names := []string{
		"", ".", "..", "...", "   ", "\t\n", "a/b/c", `a\`, "trailing.", "trailing ",
		"ctl\x1fchar", "del\x7f", "c1\u009f", "bad\xffutf8", "\xff", "/", "//",
		"name./", "a.", strings.Repeat("x", blobfs.MaxNameLength),
		strings.Repeat("é", blobfs.MaxNameLength),
	}
	id := blobfs.NewID()
	for _, name := range names {
		key, err := blobfs.NewKey(azureRules{}, id, name)
		if err != nil {
			t.Errorf("NewKey(azure, id, %q): %v", name, err)
			continue
		}
		if !strings.HasPrefix(key, id+"/") {
			t.Errorf("NewKey(azure, id, %q) = %q, want prefix %q", name, key, id+"/")
		}
	}
}

func TestNewKey(t *testing.T) {
	id := "0192b8f0-0000-7000-8000-000000000000"
	key, err := blobfs.NewKey(azureRules{}, id, "notes/2024.txt")
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	if want := id + "/notes_2024.txt"; key != want {
		t.Errorf("NewKey = %q, want %q", key, want)
	}
}

func TestNewKeyCountsRunes(t *testing.T) {
	const id = "id"
	const limit = 50
	fits := strings.Repeat("é", limit-utf8.RuneCountInString(id+"/"))
	key, err := blobfs.NewKey(limited(limit), id, fits)
	if err != nil {
		t.Fatalf("NewKey at the rune limit: %v", err)
	}
	if n := utf8.RuneCountInString(key); n != limit {
		t.Fatalf("key is %d runes, want %d", n, limit)
	}
	if len(key) <= limit {
		t.Fatalf("key is %d bytes; the case needs more bytes than runes", len(key))
	}

	over := fits + "é"
	_, err = blobfs.NewKey(limited(limit), id, over)
	if !errors.Is(err, blobfs.ErrInvalidKey) {
		t.Fatalf("NewKey one rune over = %v, want ErrInvalidKey", err)
	}
	var keyErr *blobfs.KeyError
	if !errors.As(err, &keyErr) {
		t.Fatalf("error is %T, want *KeyError", err)
	}
	if keyErr.Key != id+"/"+over {
		t.Errorf("KeyError.Key = %q, want the refused key", keyErr.Key)
	}
}

func TestNewKeyStoreRefusal(t *testing.T) {
	cause := errors.New("store says no")
	_, err := blobfs.NewKey(refusing{err: cause}, "id", "name")
	if !errors.Is(err, blobfs.ErrInvalidKey) {
		t.Errorf("errors.Is(err, ErrInvalidKey) = false; err = %v", err)
	}
	if !errors.Is(err, cause) {
		t.Errorf("errors.Is(err, cause) = false; the store's error is not reachable")
	}
}
