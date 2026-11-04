package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type ServerConfig struct {
	HerdrCore         string            `json:"herdr_core"`
	Exports           []string          `json:"exports"`
	MaxConns          int               `json:"max_conns"`
	Env               map[string]string `json:"env,omitempty"`
	ClientIdleTimeout string            `json:"client_idle_timeout,omitempty"`
}
type Profile struct {
	Address string `json:"address"`
}

func DefaultHome() (string, error) {
	d, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, ".local", "state", "herdr-tailcat"), nil
}

// CanonicalPath resolves existing ancestors too, so an absent default directory
// can be compared without creating it. Relative overrides bind to this cwd.
func CanonicalPath(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	parent, err = CanonicalPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}
func Home() (string, error) {
	h := os.Getenv("HERDR_TAILCAT_HOME")
	if h == "" {
		var err error
		h, err = DefaultHome()
		if err != nil {
			return "", err
		}
	}
	h, err := CanonicalPath(h)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(h, 0700); err != nil {
		return "", err
	}
	return h, os.Chmod(h, 0700)
}
func privateParent(path string) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	return os.Chmod(filepath.Dir(path), 0700)
}
func WriteJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return WriteFile(path, b)
}
func WriteFile(path string, b []byte) error {
	if e := privateParent(path); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".write-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	_, werr := f.Write(b)
	serr := f.Sync()
	cerr := f.Close()
	if e = errors.Join(werr, serr, cerr); e != nil {
		return e
	}
	return os.Rename(f.Name(), path)
}
func ReadJSON(path string, v any) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
