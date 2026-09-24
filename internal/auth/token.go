// Package auth issues and verifies the signed join tokens that identify a
// member of a session. There are no accounts in v1: the token is the identity.
//
// A token is base64url(JSON claims) + "." + base64url(HMAC-SHA256 of the first part).
package auth

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DefaultTTL is how long a join token stays valid.
const DefaultTTL = 30 * 24 * time.Hour

// MinKeyLen is the shortest signing key NewSigner accepts.
const MinKeyLen = 32

var (
	ErrMalformed = errors.New("auth: malformed token")
	ErrSignature = errors.New("auth: bad signature")
	ErrExpired   = errors.New("auth: token expired")
	ErrClaims    = errors.New("auth: invalid claims")
)

var enc = base64.RawURLEncoding.Strict()

// Claims is what a token asserts about its holder.
type Claims struct {
	Session string `json:"sid"`
	User    string `json:"uid"`
	Role    Role   `json:"role"`
	Expires int64  `json:"exp"` // Unix seconds
}

func (c Claims) validate() error {
	if c.Session == "" || c.User == "" || !c.Role.Valid() || c.Expires == 0 {
		return ErrClaims
	}
	return nil
}

// Signer issues and verifies tokens with one HMAC key.
type Signer struct {
	key []byte
	now func() time.Time
}

func NewSigner(key []byte) (*Signer, error) {
	if len(key) < MinKeyLen {
		return nil, fmt.Errorf("auth: signing key must be at least %d bytes", MinKeyLen)
	}
	return &Signer{key: bytes.Clone(key), now: time.Now}, nil
}

// Issue signs a token for user in session that expires after ttl.
func (s *Signer) Issue(session, user string, role Role, ttl time.Duration) (string, error) {
	return s.Sign(Claims{Session: session, User: user, Role: role, Expires: s.now().Add(ttl).Unix()})
}

// Sign encodes and signs c as given.
func (s *Signer) Sign(c Claims) (string, error) {
	if err := c.validate(); err != nil {
		return "", err
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	p := enc.EncodeToString(payload)
	return p + "." + enc.EncodeToString(s.mac(p)), nil
}

// Verify checks the signature before looking at the payload, then checks
// the claims and expiry.
func (s *Signer) Verify(token string) (Claims, error) {
	p, sig, ok := strings.Cut(token, ".")
	if !ok || p == "" || sig == "" {
		return Claims{}, ErrMalformed
	}
	got, err := enc.DecodeString(sig)
	if err != nil {
		return Claims{}, ErrMalformed
	}
	if !hmac.Equal(got, s.mac(p)) {
		return Claims{}, ErrSignature
	}
	payload, err := enc.DecodeString(p)
	if err != nil {
		return Claims{}, ErrMalformed
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return Claims{}, ErrMalformed
	}
	if err := c.validate(); err != nil {
		return Claims{}, err
	}
	if !s.now().Before(time.Unix(c.Expires, 0)) {
		return Claims{}, ErrExpired
	}
	return c, nil
}

func (s *Signer) mac(payload string) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(payload))
	return m.Sum(nil)
}
