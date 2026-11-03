package redact

import (
	"fmt"
	"io"
	"log"
	"regexp"
	"strings"
	"sync"
)

var tokens = regexp.MustCompile(`tc[A-Za-z0-9_-]{50,}|(?:psk:|privk[e]y:)[a-fA-F0-9]+|nodekey:[a-fA-F0-9]{64}`)
var mu sync.RWMutex
var secrets []string

func Add(values ...string) {
	mu.Lock()
	defer mu.Unlock()
	for _, v := range values {
		if len(v) > 8 {
			secrets = append(secrets, v)
		}
	}
}
func Clean(s string) string {
	mu.RLock()
	defer mu.RUnlock()
	for _, v := range secrets {
		s = strings.ReplaceAll(s, v, "[secret]")
	}
	s = tokens.ReplaceAllString(s, "[redacted]")
	s = strings.ReplaceAll(s, "\r", " ")
	return s
}

type Writer struct{ Out io.Writer }

func (w Writer) Write(b []byte) (int, error) {
	_, e := io.WriteString(w.Out, Clean(string(b)))
	return len(b), e
}
func Logger(w io.Writer) *log.Logger { return log.New(Writer{w}, "", log.LstdFlags) }
func OneLine(e error) string {
	s := Clean(fmt.Sprint(e))
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 512 {
		s = s[:512]
	}
	return s
}
