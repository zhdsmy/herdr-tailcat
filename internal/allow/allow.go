package allow

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"tailscale.com/types/key"
)

func Load(path string) ([]key.NodePublic, error) {
	return Read(path, false)
}
func Read(path string, emptyOK bool) ([]key.NodePublic, error) {
	f, e := os.Open(path)
	if os.IsNotExist(e) && emptyOK {
		return []key.NodePublic{}, nil
	}
	if e != nil {
		return nil, errors.New("cannot read required allowed_clients file")
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0400 == 0 {
		return nil, errors.New("allowed_clients must be a readable regular file")
	}
	var keys []key.NodePublic
	seen := map[key.NodePublic]bool{}
	s := bufio.NewScanner(f)
	n := 0
	for s.Scan() {
		n++
		text := strings.TrimSpace(s.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		var k key.NodePublic
		if e = k.UnmarshalText([]byte(text)); e != nil || k.IsZero() {
			return nil, fmt.Errorf("invalid client key on allowed_clients line %d", n)
		}
		if seen[k] {
			return nil, fmt.Errorf("duplicate client key on allowed_clients line %d", n)
		}
		seen[k] = true
		keys = append(keys, k)
	}
	if s.Err() != nil {
		return nil, errors.New("cannot read allowed_clients")
	}
	if len(keys) == 0 && !emptyOK {
		return nil, errors.New("allowed_clients must contain at least one valid client key")
	}
	return keys, nil
}
