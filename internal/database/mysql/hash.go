package mysql

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
)

const (
	cryptAlphabet = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	cryptRounds   = 5000
	saltLength    = 20
)

func CachingSHA2HexLiteral(password string) (string, error) {
	salt, err := engine.RandomString(saltLength)
	if err != nil {
		return "", err
	}
	return cachingSHA2Literal(password, salt), nil
}

func cachingSHA2Literal(password, salt string) string {
	stored := "$A$005$" + salt + sha256Crypt([]byte(password), []byte(salt), cryptRounds)
	return "0x" + hex.EncodeToString([]byte(stored))
}

func digestOfPasswordAndSalt(password, salt []byte) []byte {
	sum := sha256.New()
	sum.Write(password)
	sum.Write(salt)
	sum.Write(password)
	return sum.Sum(nil)
}

func initialDigest(password, salt []byte) []byte {
	alternate := digestOfPasswordAndSalt(password, salt)
	digest := sha256.New()
	digest.Write(password)
	digest.Write(salt)
	remaining := len(password)
	for ; remaining > 32; remaining -= 32 {
		digest.Write(alternate)
	}
	digest.Write(alternate[:remaining])
	for bits := len(password); bits > 0; bits >>= 1 {
		if bits&1 != 0 {
			digest.Write(alternate)
		} else {
			digest.Write(password)
		}
	}
	return digest.Sum(nil)
}

func stretch(password, salt, digest []byte, rounds int) []byte {
	passwordBytes := repeatTo(repeatedDigest(password, len(password)), len(password))
	saltBytes := repeatTo(repeatedDigest(salt, 16+int(digest[0])), len(salt))
	for round := 0; round < rounds; round++ {
		next := sha256.New()
		if round&1 != 0 {
			next.Write(passwordBytes)
		} else {
			next.Write(digest)
		}
		if round%3 != 0 {
			next.Write(saltBytes)
		}
		if round%7 != 0 {
			next.Write(passwordBytes)
		}
		if round&1 != 0 {
			next.Write(digest)
		} else {
			next.Write(passwordBytes)
		}
		digest = next.Sum(nil)
	}
	return digest
}

func repeatedDigest(data []byte, times int) []byte {
	digest := sha256.New()
	for i := 0; i < times; i++ {
		digest.Write(data)
	}
	return digest.Sum(nil)
}

func repeatTo(block []byte, length int) []byte {
	out := make([]byte, 0, length)
	for len(out)+len(block) <= length {
		out = append(out, block...)
	}
	return append(out, block[:length-len(out)]...)
}

func cryptEncode(sum []byte) string {
	var out []byte
	emit := func(high, middle, low byte, count int) {
		word := uint(high)<<16 | uint(middle)<<8 | uint(low)
		for i := 0; i < count; i++ {
			out = append(out, cryptAlphabet[word&0x3f])
			word >>= 6
		}
	}
	for _, group := range [][3]int{{0, 10, 20}, {21, 1, 11}, {12, 22, 2}, {3, 13, 23}, {24, 4, 14}, {15, 25, 5}, {6, 16, 26}, {27, 7, 17}, {18, 28, 8}, {9, 19, 29}} {
		emit(sum[group[0]], sum[group[1]], sum[group[2]], 4)
	}
	emit(0, sum[31], sum[30], 3)
	return string(out)
}

func sha256Crypt(password, salt []byte, rounds int) string {
	return cryptEncode(stretch(password, salt, initialDigest(password, salt), rounds))
}
