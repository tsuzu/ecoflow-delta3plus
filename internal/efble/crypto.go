package efble

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	_ "embed"
	"errors"
)

// type7Encryption is eflib's Type7Encryption: AES-CBC with PKCS#7
// padding. Decryption mirrors the reference exactly, including its
// quirk: only full blocks are decrypted, and if unpadding fails the
// still-padded plaintext is returned.
type type7Encryption struct {
	key []byte // 16 bytes
	iv  []byte // 16 bytes
}

func (e *type7Encryption) encrypt(plaintext []byte) []byte {
	block, _ := aes.NewCipher(e.key)
	padded := pkcs7Pad(plaintext, aes.BlockSize)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, e.iv).CryptBlocks(out, padded)
	return out
}

func (e *type7Encryption) decrypt(ciphertext []byte) []byte {
	aligned := len(ciphertext) - len(ciphertext)%aes.BlockSize
	if aligned == 0 {
		return ciphertext
	}
	block, _ := aes.NewCipher(e.key)
	out := make([]byte, aligned)
	cipher.NewCBCDecrypter(block, e.iv).CryptBlocks(out, ciphertext[:aligned])
	if unpadded, err := pkcs7Unpad(out, aes.BlockSize); err == nil {
		return unpadded
	}
	return out
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	n := blockSize - len(data)%blockSize
	out := make([]byte, len(data)+n)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(n)
	}
	return out
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, errors.New("invalid padded length")
	}
	n := int(data[len(data)-1])
	if n == 0 || n > blockSize || n > len(data) {
		return nil, errors.New("invalid padding")
	}
	for _, b := range data[len(data)-n:] {
		if int(b) != n {
			return nil, errors.New("invalid padding bytes")
		}
	}
	return data[:len(data)-n], nil
}

// keydataBlob is the session-key derivation table from eflib's
// keydata.py (base64-decoded; Apache-2.0, ha-ef-ble).
//
//go:embed keydata.bin
var keydataBlob []byte

// genSessionKey mirrors Connection._gen_session_key: two uint64s are
// read from the keydata table at an offset derived from the seed,
// two from the device-supplied sRand, and the session key is the MD5
// of the four little-endian uint64s concatenated.
func genSessionKey(seed, srand []byte) []byte {
	pos := int(seed[0])*0x10 + int((seed[1]-1)&0xFF)*0x100
	var data bytes.Buffer
	data.Write(u64leAt(keydataBlob, pos))
	data.Write(u64leAt(keydataBlob, pos+8))
	data.Write(srand[0:8])
	data.Write(srand[8:16])
	sum := md5.Sum(data.Bytes())
	return sum[:]
}

func u64leAt(b []byte, pos int) []byte {
	out := make([]byte, 8)
	if pos+8 <= len(b) {
		copy(out, b[pos:pos+8])
	}
	return out
}

// authToken builds the authentication payload: uppercase hex of
// md5(user_id + serial), matching _auto_authentication for the
// DELTA 3 Plus (auth_token_lowercase is false for this device).
func authToken(userID, serial string) []byte {
	sum := md5.Sum([]byte(userID + serial))
	const hexd = "0123456789ABCDEF"
	out := make([]byte, 0, 32)
	for _, b := range sum {
		out = append(out, hexd[b>>4], hexd[b&0x0F])
	}
	return out
}
