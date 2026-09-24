package auth

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 1, 19, 0, 0, 0, time.UTC)

func newTestSigner(t *testing.T, key string) *Signer {
	t.Helper()
	s, err := NewSigner([]byte(key))
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return t0 }
	return s
}

const (
	keyA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	keyB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestRoundTrip(t *testing.T) {
	s := newTestSigner(t, keyA)
	tok, err := s.Issue("ses_1", "usr_kai", RolePlayer, DefaultTTL)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Verify(tok)
	if err != nil {
		t.Fatal(err)
	}
	want := Claims{Session: "ses_1", User: "usr_kai", Role: RolePlayer, Expires: t0.Add(DefaultTTL).Unix()}
	if got != want {
		t.Fatalf("claims = %+v, want %+v", got, want)
	}
}

func TestVerifyRejects(t *testing.T) {
	s := newTestSigner(t, keyA)
	valid, _ := s.Issue("ses_1", "usr_kai", RolePlayer, time.Hour)
	asDM, _ := s.Issue("ses_1", "usr_kai", RoleDM, time.Hour)
	payload, sig, _ := strings.Cut(valid, ".")
	dmPayload, _, _ := strings.Cut(asDM, ".")

	sigBytes, _ := enc.DecodeString(sig)
	sigBytes[0] ^= 0xff
	flippedSig := enc.EncodeToString(sigBytes)

	// Correctly signed, but the claims are incomplete.
	badClaims := enc.EncodeToString([]byte(`{"sid":"ses_1","role":"dm","exp":9999999999}`))
	badClaims += "." + enc.EncodeToString(s.mac(badClaims))

	tests := []struct {
		name  string
		token string
		now   time.Time
		want  error
	}{
		{"empty", "", t0, ErrMalformed},
		{"no separator", "abc", t0, ErrMalformed},
		{"only separator", ".", t0, ErrMalformed},
		{"extra part", valid + ".x", t0, ErrMalformed},
		{"not base64", "!!!.!!!", t0, ErrMalformed},
		{"payload swapped (role escalation)", dmPayload + "." + sig, t0, ErrSignature},
		{"signature changed", payload + "." + flippedSig, t0, ErrSignature},
		{"wrong key", mustIssue(t, keyB), t0, ErrSignature},
		{"expired", valid, t0.Add(2 * time.Hour), ErrExpired},
		{"at expiry", valid, t0.Add(time.Hour), ErrExpired},
		{"missing user", badClaims, t0, ErrClaims},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s.now = func() time.Time { return tt.now }
			_, err := s.Verify(tt.token)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func mustIssue(t *testing.T, key string) string {
	t.Helper()
	tok, err := newTestSigner(t, key).Issue("ses_1", "usr_kai", RolePlayer, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestSignRejectsInvalidClaims(t *testing.T) {
	s := newTestSigner(t, keyA)
	for _, c := range []Claims{
		{User: "u", Role: RoleDM, Expires: 1},
		{Session: "s", Role: RoleDM, Expires: 1},
		{Session: "s", User: "u", Role: "admin", Expires: 1},
		{Session: "s", User: "u", Role: RoleDM},
	} {
		if _, err := s.Sign(c); !errors.Is(err, ErrClaims) {
			t.Errorf("Sign(%+v) err = %v, want ErrClaims", c, err)
		}
	}
}

func TestNewSignerRejectsShortKey(t *testing.T) {
	if _, err := NewSigner([]byte("short")); err == nil {
		t.Fatal("expected error for short key")
	}
}

func FuzzVerify(f *testing.F) {
	s, _ := NewSigner([]byte(keyA))
	valid, _ := s.Issue("ses_1", "usr_kai", RoleDM, time.Hour)
	for _, seed := range []string{"", ".", "a.b", valid, valid + "x", "x" + valid} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, token string) {
		c, err := s.Verify(token)
		if err == nil && c.validate() != nil {
			t.Fatalf("Verify accepted invalid claims %+v", c)
		}
	})
}
