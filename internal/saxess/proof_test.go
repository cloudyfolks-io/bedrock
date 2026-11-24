package saxess

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

func TestVerifyCardProof(t *testing.T) {
	const address = "0x7e5f4552091a69125d5dfcb7b8c2659029395bdf" // Ethereum address for private key 1.
	key := secp256k1.PrivKeyFromBytes([]byte{1})
	for _, payload := range []string{"invite-token.é", "201::1763974961::employee@example.com"} {
		t.Run(payload, func(t *testing.T) {
			digest := sha256.Sum256([]byte(payload))
			compact := ecdsa.SignCompact(key, digest[:], false)
			rsv := append(append([]byte(nil), compact[1:]...), compact[0]-27)
			signature := "0x" + hex.EncodeToString(rsv)
			if err := VerifyCardProof(address, payload, signature); err != nil {
				t.Fatalf("valid proof: %v", err)
			}

			highS := append([]byte(nil), rsv...)
			var s secp256k1.ModNScalar
			s.SetByteSlice(highS[32:64])
			s.Negate().PutBytesUnchecked(highS[32:64])
			highS[64] ^= 1

			for _, tc := range []struct{ name, address, payload, signature string }{
				{"wrong address", "0x" + strings.Repeat("0", 40), payload, signature},
				{"wrong payload", address, payload + " ", signature},
				{"empty payload", address, "", signature},
				{"bad address hex", address[:40] + "zz", payload, signature},
				{"missing address prefix", address[2:], payload, signature},
				{"short signature", address, payload, signature[:len(signature)-2]},
				{"missing signature prefix", address, payload, signature[2:]},
				{"bad signature hex", address, payload, signature[:len(signature)-4] + "zz00"},
				{"legacy recovery byte", address, payload, signature[:len(signature)-2] + "1b"},
				{"zero R", address, payload, "0x" + strings.Repeat("0", 64) + signature[66:]},
				{"high S", address, payload, "0x" + hex.EncodeToString(highS)},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if err := VerifyCardProof(tc.address, tc.payload, tc.signature); err != ErrInvalidCardProof {
						t.Fatalf("got %v, want %v", err, ErrInvalidCardProof)
					}
				})
			}
		})
	}
}
