package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mount(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A Secret written from a file carries the newline the editor left; a password
// that differs by an invisible byte is the worst kind of wrong.
func TestLoadTrimsTheTrailingNewline(t *testing.T) {
	c, err := Load(mount(t, map[string]string{
		FileUsername: "admin\n", FilePassword: "hunter2\n", FileTokenKey: "k\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Username != "admin" || c.Password != "hunter2" || string(c.TokenKey) != "k" {
		t.Fatalf("not trimmed: %+v", c)
	}
}

// Two keys is a valid Secret to make by hand, so the signing key is derived
// when it is not mounted.
func TestTokenKeyIsDerivedWhenAbsent(t *testing.T) {
	c, err := Load(mount(t, map[string]string{FileUsername: "admin", FilePassword: "hunter2"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.TokenKey) != 32 {
		t.Fatalf("derived key is %d bytes", len(c.TokenKey))
	}
	other, _ := Load(mount(t, map[string]string{FileUsername: "admin", FilePassword: "other"}))
	if string(other.TokenKey) == string(c.TokenKey) {
		t.Error("a derived key must follow the password it was derived from")
	}
}

func TestLoadNeedsBothHalves(t *testing.T) {
	if _, err := Load(mount(t, map[string]string{FileUsername: "admin"})); err == nil {
		t.Error("a mount with no password must not load")
	}
	if _, err := Load(mount(t, map[string]string{FileUsername: "admin", FilePassword: ""})); err == nil {
		t.Error("an empty password must not load")
	}
}

func TestMatches(t *testing.T) {
	c := Credentials{Username: "admin", Password: "hunter2"}
	if !c.Matches("admin", "hunter2") {
		t.Error("the account must match itself")
	}
	for _, bad := range [][2]string{{"admin", "wrong"}, {"root", "hunter2"}, {"", ""}} {
		if c.Matches(bad[0], bad[1]) {
			t.Errorf("%q/%q must not match", bad[0], bad[1])
		}
	}
}

func TestSignAndVerifyRoundTrip(t *testing.T) {
	key := []byte("a-key")
	now := time.Now()
	want := Claims{Sub: "admin", Iat: now.Unix(), Exp: now.Add(time.Hour).Unix(), PV: "abc"}

	tok, err := Sign(key, want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Verify(key, tok, now)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("round trip: %+v != %+v", got, want)
	}
}

func TestVerifyRefusesTheObviousForgeries(t *testing.T) {
	key := []byte("a-key")
	now := time.Now()
	tok, err := Sign(key, Claims{Sub: "admin", Exp: now.Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Verify([]byte("another-key"), tok, now); err == nil {
		t.Error("a token signed with another key must not verify")
	}
	if _, err := Verify(key, tok[:len(tok)-2]+"xy", now); err == nil {
		t.Error("a tampered signature must not verify")
	}
	if _, err := Verify(key, "not.a.token", now); err == nil {
		t.Error("garbage must not verify")
	}

	expired, _ := Sign(key, Claims{Sub: "admin", Exp: now.Add(-time.Second).Unix()})
	if _, err := Verify(key, expired, now); err == nil {
		t.Error("an expired token must not verify")
	}
	none, _ := Sign(key, Claims{Sub: "admin", Exp: now.Add(time.Hour).Unix()})
	if _, err := Verify(key, none, now.Add(2*time.Hour)); err == nil {
		t.Error("expiry is checked against the clock, not the issue time")
	}
}

// The header's alg is not a fact about the token, it is a claim by whoever
// wrote it. Reading it is the "none" attack.
func TestVerifyRefusesAnUnsignedToken(t *testing.T) {
	key := []byte("a-key")
	now := time.Now()
	unsigned := enc([]byte(`{"alg":"none","typ":"JWT"}`)) + "." +
		enc([]byte(`{"sub":"admin","exp":`+itoa(now.Add(time.Hour).Unix())+`}`))
	// Signed correctly for THIS key, but claiming alg none.
	tok := unsigned + "." + enc(mac(key, unsigned))
	if _, err := Verify(key, tok, now); err == nil || !strings.Contains(err.Error(), "algorithm") {
		t.Fatalf("alg must be checked against HS256, got %v", err)
	}
}

// A changed password must stop every token issued before it.
func TestVersionFollowsThePassword(t *testing.T) {
	a := Credentials{Password: "hunter2"}
	b := Credentials{Password: "hunter3"}
	if a.Version() == b.Version() {
		t.Error("two passwords must not share a version")
	}
	if a.Version() != (Credentials{Password: "hunter2"}).Version() {
		t.Error("one password must have one version")
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// The proxy key is optional: without the file nothing is trusted, with it only
// the exact key is.
func TestProxyKeyIsOptionalAndExact(t *testing.T) {
	without, err := Load(mount(t, map[string]string{FileUsername: "admin", FilePassword: "hunter2"}))
	if err != nil {
		t.Fatal(err)
	}
	if without.IsProxy("") || without.IsProxy("anything") {
		t.Fatal("no proxy key mounted, yet a bearer was taken for the proxy")
	}

	with, err := Load(mount(t, map[string]string{FileUsername: "admin", FilePassword: "hunter2", FileProxyKey: "pk\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if !with.IsProxy("pk") || with.IsProxy("pk2") || with.IsProxy("") {
		t.Fatalf("proxy key %q not matched exactly", with.ProxyKey)
	}
}
