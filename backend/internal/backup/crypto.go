package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// Backups are encrypted before they leave the server, so that a remote provider
// holding the file cannot read the database.
//
// The format is streamed in fixed-size chunks rather than encrypting the whole
// file in memory, because a database backup can be far larger than available
// RAM. Each chunk is sealed independently with AES-256-GCM under a per-chunk
// nonce derived from a random file nonce plus the chunk index, which gives
// every chunk a unique nonce without storing one per chunk.
//
// Layout:
//
//	magic    8 bytes  "LBENC001"
//	salt    16 bytes  HKDF salt
//	nonce   12 bytes  base nonce; the low 4 bytes are XORed with the chunk index
//	chunks            repeated: uint32 length, then the sealed chunk
//
// The chunk index is part of each nonce, so chunks cannot be reordered or
// dropped without the authentication tag failing.

const (
	magic          = "LBENC001"
	saltLen        = 16
	nonceLen       = 12
	chunkSize      = 1 << 20 // 1 MiB of plaintext per chunk
	maxChunkOnDisk = chunkSize + 1024
)

var (
	// ErrNotEncrypted means the file does not carry the Litebase header.
	ErrNotEncrypted = errors.New("file is not a Litebase encrypted backup")
	// ErrDecryptFailed means the key is wrong or the file was tampered with.
	ErrDecryptFailed = errors.New("backup could not be decrypted; the key may be wrong or the file damaged")
	// ErrNoKey means encryption was requested without a configured key.
	ErrNoKey = errors.New("no backup encryption key is configured")
)

// deriveKey stretches the configured passphrase into an AES-256 key.
//
// HKDF is appropriate here because the input is a high-entropy configured
// secret rather than a human-chosen password; it binds the key to a random
// per-file salt so two backups never share a key stream.
func deriveKey(passphrase string, salt []byte) ([]byte, error) {
	if passphrase == "" {
		return nil, ErrNoKey
	}
	key := make([]byte, 32)
	kdf := hkdf.New(sha256.New, []byte(passphrase), salt, []byte("litebase-backup-v1"))
	if _, err := io.ReadFull(kdf, key); err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}
	return key, nil
}

// chunkNonce derives a unique nonce for a chunk from the file's base nonce.
func chunkNonce(base []byte, index uint64) []byte {
	nonce := make([]byte, nonceLen)
	copy(nonce, base)
	// XOR the counter into the trailing bytes so each chunk differs.
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], index)
	for i := 0; i < 8; i++ {
		nonce[nonceLen-8+i] ^= counter[i]
	}
	return nonce
}

// Encrypt streams plaintext from r to w in the Litebase backup format.
func Encrypt(w io.Writer, r io.Reader, passphrase string) error {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	baseNonce := make([]byte, nonceLen)
	if _, err := rand.Read(baseNonce); err != nil {
		return err
	}

	key, err := deriveKey(passphrase, salt)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}

	if _, err := w.Write([]byte(magic)); err != nil {
		return err
	}
	if _, err := w.Write(salt); err != nil {
		return err
	}
	if _, err := w.Write(baseNonce); err != nil {
		return err
	}

	buf := make([]byte, chunkSize)
	sealed := make([]byte, 0, chunkSize+aead.Overhead())
	var index uint64

	for {
		n, readErr := io.ReadFull(r, buf)
		if n > 0 {
			// The chunk index is authenticated as additional data, so a
			// reordered or duplicated chunk fails to open.
			var aad [8]byte
			binary.BigEndian.PutUint64(aad[:], index)

			sealed = aead.Seal(sealed[:0], chunkNonce(baseNonce, index), buf[:n], aad[:])
			var length [4]byte
			binary.BigEndian.PutUint32(length[:], uint32(len(sealed)))
			if _, err := w.Write(length[:]); err != nil {
				return err
			}
			if _, err := w.Write(sealed); err != nil {
				return err
			}
			index++
		}
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

// Decrypt streams a Litebase encrypted backup from r to w.
func Decrypt(w io.Writer, r io.Reader, passphrase string) error {
	header := make([]byte, len(magic)+saltLen+nonceLen)
	if _, err := io.ReadFull(r, header); err != nil {
		return ErrNotEncrypted
	}
	if string(header[:len(magic)]) != magic {
		return ErrNotEncrypted
	}
	salt := header[len(magic) : len(magic)+saltLen]
	baseNonce := header[len(magic)+saltLen:]

	key, err := deriveKey(passphrase, salt)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}

	var (
		lengthBuf [4]byte
		index     uint64
		sealed    = make([]byte, 0, maxChunkOnDisk)
		plain     = make([]byte, 0, chunkSize)
	)
	for {
		if _, err := io.ReadFull(r, lengthBuf[:]); err != nil {
			if err == io.EOF {
				return nil
			}
			return ErrDecryptFailed
		}
		length := binary.BigEndian.Uint32(lengthBuf[:])
		// Bound the allocation so a corrupt or hostile length cannot exhaust
		// memory.
		if length == 0 || int(length) > maxChunkOnDisk+aead.Overhead() {
			return ErrDecryptFailed
		}

		if cap(sealed) < int(length) {
			sealed = make([]byte, length)
		}
		sealed = sealed[:length]
		if _, err := io.ReadFull(r, sealed); err != nil {
			return ErrDecryptFailed
		}

		var aad [8]byte
		binary.BigEndian.PutUint64(aad[:], index)

		plain, err = aead.Open(plain[:0], chunkNonce(baseNonce, index), sealed, aad[:])
		if err != nil {
			return ErrDecryptFailed
		}
		if _, err := w.Write(plain); err != nil {
			return err
		}
		index++
	}
}

// IsEncrypted reports whether a file begins with the Litebase backup header.
func IsEncrypted(r io.Reader) (bool, error) {
	buf := make([]byte, len(magic))
	n, err := io.ReadFull(r, buf)
	if err != nil && n < len(magic) {
		return false, nil
	}
	return string(buf) == magic, nil
}
