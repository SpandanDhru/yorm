// Package ids generates random identifiers such as "tok_3hkq7w2m9x4bcd5e".
package ids

import (
	"crypto/rand"
	"encoding/base32"
)

var enc = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// New returns prefix + "_" + 16 random lowercase base32 characters (80 bits).
func New(prefix string) string {
	return prefix + "_" + Random(10)
}

// Random returns n random bytes encoded as lowercase base32.
func Random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error
	return enc.EncodeToString(b)
}
