package gateway

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/scrypt"
)

const backupMagic = "AIGWBK01"
const backupChunkSize = 1 << 20

// Version 1: fixed scrypt parameters, a random salt and XChaCha20-Poly1305
// records. A required authenticated empty record terminates the stream, so
// truncation, record reordering and appended bytes are all rejected.
func encryptBackup(dst io.Writer, src io.Reader, password string) error {
	header := make([]byte, 40)
	copy(header, backupMagic)
	if _, err := rand.Read(header[8:]); err != nil {
		return err
	}
	key, err := scrypt.Key([]byte(password), header[8:24], 32768, 8, 1, 32)
	if err != nil {
		return err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return err
	}
	if _, err = dst.Write(header); err != nil {
		return err
	}
	buffer := make([]byte, backupChunkSize)
	nonce := make([]byte, 24)
	copy(nonce, header[24:40])
	var record uint64
	for {
		n, readErr := io.ReadFull(src, buffer)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return readErr
		}
		binary.BigEndian.PutUint64(nonce[16:], record)
		ciphertext := aead.Seal(nil, nonce, buffer[:n], header)
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(ciphertext)))
		if _, err = dst.Write(length[:]); err != nil {
			return err
		}
		if _, err = dst.Write(ciphertext); err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		record++
	}
}

func decryptBackup(dst io.Writer, src io.Reader, password string) error {
	header := make([]byte, 40)
	if _, err := io.ReadFull(src, header); err != nil || string(header[:8]) != backupMagic {
		return errors.New("invalid backup file")
	}
	key, err := scrypt.Key([]byte(password), header[8:24], 32768, 8, 1, 32)
	if err != nil {
		return err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return err
	}
	nonce := make([]byte, 24)
	copy(nonce, header[24:40])
	for record := uint64(0); ; record++ {
		var length [4]byte
		if _, err = io.ReadFull(src, length[:]); err != nil {
			return errors.New("backup is incomplete")
		}
		size := binary.BigEndian.Uint32(length[:])
		if size < uint32(aead.Overhead()) || size > backupChunkSize+uint32(aead.Overhead()) {
			return errors.New("invalid backup record")
		}
		ciphertext := make([]byte, int(size))
		if _, err = io.ReadFull(src, ciphertext); err != nil {
			return errors.New("backup is incomplete")
		}
		binary.BigEndian.PutUint64(nonce[16:], record)
		plain, err := aead.Open(nil, nonce, ciphertext, header)
		if err != nil {
			return errors.New("backup password is incorrect or the file is damaged")
		}
		if len(plain) == 0 {
			var extra [1]byte
			if n, err := src.Read(extra[:]); n != 0 || err != io.EOF {
				return errors.New("unexpected data after backup")
			}
			return nil
		}
		if _, err = dst.Write(plain); err != nil {
			return err
		}
	}
}
