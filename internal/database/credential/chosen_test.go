package credential

import "testing"

func TestValidateChosen(t *testing.T) {
	good := []string{"abcdefghijklmnop", "Chosen-Passw0rd!x#2026", "~{|}]^_[@?=>" + "<;:/.-,+*)(", "aA0!#$%&()*+,-./"}
	for _, password := range good {
		if err := ValidateChosen(password); err != nil {
			t.Errorf("%q: %v", password, err)
		}
	}
	bad := map[string]error{
		"short":                    ErrChosenLength,
		"":                         ErrChosenLength,
		"has a space 0123456789":   ErrChosenCharset,
		"quote'd-0123456789-abc":   ErrChosenCharset,
		"back\\slash-0123456789ab": ErrChosenCharset,
		"pässw0rd-0123456789":      ErrChosenCharset,
	}
	for password, want := range bad {
		if err := ValidateChosen(password); err != want {
			t.Errorf("%q: got %v want %v", password, err, want)
		}
	}
	long := make([]byte, 129)
	for i := range long {
		long[i] = 'a'
	}
	if err := ValidateChosen(string(long)); err != ErrChosenLength {
		t.Errorf("long: %v", err)
	}
}
