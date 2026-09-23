// Package auth is the site login: one account, a signed token, and no session
// store.
//
// One account because swissd manages one cluster and the thing it protects is
// already protected by Kubernetes RBAC underneath -- a user table here would be
// a second, weaker copy of an authorization decision the cluster is making
// anyway. What this adds is that the web UI is not open to anyone who can reach
// the Service.
//
// The credential is a mounted Secret rather than an API read: a file the kubelet
// keeps up to date, which means no RBAC grant on Secrets for swissd's own
// login, and a rotated password taking effect without a restart.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// File names inside the mounted Secret. One file per key is what a Secret
// volume is, so these are the Secret's keys.
const (
	FileUsername = "username"
	FilePassword = "password"
	FileTokenKey = "tokenKey"
)

// Credentials is the login, as mounted.
type Credentials struct {
	Username string
	Password string
	// TokenKey signs tokens. Kept beside the password rather than generated per
	// process, so a swissd restart does not log everybody out.
	TokenKey []byte
}

// Load reads the credentials from a mounted Secret directory.
//
// A missing tokenKey is not an error: it is derived from the password instead,
// so a Secret made by hand with two keys works. The cost is that changing the
// password then also changes the signing key, which invalidates outstanding
// tokens -- the same thing the pv claim does deliberately.
func Load(dir string) (Credentials, error) {
	user, err := readFile(dir, FileUsername)
	if err != nil {
		return Credentials{}, err
	}
	pass, err := readFile(dir, FilePassword)
	if err != nil {
		return Credentials{}, err
	}
	if user == "" || pass == "" {
		return Credentials{}, fmt.Errorf("%s: username and password must both be set", dir)
	}
	c := Credentials{Username: user, Password: pass}

	key, err := readFile(dir, FileTokenKey)
	switch {
	case err != nil && !os.IsNotExist(err):
		return Credentials{}, err
	case key != "":
		c.TokenKey = []byte(key)
	default:
		sum := sha256.Sum256([]byte("swiss-token-key\x00" + pass))
		c.TokenKey = sum[:]
	}
	return c, nil
}

// Matches reports whether a login attempt is the account, in constant time.
// Both halves are compared even when the username is already wrong: a reply
// that comes back faster for a wrong user than for a wrong password is a user
// enumeration oracle.
func (c Credentials) Matches(username, password string) bool {
	u := subtle.ConstantTimeCompare([]byte(username), []byte(c.Username))
	p := subtle.ConstantTimeCompare([]byte(password), []byte(c.Password))
	return u&p == 1
}

// Version identifies the password a token was issued against, without carrying
// it. A changed password stops verifying every token issued before it, which is
// the only revocation a stateless token has.
func (c Credentials) Version() string {
	sum := sha256.Sum256([]byte(c.Password))
	return hex.EncodeToString(sum[:8])
}

// readFile reads one key, trimming the trailing newline an editor or a `helm
// --set-file` leaves behind. A Secret written with stringData has none; one
// written from a file usually does, and a password that differs by an invisible
// byte is the worst kind of wrong.
func readFile(dir, name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

// Claims is the token body. Deliberately small: a token says who, until when,
// and against which password -- everything else is read from the cluster.
type Claims struct {
	Sub string `json:"sub"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
	// PV is the password version this was issued against.
	PV string `json:"pv"`
}

type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

// Sign returns a JWS compact token, HS256.
func Sign(key []byte, c Claims) (string, error) {
	h, err := json.Marshal(header{Alg: "HS256", Typ: "JWT"})
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	signing := enc(h) + "." + enc(body)
	return signing + "." + enc(mac(key, signing)), nil
}

// Verify checks the signature and the expiry, and returns the claims.
//
// The algorithm is checked against HS256 rather than read from the token. A
// verifier that trusts the header's alg is the "none" attack, and it is the
// single most repeated mistake in JWT handling.
func Verify(key []byte, token string, now time.Time) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, fmt.Errorf("malformed token")
	}
	signing := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Claims{}, fmt.Errorf("malformed signature")
	}
	if !hmac.Equal(sig, mac(key, signing)) {
		return Claims{}, fmt.Errorf("bad signature")
	}

	rawHeader, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Claims{}, fmt.Errorf("malformed header")
	}
	var h header
	if err := json.Unmarshal(rawHeader, &h); err != nil {
		return Claims{}, fmt.Errorf("malformed header")
	}
	if h.Alg != "HS256" {
		return Claims{}, fmt.Errorf("unexpected algorithm %q", h.Alg)
	}

	rawBody, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, fmt.Errorf("malformed claims")
	}
	var c Claims
	if err := json.Unmarshal(rawBody, &c); err != nil {
		return Claims{}, fmt.Errorf("malformed claims")
	}
	if c.Exp <= 0 || now.After(time.Unix(c.Exp, 0)) {
		return Claims{}, fmt.Errorf("token expired")
	}
	return c, nil
}

func mac(key []byte, signing string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(signing))
	return m.Sum(nil)
}

func enc(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
