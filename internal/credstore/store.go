// Package credstore persists LimitDock-owned credentials as DPAPI-encrypted
// JSON blobs under the app's state directory. The protection boundary is the
// current Windows user account.
package credstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"limitdock/internal/fsutil"
	"limitdock/internal/native"
)

var ErrNotFound = errors.New("credstore: not found")

// entropy namespaces LimitDock blobs; it is deliberately a documented
// constant, not a secret.
var entropy = []byte("LimitDock:credential-store:v1")

type Store struct {
	Dir string
}

func New(dir string) Store { return Store{Dir: dir} }

func (s Store) path(name string) string { return filepath.Join(s.Dir, name+".bin") }

func (s Store) Load(name string, out any) error {
	blob, err := os.ReadFile(s.path(name))
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	plain, err := native.UnprotectData(blob, entropy)
	if err != nil {
		return err
	}
	return json.Unmarshal(plain, out)
}

func (s Store) Save(name string, in any) error {
	plain, err := json.Marshal(in)
	if err != nil {
		return err
	}
	blob, err := native.ProtectData(plain, entropy)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	return fsutil.AtomicWriteFile(s.path(name), blob, 0o600)
}

func (s Store) Delete(name string) error {
	err := os.Remove(s.path(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
