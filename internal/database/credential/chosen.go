package credential

import (
	"errors"
	"strings"
)

const (
	MinChosenLength = 16
	MaxChosenLength = 128
	chosenSymbols   = "!#$%&()*+,-./:;<=>?@[]^_{|}~"
)

var (
	ErrChosenLength  = errors.New("the password needs 16 to 128 characters")
	ErrChosenCharset = errors.New("the password has a character that is not allowed: use letters, digits and !#$%&()*+,-./:;<=>?@[]^_{|}~")
)

func ValidateChosen(password string) error {
	if len(password) < MinChosenLength || len(password) > MaxChosenLength {
		return ErrChosenLength
	}
	for _, r := range password {
		switch {
		case r >= 0x61 && r <= 0x7a, r >= 0x41 && r <= 0x5a, r >= 0x30 && r <= 0x39, strings.ContainsRune(chosenSymbols, r):
		default:
			return ErrChosenCharset
		}
	}
	return nil
}
