package crypto

import (
	"crypto/aes"
	"testing"
)

func BenchmarkEncryptBarkCBC_128B(b *testing.B) {
	key := []byte("1234567890123456")
	iv := "1111111111111111"
	plainText := []byte(`{"title":"Verification","body":"Your verification code is 123456, valid for 5 mins.","group":"SMS","copy":"123456"}`)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := EncryptBarkCBC(plainText, key, iv)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncryptBarkCBC_1KB(b *testing.B) {
	key := []byte("1234567890123456")
	iv := "1111111111111111"
	plainText := make([]byte, 1024)
	for i := range plainText {
		plainText[i] = 'a'
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := EncryptBarkCBC(plainText, key, iv)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecryptBarkCBC(b *testing.B) {
	key := []byte("1234567890123456")
	iv := "1111111111111111"
	plainText := []byte(`{"title":"Verification","body":"Your verification code is 123456, valid for 5 mins."}`)
	cipherText, err := EncryptBarkCBC(plainText, key, iv)
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := DecryptBarkCBC(cipherText, key, iv)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGenerateBarkIV(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := GenerateBarkIV()
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPKCS7Padding(b *testing.B) {
	data := []byte(`{"title":"Alert","body":"Emergency call from 110"}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = PKCS7Padding(data, aes.BlockSize)
	}
}

func BenchmarkPKCS7Unpadding(b *testing.B) {
	data := PKCS7Padding([]byte(`{"title":"Alert","body":"Emergency call from 110"}`), aes.BlockSize)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := PKCS7Unpadding(data, aes.BlockSize)
		if err != nil {
			b.Fatal(err)
		}
	}
}
