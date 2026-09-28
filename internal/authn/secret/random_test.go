package secret

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"
)

func TestBase62HasNoBias(t *testing.T) {
	t.Run("no rejects", func(t *testing.T) {
		reader := bytes.NewReader([]byte{0, 1, 2})
		got, err := Base62(reader, 3)
		if err != nil {
			t.Fatal(err)
		}
		want := string([]byte{base62Alphabet[0], base62Alphabet[1], base62Alphabet[2]})
		if got != want {
			t.Fatalf("Base62 = %q, want %q", got, want)
		}
	})
	t.Run("skips out of range bytes", func(t *testing.T) {
		reader := bytes.NewReader([]byte{250, 255, 254, 5})
		got, err := Base62(reader, 1)
		if err != nil {
			t.Fatal(err)
		}
		want := string(base62Alphabet[5])
		if got != want {
			t.Fatalf("Base62 = %q, want %q (250, 255 and 254 are out of range for 62 letters and must be skipped)", got, want)
		}
	})
	t.Run("skips a long run of out of range bytes", func(t *testing.T) {
		rejects := bytes.Repeat([]byte{255}, 30)
		reader := bytes.NewReader(append(rejects, 42))
		got, err := Base62(reader, 1)
		if err != nil {
			t.Fatal(err)
		}
		want := string(base62Alphabet[42])
		if got != want {
			t.Fatalf("Base62 = %q, want %q (30 consecutive out of range bytes must all be skipped)", got, want)
		}
	})
}

func TestAPITokenFormat(t *testing.T) {
	token, err := NewAPIToken(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, "brk_") || len(token) != 44 {
		t.Fatalf("token %q must be brk_ plus 40 characters", token)
	}
	if !IsAPIToken(token) {
		t.Fatalf("IsAPIToken(%q) = false", token)
	}
	if IsAPIToken(token[:len(token)-1]) {
		t.Fatal("IsAPIToken must refuse a truncated token")
	}
	if IsAPIToken("brk_" + strings.Repeat("!", 40)) {
		t.Fatal("IsAPIToken must refuse characters outside the alphabet")
	}
}
