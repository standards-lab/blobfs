package blobfs_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/standards-lab/blobfs"
)

// TestNormalizeNameNFC fixes the collision rule: the composed and the
// decomposed spelling of one name normalize to the same string, so the
// unique constraint on the normalized name treats them as one.
func TestNormalizeNameNFC(t *testing.T) {
	// Built from code points so that no editor can normalize the fixtures.
	composed := "caf" + string(rune(0x00E9))    // é as one code point
	decomposed := "cafe" + string(rune(0x0301)) // e followed by a combining acute
	if composed == decomposed {
		t.Fatal("the fixtures are already equal; the test proves nothing")
	}
	a, b := blobfs.NormalizeName(composed), blobfs.NormalizeName(decomposed)
	if a != b {
		t.Errorf("NormalizeName(%q) = %q, NormalizeName(%q) = %q; want equal", composed, a, decomposed, b)
	}
	if a != composed {
		t.Errorf("NormalizeName(composed) = %q, want the composed form unchanged", a)
	}
	if plain := "report.pdf"; blobfs.NormalizeName(plain) != plain {
		t.Errorf("NormalizeName(%q) changed an ASCII name", plain)
	}
}

func TestValidateName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{"plain", "report.pdf", true},
		{"unicode", "résumé.pdf", true},
		{"spaces inside", "my report.pdf", true},
		{"leading dot", ".hidden", true},
		{"backslash allowed", `a\b`, true},
		{"trailing dot allowed", "name.", true},
		{"three dots", "...", true},
		{"at the limit", strings.Repeat("x", blobfs.MaxNameLength), true},
		{"at the limit in multibyte runes", strings.Repeat("é", blobfs.MaxNameLength), true},
		{"empty", "", false},
		{"dot", ".", false},
		{"dot dot", "..", false},
		{"slash", "a/b", false},
		{"leading slash", "/a", false},
		{"nul", "a\x00b", false},
		{"tab", "a\tb", false},
		{"newline", "a\nb", false},
		{"delete", "a\x7fb", false},
		{"c1 control", "a\u0085b", false},
		{"invalid utf-8", "a\xffb", false},
		{"one over the limit", strings.Repeat("x", blobfs.MaxNameLength+1), false},
		{"one over the limit in multibyte runes", strings.Repeat("é", blobfs.MaxNameLength+1), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := blobfs.ValidateName(tt.in)
			if tt.ok {
				if err != nil {
					t.Errorf("ValidateName(%q) = %v, want nil", tt.in, err)
				}
				return
			}
			if !errors.Is(err, blobfs.ErrInvalidName) {
				t.Fatalf("ValidateName(%q) = %v, want ErrInvalidName", tt.in, err)
			}
			var nameErr *blobfs.NameError
			if !errors.As(err, &nameErr) {
				t.Fatalf("error is %T, want *NameError", err)
			}
			if nameErr.Name != tt.in || nameErr.Reason == "" {
				t.Errorf("NameError = %+v, want the name and a reason", *nameErr)
			}
		})
	}
}
