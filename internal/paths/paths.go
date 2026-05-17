package paths

import (
	"os"
	"path/filepath"
)

type Paths struct {
	Root     string
	State    string
	Spool    string
	Logs     string
	AppPID   string
	IconDir  string
	Settings string
}

func ResolveRoot() string {
	if cwd, err := os.Getwd(); err == nil {
		if looksLikeAppRoot(cwd) {
			return cwd
		}
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if looksLikeAppRoot(dir) {
			return dir
		}
		return dir
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return "."
}

func New(root string) Paths {
	state := filepath.Join(root, "state")
	logs := filepath.Join(state, "logs")
	return Paths{
		Root:     root,
		State:    state,
		Spool:    filepath.Join(state, "limitdock-spool"),
		Logs:     logs,
		AppPID:   filepath.Join(state, "limitdock.pid"),
		IconDir:  filepath.Join(root, "assets", "icons"),
		Settings: filepath.Join(root, "settings.json"),
	}
}

func Ensure(p Paths) error {
	for _, dir := range []string{p.State, p.Spool, p.Logs} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func looksLikeAppRoot(dir string) bool {
	for _, name := range []string{"settings.example.json", "go.mod"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "assets", "icons")); err == nil {
		return true
	}
	return false
}
