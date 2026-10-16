// Package crypto provides cryptographic utilities for push-transfer, including Bark AES payload encryption.
package crypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"unicode/utf8"
)

var (
	// ErrInvalidKeyLength indicates that the provided key does not meet AES-128 requirements.
	ErrInvalidKeyLength = errors.New("key must be exactly 16 bytes for AES-128")
	// ErrInvalidIVLength indicates that the provided IV is not a 16-character ASCII string.
	ErrInvalidIVLength = errors.New("iv must be exactly 16 ASCII characters for AES-128-CBC")
	// ErrInvalidCiphertext indicates that the ciphertext is malformed or too short.
	ErrInvalidCiphertext = errors.New("ciphertext is too short or not block-aligned")
	// ErrInvalidPadding indicates corrupted or invalid PKCS#7 padding.
	ErrInvalidPadding = errors.New("invalid PKCS#7 padding")
)

// barkIVAlphabet is used to build a 16-character IV whose raw UTF-8 bytes are the CBC IV.
// Bark transmits the IV as a JSON string and uses its bytes verbatim, so the value must
// consist of single-byte characters that survive JSON transport unchanged.
const barkIVAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// BarkPayload is the wire envelope of an encrypted Bark push.
//
// Bark expects the ciphertext and the initialization vector as two independent
// top-level parameters; it never re-derives the IV from the ciphertext prefix.
// See https://bark.day.app/en-us/params.md (ciphertext, iv).
type BarkPayload struct {
	Ciphertext string `json:"ciphertext"`
	IV         string `json:"iv"`
}

// PKCS7Padding appends PKCS#7 padding to data according to blockSize.
func PKCS7Padding(data []byte, blockSize int) []byte {
	if blockSize <= 0 || blockSize > 255 {
		blockSize = aes.BlockSize
	}
	padLen := blockSize - (len(data) % blockSize)
	padding := bytes.Repeat([]byte{byte(padLen)}, padLen)
	return append(data, padding...)
}

// PKCS7Unpadding removes and validates PKCS#7 padding from data according to blockSize.
// Uses constant-time operations across padding bytes to protect against padding oracle attacks.
func PKCS7Unpadding(data []byte, blockSize int) ([]byte, error) {
	if blockSize <= 0 || blockSize > 255 {
		blockSize = aes.BlockSize
	}
	length := len(data)
	if length == 0 || length%blockSize != 0 {
		return nil, ErrInvalidPadding
	}

	padLen := int(data[length-1])
	good := subtle.ConstantTimeLessOrEq(1, padLen) & subtle.ConstantTimeLessOrEq(padLen, blockSize)

	var diff byte
	for i := 0; i < blockSize; i++ {
		b := data[length-1-i]
		inPadding := subtle.ConstantTimeLessOrEq(i+1, padLen)
		mask := byte(subtle.ConstantTimeSelect(inPadding, 0xff, 0x00))
		diff |= mask & (b ^ byte(padLen))
	}

	if good != 1 || subtle.ConstantTimeByteEq(diff, 0) != 1 {
		return nil, ErrInvalidPadding
	}

	return data[:length-padLen], nil
}

// GenerateBarkIV returns a fresh 16-character ASCII initialization vector.
//
// The returned string is meant to be sent as Bark's `iv` parameter: the iOS client
// feeds the raw UTF-8 bytes of that string to AES-128-CBC, so it must be exactly 16
// single-byte characters. Base64 or hex of 16 random bytes would be 24/32 characters
// long and therefore unusable by Bark.
func GenerateBarkIV() (string, error) {
	buf := make([]byte, aes.BlockSize)
	alphabetSize := big.NewInt(int64(len(barkIVAlphabet)))
	for i := range buf {
		n, err := rand.Int(rand.Reader, alphabetSize)
		if err != nil {
			return "", fmt.Errorf("failed to generate random IV: %w", err)
		}
		buf[i] = barkIVAlphabet[n.Int64()]
	}
	return string(buf), nil
}

func validateBarkKey(key []byte) error {
	if len(key) != 16 {
		return fmt.Errorf("%w: got %d bytes", ErrInvalidKeyLength, len(key))
	}
	return nil
}

// validateBarkIV rejects IVs that Bark cannot use: the value is transported as a JSON
// string and its raw bytes become the CBC IV, so 16 bytes and 16 runes must coincide.
func validateBarkIV(iv string) error {
	if len(iv) != aes.BlockSize || utf8.RuneCountInString(iv) != aes.BlockSize {
		return fmt.Errorf("%w: got %d bytes / %d characters", ErrInvalidIVLength, len(iv), utf8.RuneCountInString(iv))
	}
	return nil
}

// EncryptBarkCBC encrypts plainText with AES-128-CBC and PKCS#7 padding using an
// explicit 16-character IV, returning the standard Base64 ciphertext.
//
// It exists so that deterministic, interoperable vectors can be produced and verified
// against Bark's own documented example; prefer EncryptBark for real pushes.
func EncryptBarkCBC(plainText, key []byte, iv string) (string, error) {
	if err := validateBarkKey(key); err != nil {
		return "", err
	}
	if err := validateBarkIV(iv); err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	padded := PKCS7Padding(plainText, aes.BlockSize)
	cipherText := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, []byte(iv)).CryptBlocks(cipherText, padded)

	return base64.StdEncoding.EncodeToString(cipherText), nil
}

// EncryptBark encrypts plainText with AES-128-CBC/PKCS#7 using a per-message random IV
// and returns the Bark envelope holding both the ciphertext and that IV.
func EncryptBark(plainText, key []byte) (BarkPayload, error) {
	iv, err := GenerateBarkIV()
	if err != nil {
		return BarkPayload{}, err
	}
	cipherText, err := EncryptBarkCBC(plainText, key, iv)
	if err != nil {
		return BarkPayload{}, err
	}
	return BarkPayload{Ciphertext: cipherText, IV: iv}, nil
}

// DecryptBarkCBC decrypts a Base64 Bark ciphertext with an explicit 16-character IV.
func DecryptBarkCBC(cipherB64 string, key []byte, iv string) ([]byte, error) {
	if err := validateBarkKey(key); err != nil {
		return nil, err
	}
	if err := validateBarkIV(iv); err != nil {
		return nil, err
	}

	raw, err := base64.StdEncoding.DecodeString(cipherB64)
	if err != nil {
		return nil, fmt.Errorf("base64 decode error: %w", err)
	}
	if len(raw) == 0 || len(raw)%aes.BlockSize != 0 {
		return nil, ErrInvalidCiphertext
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	plainPadded := make([]byte, len(raw))
	cipher.NewCBCDecrypter(block, []byte(iv)).CryptBlocks(plainPadded, raw)

	return PKCS7Unpadding(plainPadded, aes.BlockSize)
}

// DecryptBark decrypts a Bark envelope produced by EncryptBark (or by any Bark client
// that follows the ciphertext/iv parameter contract).
func DecryptBark(payload BarkPayload, key []byte) ([]byte, error) {
	return DecryptBarkCBC(payload.Ciphertext, key, payload.IV)
}
