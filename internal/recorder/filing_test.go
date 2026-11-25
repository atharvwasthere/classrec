package recorder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSanitizeTitle(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"meet boilerplate, edge and profile", "Meet - xyz-abcd-efg - Personal - Microsoft Edge", "xyz-abcd-efg"},
		{"edge's zero-width space", "Meet - Maths 101 - Microsoft​ Edge", "Maths 101"},
		{"no profile segment", "Meet - Maths 101 - Microsoft Edge", "Maths 101"},
		{"title with a dash of its own", "Meet - Algebra - Part 2 - Personal - Microsoft Edge", "Algebra - Part 2"},
		{"zoom boilerplate only", "Zoom Meeting", "Class"},
		{"zoom boilerplate with a topic", "Zoom Meeting - Algebra", "Algebra"},
		{"zoom boilerplate in any case", "zoom meeting", "Class"},
		{"reserved characters", `Maths: Algebra/Geometry? "Part" <1> | *final* \x`, "Maths AlgebraGeometry Part 1 final x"},
		{"control characters", "Tab\there\x01 x\x7f", "Tabhere x"},
		{"collapses spaces", "  a    b \t c  ", "a b c"},
		{"trailing dots and spaces", "Maths . . ", "Maths"},
		{"only dots", "...", "Class"},
		{"empty", "", "Class"},
		{"only boilerplate", "Meet - ", "Class"},
		{"hindi", "Meet - गणित कक्षा - Microsoft Edge", "गणित कक्षा"},
		{"sixty characters at most", strings.Repeat("a", 100), strings.Repeat("a", 60)},
		{"sixty runes, not bytes", strings.Repeat("क", 70), strings.Repeat("क", 60)},
		{"cut then trimmed", strings.Repeat("a", 59) + " bbb", strings.Repeat("a", 59)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeTitle(tt.in); got != tt.want {
				t.Errorf("SanitizeTitle(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestClassFolderName(t *testing.T) {
	began := time.Date(2026, 11, 25, 14, 3, 0, 0, time.UTC)
	tests := []struct{ app, title, want string }{
		{"Meet", "Meet - Maths - Personal - Microsoft Edge", "2026-11-25 Wednesday - Maths (Meet)"},
		{"Zoom", "Zoom Meeting", "2026-11-25 Wednesday - Class (Zoom)"},
	}
	for _, tt := range tests {
		if got := ClassFolderName(began, tt.app, tt.title); got != tt.want {
			t.Errorf("got %q, want %q", got, tt.want)
		}
	}
}

func TestTwoClassesNeverShareAFolder(t *testing.T) {
	root := t.TempDir()
	first, err := makeClassDir(root, "2026-11-25 Wednesday - Maths (Meet)")
	if err != nil {
		t.Fatal(err)
	}
	second, err := makeClassDir(root, "2026-11-25 Wednesday - Maths (Meet)")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || filepath.Base(second) != "2026-11-25 Wednesday - Maths (Meet) (2)" {
		t.Fatalf("got %q and %q", first, second)
	}
	if _, err := os.Stat(first); err != nil {
		t.Fatal(err)
	}
}
