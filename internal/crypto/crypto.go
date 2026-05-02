//go:build windows

package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	masterKeySize = 32
	nonceSize     = 12
	appDirName    = "gcp-mailnode"
	keyFileName   = "master.key"
)

var (
	masterKey   []byte
	mu          sync.RWMutex
	initialized bool

	crypt32           = windows.NewLazyDLL("crypt32.dll")
	procProtectData   = crypt32.NewProc("CryptProtectData")
	procUnprotectData = crypt32.NewProc("CryptUnprotectData")

	kernel32         = windows.NewLazyDLL("kernel32.dll")
	procLocalFree    = kernel32.NewProc("LocalFree")
)

// dataBlob mirrors DATA_BLOB (CRYPTOAPI_BLOB) used by DPAPI.
type dataBlob struct {
	cbData uint32
	pbData *byte
}

func newBlob(d []byte) *dataBlob {
	if len(d) == 0 {
		return &dataBlob{}
	}
	return &dataBlob{
		cbData: uint32(len(d)),
		pbData: &d[0],
	}
}

func (b *dataBlob) toBytes() []byte {
	if b.cbData == 0 || b.pbData == nil {
		return nil
	}
	out := make([]byte, b.cbData)
	src := unsafe.Slice(b.pbData, b.cbData)
	copy(out, src)
	return out
}

func (b *dataBlob) free() {
	if b.pbData != nil {
		procLocalFree.Call(uintptr(unsafe.Pointer(b.pbData)))
		b.pbData = nil
		b.cbData = 0
	}
}

// dpapiProtect encrypts data with the current user's DPAPI key.
func dpapiProtect(plain []byte) ([]byte, error) {
	in := newBlob(plain)
	var out dataBlob
	r, _, err := procProtectData.Call(
		uintptr(unsafe.Pointer(in)),
		0, // szDataDescr
		0, // pOptionalEntropy
		0, // pvReserved
		0, // pPromptStruct
		0, // dwFlags (default scope: current user)
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return nil, fmt.Errorf("CryptProtectData failed: %w", err)
	}
	defer out.free()
	return out.toBytes(), nil
}

// dpapiUnprotect decrypts data produced by dpapiProtect for the same user.
func dpapiUnprotect(cipher []byte) ([]byte, error) {
	in := newBlob(cipher)
	var out dataBlob
	r, _, err := procUnprotectData.Call(
		uintptr(unsafe.Pointer(in)),
		0, // ppszDataDescr
		0, // pOptionalEntropy
		0, // pvReserved
		0, // pPromptStruct
		0, // dwFlags
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return nil, fmt.Errorf("CryptUnprotectData failed: %w", err)
	}
	defer out.free()
	return out.toBytes(), nil
}

func keyFilePath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config dir: %w", err)
	}
	return filepath.Join(base, appDirName, keyFileName), nil
}

// Init loads or generates the 32-byte AES master key protected by DPAPI.
func Init() error {
	mu.Lock()
	defer mu.Unlock()

	if initialized {
		return nil
	}

	path, err := keyFilePath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create key dir: %w", err)
	}

	if data, err := os.ReadFile(path); err == nil {
		plain, derr := dpapiUnprotect(data)
		if derr != nil {
			return fmt.Errorf("decrypt master key: %w", derr)
		}
		if len(plain) != masterKeySize {
			return fmt.Errorf("master key size invalid: got %d, want %d", len(plain), masterKeySize)
		}
		masterKey = plain
		initialized = true
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read master key: %w", err)
	}

	// Generate a fresh 32-byte key.
	newKey := make([]byte, masterKeySize)
	if _, err := io.ReadFull(rand.Reader, newKey); err != nil {
		return fmt.Errorf("generate master key: %w", err)
	}

	protected, err := dpapiProtect(newKey)
	if err != nil {
		return fmt.Errorf("protect master key: %w", err)
	}

	if err := os.WriteFile(path, protected, 0o600); err != nil {
		return fmt.Errorf("write master key: %w", err)
	}

	masterKey = newKey
	initialized = true
	return nil
}

// IsInitialized reports whether the master key has been loaded.
func IsInitialized() bool {
	mu.RLock()
	defer mu.RUnlock()
	return initialized
}

func currentKey() ([]byte, error) {
	mu.RLock()
	defer mu.RUnlock()
	if !initialized || len(masterKey) != masterKeySize {
		return nil, errors.New("crypto: not initialized")
	}
	return masterKey, nil
}

// Encrypt returns nonce(12) || ciphertext || tag using AES-256-GCM.
func Encrypt(plaintext []byte) ([]byte, error) {
	key, err := currentKey()
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}

	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	// Seal appends ciphertext||tag to its first arg.
	out := make([]byte, 0, nonceSize+len(plaintext)+gcm.Overhead())
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, plaintext, nil)
	return out, nil
}

// Decrypt expects nonce(12) || ciphertext || tag.
func Decrypt(ciphertext []byte) ([]byte, error) {
	key, err := currentKey()
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < nonceSize {
		return nil, errors.New("crypto: ciphertext too short")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}

	nonce := ciphertext[:nonceSize]
	body := ciphertext[nonceSize:]
	plain, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, fmt.Errorf("gcm open: %w", err)
	}
	return plain, nil
}
