package secrets

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

func testKey(t *testing.T) string {
	t.Helper()
	raw := make([]byte, KeyLen)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(raw)
}

func TestSealOpenRoundTrip(t *testing.T) {
	box, err := NewBox(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	const token = "Bearer eyJhbGciOi.deadbeef"
	sealed, err := box.Seal(token)
	if err != nil {
		t.Fatal(err)
	}
	if !IsSealed(sealed) {
		t.Fatalf("value is not sealed: %q", sealed)
	}
	if strings.Contains(sealed, "deadbeef") {
		t.Fatalf("plaintext leaked into the sealed value: %q", sealed)
	}
	opened, err := box.Open(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if opened != token {
		t.Fatalf("round trip = %q, want %q", opened, token)
	}
}

func TestFreshNonceEachSeal(t *testing.T) {
	box, _ := NewBox(testKey(t))
	a, _ := box.Seal("same")
	b, _ := box.Seal("same")
	if a == b {
		t.Fatal("two seals of the same value produced identical output; the nonce is reused")
	}
	for _, s := range []string{a, b} {
		if got, err := box.Open(s); err != nil || got != "same" {
			t.Fatalf("open(%q) = %q, %v", s, got, err)
		}
	}
}

func TestEmptyStaysEmpty(t *testing.T) {
	box, _ := NewBox(testKey(t))
	sealed, err := box.Seal("")
	if err != nil || sealed != "" {
		t.Fatalf("Seal(\"\") = %q, %v", sealed, err)
	}
	opened, err := box.Open("")
	if err != nil || opened != "" {
		t.Fatalf("Open(\"\") = %q, %v", opened, err)
	}
}

func TestSealFailsClosedWithoutKey(t *testing.T) {
	var box *Box // unset deployment key
	if _, err := box.Seal("secret"); err == nil {
		t.Fatal("sealing without a key must fail rather than store plaintext")
	} else if !strings.Contains(err.Error(), "GAMEDEPOT_CREDENTIAL_KEY") {
		t.Fatalf("error should name the missing variable, got %v", err)
	}
}

func TestOpenFailsWithoutKey(t *testing.T) {
	box, _ := NewBox(testKey(t))
	sealed, _ := box.Seal("secret")
	var noKey *Box
	if _, err := noKey.Open(sealed); err == nil {
		t.Fatal("opening sealed data without the key must fail")
	}
}

func TestUnsealedValueIsRefused(t *testing.T) {
	box, _ := NewBox(testKey(t))
	for _, v := range []string{"old-plaintext-token", "enc:other:thing"} {
		if _, err := box.Open(v); err == nil {
			t.Fatalf("Open(%q) accepted an unsealed value", v)
		}
	}
}

func TestWrongKeyGivesClearError(t *testing.T) {
	box, _ := NewBox(testKey(t))
	sealed, _ := box.Seal("secret")
	other, _ := NewBox(testKey(t))
	_, err := other.Open(sealed)
	if err == nil {
		t.Fatal("a different key must not decrypt")
	}
	if !strings.Contains(err.Error(), "key changed") {
		t.Fatalf("want a key-rotation message, got %v", err)
	}
}

func TestTamperedCiphertextRejected(t *testing.T) {
	box, _ := NewBox(testKey(t))
	sealed, _ := box.Seal("secret")
	parts := strings.SplitN(strings.TrimPrefix(sealed, sealPrefix), ":", 3)
	ct, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	ct[0] ^= 0xff
	parts[2] = base64.StdEncoding.EncodeToString(ct)
	tampered := sealPrefix + strings.Join(parts, ":")
	if _, err := box.Open(tampered); err == nil {
		t.Fatal("GCM must reject a modified ciphertext")
	}
}

func TestKeyParsing(t *testing.T) {
	t.Run("hex any case", func(t *testing.T) {
		raw := testKey(t)
		if _, err := NewBox(strings.ToUpper(raw)); err != nil {
			t.Fatalf("uppercase hex rejected: %v", err)
		}
		if _, err := NewBox("  " + raw + " "); err != nil {
			t.Fatalf("surrounding space rejected: %v", err)
		}
	})
	t.Run("base64", func(t *testing.T) {
		raw := make([]byte, KeyLen)
		_, _ = rand.Read(raw)
		if _, err := NewBox(base64.StdEncoding.EncodeToString(raw)); err != nil {
			t.Fatalf("standard base64 rejected: %v", err)
		}
	})
	t.Run("rejects wrong sizes and alphabets", func(t *testing.T) {
		bad := []string{
			"",
			"short",
			hex.EncodeToString(make([]byte, 16)), // 32 hex chars, 16 bytes
			base64.StdEncoding.EncodeToString(make([]byte, 24)),        // 24 bytes in base64
			base64.RawStdEncoding.EncodeToString(make([]byte, KeyLen)), // unpadded
		}
		for _, v := range bad {
			if _, err := NewBox(v); err == nil {
				t.Fatalf("NewBox(%q) accepted an unusable key", v)
			}
		}
	})
}

func TestEnabled(t *testing.T) {
	var nilBox *Box
	if nilBox.Enabled() {
		t.Fatal("nil box must not report enabled")
	}
	box, _ := NewBox(testKey(t))
	if !box.Enabled() {
		t.Fatal("configured box must report enabled")
	}
}
