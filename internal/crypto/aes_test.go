package crypto

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// barkOfficialVector is the canonical example published by Bark at
// https://bark.day.app/en-us/encryption.md (AES-128-CBC, PKCS#7, standard Base64,
// key/iv used as their raw ASCII bytes). It pins push-transfer to Bark's real
// on-the-wire format instead of an invented one.
const (
	barkOfficialKey        = "1234567890123456"
	barkOfficialIV         = "1111111111111111"
	barkOfficialPlainText  = `{"body": "test", "sound": "birdsong"}`
	barkOfficialCipherText = "d3QhjQjP5majvNt5CjsvFWwqqj2gKl96RFj5OO+u6ynTt7lkyigDYNA3abnnCLpr"
)

func TestEncryptBark_BarkOfficialVector(t *testing.T) {
	cipherText, err := EncryptBarkCBC([]byte(barkOfficialPlainText), []byte(barkOfficialKey), barkOfficialIV)
	if err != nil {
		t.Fatalf("EncryptBarkCBC failed: %v", err)
	}
	if cipherText != barkOfficialCipherText {
		t.Fatalf("Bark interop mismatch:\n got %s\nwant %s", cipherText, barkOfficialCipherText)
	}

	plainText, err := DecryptBarkCBC(barkOfficialCipherText, []byte(barkOfficialKey), barkOfficialIV)
	if err != nil {
		t.Fatalf("DecryptBarkCBC failed on Bark's own vector: %v", err)
	}
	if string(plainText) != barkOfficialPlainText {
		t.Fatalf("decrypted %q, want %q", string(plainText), barkOfficialPlainText)
	}
}

func TestGenerateBarkIV(t *testing.T) {
	seen := make(map[string]struct{})
	for i := 0; i < 64; i++ {
		iv, err := GenerateBarkIV()
		if err != nil {
			t.Fatalf("GenerateBarkIV failed: %v", err)
		}
		// Bark feeds the raw UTF-8 bytes of the `iv` string to AES-128-CBC, so the
		// value must be 16 single-byte characters and survive a JSON round trip.
		if len(iv) != 16 || utf8.RuneCountInString(iv) != 16 {
			t.Fatalf("expected 16 single-byte characters, got %d bytes / %d runes (%q)",
				len(iv), utf8.RuneCountInString(iv), iv)
		}
		encoded, err := json.Marshal(iv)
		if err != nil {
			t.Fatalf("iv is not JSON-encodable: %v", err)
		}
		var decoded string
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("iv is not JSON-decodable: %v", err)
		}
		if decoded != iv {
			t.Fatalf("iv changed during JSON round trip: %q -> %q", iv, decoded)
		}
		if _, err := EncryptBarkCBC([]byte("payload"), []byte(barkOfficialKey), iv); err != nil {
			t.Fatalf("generated IV rejected by the cipher: %v", err)
		}
		seen[iv] = struct{}{}
	}
	if len(seen) < 60 {
		t.Fatalf("expected distinct IVs, got %d unique values out of 64", len(seen))
	}
}

func TestBarkCryptoDynamicIV(t *testing.T) {
	key := []byte(barkOfficialKey)
	plainText := []byte("Hello, this is a secret push message!")

	first, err := EncryptBark(plainText, key)
	if err != nil {
		t.Fatalf("first EncryptBark failed: %v", err)
	}
	second, err := EncryptBark(plainText, key)
	if err != nil {
		t.Fatalf("second EncryptBark failed: %v", err)
	}

	if first.Ciphertext == second.Ciphertext {
		t.Fatalf("expected different ciphertexts for identical plaintext, got identical: %s", first.Ciphertext)
	}
	if first.IV == second.IV {
		t.Fatalf("expected different IVs, got identical: %s", first.IV)
	}

	// The ciphertext must be pure AES-CBC output: the IV travels in its own field and
	// must never be prepended, otherwise Bark cannot decrypt the push.
	raw, err := base64.StdEncoding.DecodeString(first.Ciphertext)
	if err != nil {
		t.Fatalf("decode ciphertext failed: %v", err)
	}
	if len(raw)%16 != 0 {
		t.Fatalf("ciphertext is not block aligned: %d bytes", len(raw))
	}
	wantLen := (len(plainText)/16 + 1) * 16
	if len(raw) != wantLen {
		t.Fatalf("ciphertext carries extra bytes (IV prefix?): got %d bytes, want %d", len(raw), wantLen)
	}

	// The payload must serialise to exactly the two Bark parameters.
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal payload failed: %v", err)
	}
	var fields map[string]string
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("unmarshal payload failed: %v", err)
	}
	if len(fields) != 2 || fields["ciphertext"] == "" || fields["iv"] == "" {
		t.Fatalf("expected exactly {ciphertext, iv} in the Bark envelope, got %s", encoded)
	}
}

func TestEncryptDecryptBark(t *testing.T) {
	key := []byte("abcdef0123456789")
	testCases := []string{
		"",
		"a",
		"1234567890123456",  // exactly 16 bytes
		"12345678901234567", // 17 bytes
		"验证码是 952701，请妥善保管！",
		"Long message with special characters: !@#$%^&*()_+-={}|[]\\:\";'<>?,./~`",
	}

	for _, tc := range testCases {
		payload, err := EncryptBark([]byte(tc), key)
		if err != nil {
			t.Fatalf("EncryptBark failed for %q: %v", tc, err)
		}

		decrypted, err := DecryptBark(payload, key)
		if err != nil {
			t.Fatalf("DecryptBark failed for %q: %v", tc, err)
		}
		if string(decrypted) != tc {
			t.Fatalf("expected %q, got %q", tc, string(decrypted))
		}

		// Decrypting with the explicit-vector entry point must agree.
		decryptedExplicit, err := DecryptBarkCBC(payload.Ciphertext, key, payload.IV)
		if err != nil {
			t.Fatalf("DecryptBarkCBC failed for %q: %v", tc, err)
		}
		if !bytes.Equal(decryptedExplicit, decrypted) {
			t.Fatalf("DecryptBarkCBC and DecryptBark disagree for %q", tc)
		}
	}
}

func TestEncryptBark_RejectsUnusableIV(t *testing.T) {
	key := []byte(barkOfficialKey)
	// A 16-byte IV that is *not* 16 characters cannot be transported as a Bark `iv`
	// string: its bytes would not come back identical after JSON encoding.
	invalidIVs := []string{
		"",
		"short",
		strings.Repeat("x", 15),
		strings.Repeat("x", 17),
		"密钥密钥密钥密钥",                 // 24 bytes / 8 characters
		"0123456789abcdef01234567", // base64 of 16 random bytes is 24 characters
	}
	for _, iv := range invalidIVs {
		if _, err := EncryptBarkCBC([]byte("test"), key, iv); !errors.Is(err, ErrInvalidIVLength) {
			t.Errorf("EncryptBarkCBC(iv=%q): expected ErrInvalidIVLength, got %v", iv, err)
		}
		if _, err := DecryptBarkCBC("AAAA", key, iv); !errors.Is(err, ErrInvalidIVLength) {
			t.Errorf("DecryptBarkCBC(iv=%q): expected ErrInvalidIVLength, got %v", iv, err)
		}
	}
}

func TestPKCS7Padding(t *testing.T) {
	blockSize := 16

	// Test 0 bytes
	p0 := PKCS7Padding([]byte{}, blockSize)
	if len(p0) != blockSize {
		t.Fatalf("expected length %d, got %d", blockSize, len(p0))
	}
	for _, b := range p0 {
		if b != byte(blockSize) {
			t.Fatalf("expected padding byte %d, got %d", blockSize, b)
		}
	}

	// Test 15 bytes -> pad 1 byte of 0x01
	p15 := PKCS7Padding(bytes.Repeat([]byte{0xaa}, 15), blockSize)
	if len(p15) != 16 || p15[15] != 1 {
		t.Fatalf("expected length 16 with last byte 1, got len=%d last=%d", len(p15), p15[len(p15)-1])
	}

	// Test 16 bytes -> pad 16 bytes of 0x10
	p16 := PKCS7Padding(bytes.Repeat([]byte{0xbb}, 16), blockSize)
	if len(p16) != 32 {
		t.Fatalf("expected length 32, got %d", len(p16))
	}
	for i := 16; i < 32; i++ {
		if p16[i] != 16 {
			t.Fatalf("expected padding byte 16 at index %d, got %d", i, p16[i])
		}
	}

	// Test unpadding round trip
	data := []byte("hello world")
	padded := PKCS7Padding(data, blockSize)
	unpadded, err := PKCS7Unpadding(padded, blockSize)
	if err != nil {
		t.Fatalf("unpadding failed: %v", err)
	}
	if !bytes.Equal(unpadded, data) {
		t.Fatalf("unpadding mismatch: got %q, want %q", unpadded, data)
	}

	// Test invalid padding
	invalidPaddings := [][]byte{
		{},
		{1, 2, 3},        // not multiple of block size
		make([]byte, 16), // last byte is 0, invalid pad
		append(bytes.Repeat([]byte{0xaa}, 15), 2), // pad byte is 2, but only 1 byte
	}
	for _, invalid := range invalidPaddings {
		if _, err := PKCS7Unpadding(invalid, blockSize); err == nil {
			t.Fatalf("expected error for invalid padding %x, got nil", invalid)
		}
	}
}

func TestPKCS7Unpadding_CorruptedPaddingPositions(t *testing.T) {
	blockSize := 16

	// Test corrupted padding across every possible pad length (1 to 16)
	// and corrupt every position in the padding chunk.
	for padLen := 1; padLen <= blockSize; padLen++ {
		dataLen := 32 - padLen // 2 full blocks (32 bytes total)
		original := make([]byte, dataLen)
		for i := range original {
			original[i] = byte(i + 1)
		}

		padded := PKCS7Padding(original, blockSize)
		if len(padded) != 32 {
			t.Fatalf("expected padded length 32, got %d", len(padded))
		}

		// Verify unpadding works on uncorrupted data
		recovered, err := PKCS7Unpadding(padded, blockSize)
		if err != nil {
			t.Fatalf("padLen %d: unpadding failed on valid data: %v", padLen, err)
		}
		if !bytes.Equal(recovered, original) {
			t.Fatalf("padLen %d: recovered mismatch", padLen)
		}

		// Corrupt each byte within the padding region: [32-padLen, 31]
		for corruptIdx := 32 - padLen; corruptIdx < 32; corruptIdx++ {
			corrupted := make([]byte, len(padded))
			copy(corrupted, padded)

			// Flip bits in the padding byte
			corrupted[corruptIdx] ^= 0x55
			// Ensure it didn't accidentally stay equal to byte(padLen)
			if corrupted[corruptIdx] == byte(padLen) {
				corrupted[corruptIdx] ^= 0xaa
			}

			_, err := PKCS7Unpadding(corrupted, blockSize)
			if err == nil {
				t.Fatalf("padLen %d, corruptIdx %d: expected ErrInvalidPadding, got nil", padLen, corruptIdx)
			}
			if !errors.Is(err, ErrInvalidPadding) {
				t.Fatalf("padLen %d, corruptIdx %d: expected ErrInvalidPadding, got %v", padLen, corruptIdx, err)
			}
		}
	}

	// Test out-of-range pad lengths
	t.Run("pad length zero or greater than blockSize", func(t *testing.T) {
		buf := make([]byte, 16)
		if _, err := PKCS7Unpadding(buf, blockSize); !errors.Is(err, ErrInvalidPadding) {
			t.Errorf("expected ErrInvalidPadding for padLen 0, got %v", err)
		}

		buf = bytes.Repeat([]byte{0x11}, 16) // pad length 17 in 16-byte block
		if _, err := PKCS7Unpadding(buf, blockSize); !errors.Is(err, ErrInvalidPadding) {
			t.Errorf("expected ErrInvalidPadding for padLen 17, got %v", err)
		}

		buf = bytes.Repeat([]byte{0xff}, 16) // pad length 255
		if _, err := PKCS7Unpadding(buf, blockSize); !errors.Is(err, ErrInvalidPadding) {
			t.Errorf("expected ErrInvalidPadding for padLen 255, got %v", err)
		}
	})
}

func TestInvalidKeyLength(t *testing.T) {
	invalidKeys := [][]byte{
		nil,
		{},
		[]byte("short"),
		[]byte("123456789012345"),   // 15 bytes
		[]byte("12345678901234567"), // 17 bytes
		[]byte("12345678901234567890123456789012"), // 32 bytes (must be exactly 16 bytes for AES-128)
	}

	for _, key := range invalidKeys {
		if _, err := EncryptBark([]byte("test"), key); !errors.Is(err, ErrInvalidKeyLength) {
			t.Fatalf("EncryptBark: expected ErrInvalidKeyLength for key length %d, got %v", len(key), err)
		}
		if _, err := EncryptBarkCBC([]byte("test"), key, barkOfficialIV); !errors.Is(err, ErrInvalidKeyLength) {
			t.Fatalf("EncryptBarkCBC: expected ErrInvalidKeyLength for key length %d, got %v", len(key), err)
		}
		if _, err := DecryptBarkCBC(barkOfficialCipherText, key, barkOfficialIV); !errors.Is(err, ErrInvalidKeyLength) {
			t.Fatalf("DecryptBarkCBC: expected ErrInvalidKeyLength for key length %d, got %v", len(key), err)
		}
	}
}

func TestDecryptBark_InvalidCiphertext(t *testing.T) {
	key := []byte(barkOfficialKey)
	invalidCiphertexts := []string{
		"",
		"not base64 !!",
		base64.StdEncoding.EncodeToString([]byte("short")),  // not block aligned
		base64.StdEncoding.EncodeToString(make([]byte, 16)), // valid length, invalid padding
	}
	for _, cipherText := range invalidCiphertexts {
		if _, err := DecryptBark(BarkPayload{Ciphertext: cipherText, IV: barkOfficialIV}, key); err == nil {
			t.Errorf("expected error for ciphertext %q, got nil", cipherText)
		}
	}
}
