package native

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// ProtectData encrypts plaintext for the current Windows user via DPAPI.
// entropy namespaces the blob; the same entropy must be passed to
// UnprotectData.
func ProtectData(plaintext, entropy []byte) ([]byte, error) {
	var out windows.DataBlob
	err := windows.CryptProtectData(dataBlob(plaintext), nil, dataBlob(entropy), 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	if err != nil {
		return nil, err
	}
	return copyAndFreeBlob(&out), nil
}

// UnprotectData decrypts a blob produced by ProtectData for the same
// Windows user with the same entropy.
func UnprotectData(ciphertext, entropy []byte) ([]byte, error) {
	var out windows.DataBlob
	err := windows.CryptUnprotectData(dataBlob(ciphertext), nil, dataBlob(entropy), 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	if err != nil {
		return nil, err
	}
	return copyAndFreeBlob(&out), nil
}

func dataBlob(data []byte) *windows.DataBlob {
	if len(data) == 0 {
		return nil
	}
	return &windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
}

func copyAndFreeBlob(blob *windows.DataBlob) []byte {
	if blob.Data == nil {
		return nil
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(blob.Data)))
	return append([]byte(nil), unsafe.Slice(blob.Data, int(blob.Size))...)
}
