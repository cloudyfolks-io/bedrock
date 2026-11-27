package engine

import (
	"errors"
	"fmt"
	"testing"
)

func TestRandomPasswordIsLongAndAlphanumeric(t *testing.T) {
	first, err := RandomPassword()
	if err != nil {
		t.Fatal(err)
	}
	second, _ := RandomPassword()
	if len(first) != 32 || first == second {
		t.Fatalf("passwords %q %q", first, second)
	}
	for _, r := range first {
		if !('a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9') {
			t.Fatalf("character %q in %q", r, first)
		}
	}
}

func TestErrorKindsSurviveWrapping(t *testing.T) {
	if !IsExists(fmt.Errorf("create: %w", ErrExists)) || IsExists(ErrNotFound) {
		t.Error("IsExists")
	}
	if !IsNotFound(fmt.Errorf("drop: %w", ErrNotFound)) || IsNotFound(errors.New("other")) {
		t.Error("IsNotFound")
	}
}

func TestLinesDropsBlankLines(t *testing.T) {
	got := Lines("a\n\n  b  \r\n")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("lines %q", got)
	}
}
