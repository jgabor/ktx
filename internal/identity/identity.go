// Package identity owns the canonical persisted identity contract.
package identity

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math/big"
	"regexp"
	"unicode/utf8"
)

const (
	domain   = "ktx-tool-call-id-v1"
	alphabet = "abcdefghijklmnopqrstuvwxyz"
	length   = 16
)

var (
	canonicalPattern = regexp.MustCompile(`^[a-z]{16}$`)
	modulus          = new(big.Int).Exp(big.NewInt(int64(len(alphabet))), big.NewInt(length), nil)
)

// IsCanonical reports whether id belongs to the persisted KTX identity domain.
func IsCanonical(id string) bool {
	return canonicalPattern.MatchString(id)
}

// Derive deterministically maps a complete runtime correlation tuple into the
// canonical persisted identity domain.
func Derive(runtime, sessionID, sourceID string) (string, error) {
	fields := []string{runtime, sessionID, sourceID}
	for i := range fields {
		if !utf8.ValidString(fields[i]) {
			return "", errors.New("identity: tuple field is not valid UTF-8")
		}
		fields[i] = trimASCIIWhitespace(fields[i])
		if fields[i] == "" {
			return "", errors.New("identity: tuple field is empty")
		}
	}
	fields[0] = lowerASCII(fields[0])

	preimage := make([]byte, 0, len(domain)+12+len(fields[0])+len(fields[1])+len(fields[2]))
	preimage = append(preimage, domain...)
	for _, field := range fields {
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(field)))
		preimage = append(preimage, size[:]...)
		preimage = append(preimage, field...)
	}
	digest := sha256.Sum256(preimage)
	n := new(big.Int).SetBytes(digest[:])
	n.Mod(n, modulus)

	id := make([]byte, length)
	base := big.NewInt(int64(len(alphabet)))
	rem := new(big.Int)
	for i := length - 1; i >= 0; i-- {
		n.QuoRem(n, base, rem)
		id[i] = alphabet[rem.Int64()]
	}
	return string(id), nil
}

func trimASCIIWhitespace(value string) string {
	start, end := 0, len(value)
	for start < end && isASCIIWhitespace(value[start]) {
		start++
	}
	for end > start && isASCIIWhitespace(value[end-1]) {
		end--
	}
	return value[start:end]
}

func isASCIIWhitespace(b byte) bool {
	return b == 0x20 || (b >= 0x09 && b <= 0x0d)
}

func lowerASCII(value string) string {
	b := []byte(value)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
