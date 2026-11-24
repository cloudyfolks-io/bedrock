package saxess

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"golang.org/x/crypto/sha3"
)

// CardProofFormat identifies the only signing profile accepted by VerifyCardProof.
const CardProofFormat = "secp256k1-sha256-eth-address-rsv-hex-v1"

var ErrInvalidCardProof = errors.New("invalid card proof")

// VerifyCardProof verifies SHA-256 of the exact payload bytes, without a message
// prefix or decoding. publicKey is a 0x-prefixed Ethereum address. signature is
// 0x-prefixed hex encoding of low-S R[32] || S[32] || V[1], with V equal to 0 or 1.
// The caller must supply the original server-issued payload and enforce its lifetime
// and single use; for approval it must also supply the enrolled address.
//
// The Pro notes describe recoverable secp256k1 and a card address, but do not
// specify the full wire encoding. Mobile interoperability is unverified.
func VerifyCardProof(publicKey, payload, signature string) error {
	var address [20]byte
	var compact [65]byte
	if payload == "" || !decodeProofHex(address[:], publicKey) || !decodeProofHex(compact[:], signature) {
		return ErrInvalidCardProof
	}
	v := compact[64]
	var s secp256k1.ModNScalar
	if v > 1 || s.SetByteSlice(compact[32:64]) || s.IsOverHalfOrder() {
		return ErrInvalidCardProof
	}
	// RecoverCompact expects a header byte followed by R and S.
	copy(compact[1:], compact[:64])
	compact[0] = 27 + v
	digest := sha256.Sum256([]byte(payload))
	pub, _, err := ecdsa.RecoverCompact(compact[:], digest[:])
	if err != nil {
		return ErrInvalidCardProof
	}
	hash := sha3.NewLegacyKeccak256()
	hash.Write(pub.SerializeUncompressed()[1:])
	if !bytes.Equal(address[:], hash.Sum(nil)[12:]) {
		return ErrInvalidCardProof
	}
	return nil
}

func decodeProofHex(dst []byte, src string) bool {
	if len(src) != 2+2*len(dst) || !strings.HasPrefix(src, "0x") {
		return false
	}
	_, err := hex.Decode(dst, []byte(src[2:]))
	return err == nil
}
