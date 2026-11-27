package mysql

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestCachingSHA2LiteralFixedSalt(t *testing.T) {
	got := cachingSHA2Literal("Abcdefghijklmnop1234", "abcdefghijklmnopqrst")
	want := "0x244124303035246162636465666768696a6b6c6d6e6f707172737442655647792f575a6c49776561787a45714e446a3857707a4e576a3579475539337042522f513470756a33"
	if got != want {
		t.Fatalf("got %s", got)
	}
}

func TestCachingSHA2LiteralLayout(t *testing.T) {
	literal, err := CachingSHA2HexLiteral("Abcdefghijklmnop1234")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(literal, "0x"))
	if err != nil || len(raw) != 7+20+43 {
		t.Fatalf("length %d: %v", len(raw), err)
	}
	if string(raw[:7]) != "$A$005$" {
		t.Fatalf("prefix %q", raw[:7])
	}
	other, _ := CachingSHA2HexLiteral("Abcdefghijklmnop1234")
	if other == literal {
		t.Fatal("two literals share one salt")
	}
}

func TestCachingSHA2LiteralHandlesLongPasswords(t *testing.T) {
	long := strings.Repeat("p", 100)
	if got := cachingSHA2Literal(long, "abcdefghijklmnopqrst"); !strings.HasPrefix(got, "0x24412430") || len(got) != 2+2*70 {
		t.Fatalf("literal %s", got)
	}
}
