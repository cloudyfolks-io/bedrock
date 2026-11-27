package credential

import (
	"regexp"
	"testing"
)

func TestPasswordIsRandomAlphanumeric(t *testing.T) {
	pattern := regexp.MustCompile(`^[A-Za-z0-9]{32}$`)
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		password, err := Password()
		if err != nil || !pattern.MatchString(password) || seen[password] {
			t.Fatalf("password %q %v", password, err)
		}
		seen[password] = true
	}
}

func TestScramMatchesAKnownVector(t *testing.T) {
	got, err := ScramSHA256WithSalt("pencil", []byte("0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	const want = "SCRAM-SHA-256$4096:MDEyMzQ1Njc4OWFiY2RlZg==$nQpbZ77WudtqufPwikHXGRt6g2QJ4zns8bZLw273DRM=:jn2amWP1q1h+jgjy0YTO14S6/F02SV7taipOeB7ef20="
	if got != want {
		t.Fatalf("verifier %s", got)
	}
}

func TestScramUsesAFreshSaltEachTime(t *testing.T) {
	first, _ := ScramSHA256("same")
	second, _ := ScramSHA256("same")
	if first == second {
		t.Fatal("two verifiers of one password are equal")
	}
}
