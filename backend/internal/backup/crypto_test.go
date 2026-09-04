package backup

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	// Several sizes, including ones that straddle the chunk boundary.
	sizes := []int{0, 1, 1024, chunkSize - 1, chunkSize, chunkSize + 1, 3*chunkSize + 77}
	for _, size := range sizes {
		plaintext := make([]byte, size)
		if _, err := rand.Read(plaintext); err != nil {
			t.Fatal(err)
		}

		var encrypted bytes.Buffer
		if err := Encrypt(&encrypted, bytes.NewReader(plaintext), "correct-passphrase"); err != nil {
			t.Fatalf("size %d: Encrypt: %v", size, err)
		}
		// The ciphertext must not contain the plaintext.
		if size > 32 && bytes.Contains(encrypted.Bytes(), plaintext[:32]) {
			t.Errorf("size %d: plaintext found in ciphertext", size)
		}

		var decrypted bytes.Buffer
		if err := Decrypt(&decrypted, bytes.NewReader(encrypted.Bytes()), "correct-passphrase"); err != nil {
			t.Fatalf("size %d: Decrypt: %v", size, err)
		}
		if !bytes.Equal(decrypted.Bytes(), plaintext) {
			t.Errorf("size %d: round trip produced different data", size)
		}
	}
}

func TestDecryptRejectsWrongKey(t *testing.T) {
	plaintext := []byte("sensitive database contents")
	var encrypted bytes.Buffer
	if err := Encrypt(&encrypted, bytes.NewReader(plaintext), "right-key"); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := Decrypt(&out, bytes.NewReader(encrypted.Bytes()), "wrong-key")
	if !errors.Is(err, ErrDecryptFailed) {
		t.Errorf("Decrypt with the wrong key = %v, want ErrDecryptFailed", err)
	}
	if out.Len() > 0 {
		t.Error("data was written despite a failed decryption")
	}
}

func TestDecryptDetectsTampering(t *testing.T) {
	plaintext := bytes.Repeat([]byte("secret data "), 500)
	var encrypted bytes.Buffer
	if err := Encrypt(&encrypted, bytes.NewReader(plaintext), "key"); err != nil {
		t.Fatal(err)
	}

	// Flip a bit in the ciphertext body; the authentication tag must catch it.
	tampered := append([]byte{}, encrypted.Bytes()...)
	tampered[len(tampered)-10] ^= 0x01

	var out bytes.Buffer
	if err := Decrypt(&out, bytes.NewReader(tampered), "key"); !errors.Is(err, ErrDecryptFailed) {
		t.Errorf("tampered ciphertext = %v, want ErrDecryptFailed", err)
	}
}

func TestDecryptRejectsTruncatedFile(t *testing.T) {
	plaintext := bytes.Repeat([]byte("x"), 3*chunkSize)
	var encrypted bytes.Buffer
	if err := Encrypt(&encrypted, bytes.NewReader(plaintext), "key"); err != nil {
		t.Fatal(err)
	}

	// A backup cut short mid-chunk must fail rather than silently returning
	// partial data.
	truncated := encrypted.Bytes()[:len(encrypted.Bytes())-500]
	var out bytes.Buffer
	if err := Decrypt(&out, bytes.NewReader(truncated), "key"); err == nil {
		t.Error("truncated backup decrypted without error")
	}
}

func TestDecryptRejectsForeignFile(t *testing.T) {
	var out bytes.Buffer
	err := Decrypt(&out, bytes.NewReader([]byte("SQLite format 3\x00 not encrypted")), "key")
	if !errors.Is(err, ErrNotEncrypted) {
		t.Errorf("plain file = %v, want ErrNotEncrypted", err)
	}
}

func TestEncryptRequiresKey(t *testing.T) {
	var out bytes.Buffer
	if err := Encrypt(&out, bytes.NewReader([]byte("x")), ""); !errors.Is(err, ErrNoKey) {
		t.Errorf("Encrypt with no key = %v, want ErrNoKey", err)
	}
}

func TestEachBackupUsesDistinctCiphertext(t *testing.T) {
	plaintext := []byte("identical contents")
	var a, b bytes.Buffer
	if err := Encrypt(&a, bytes.NewReader(plaintext), "key"); err != nil {
		t.Fatal(err)
	}
	if err := Encrypt(&b, bytes.NewReader(plaintext), "key"); err != nil {
		t.Fatal(err)
	}
	// A fresh salt and nonce per file means identical input must not produce
	// identical output.
	if bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Error("two encryptions of the same data produced identical ciphertext")
	}
}

func TestIsEncrypted(t *testing.T) {
	var enc bytes.Buffer
	if err := Encrypt(&enc, bytes.NewReader([]byte("data")), "key"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := IsEncrypted(bytes.NewReader(enc.Bytes())); !ok {
		t.Error("IsEncrypted = false for an encrypted backup")
	}
	if ok, _ := IsEncrypted(bytes.NewReader([]byte("SQLite format 3\x00"))); ok {
		t.Error("IsEncrypted = true for a plain SQLite file")
	}
}
