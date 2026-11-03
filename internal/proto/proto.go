// Package proto implements the bounded prelude; buffered bytes after it are raw.
package proto

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

const Version = 1
const Port = 6470
const MaxLine = 4096
const Timeout = 10 * time.Second

type Request struct {
	Version int     `json:"tailcat_herdr"`
	Op      string  `json:"op"`
	Session *string `json:"session,omitempty"`
}

// LocalRequest is only used on the UID-checked Unix socket. Its deadline is
// stripped before the public tunnel handshake.
type LocalRequest struct {
	Request
	DeadlineMS int64 `json:"deadline_ms"`
}

type Info struct {
	Version string   `json:"version"`
	Exports []string `json:"exports"`
}
type Response struct {
	Version int    `json:"tailcat_herdr"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Server  *Info  `json:"server,omitempty"`
}

func Read(r *bufio.Reader, v any) error {
	b, err := r.ReadSlice('\n')
	if err != nil {
		var n net.Error
		if errors.As(err, &n) && n.Timeout() {
			return errors.New("handshake timed out")
		}
		return errors.New("invalid or oversized handshake (maximum 4 KiB)")
	}
	if len(b) > MaxLine {
		return errors.New("handshake exceeds 4 KiB")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(v); err != nil {
		return errors.New("invalid handshake JSON or unknown field")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("extra handshake JSON")
	}
	return nil
}
func ReadRequest(c net.Conn, r *bufio.Reader, timeout time.Duration) (Request, error) {
	c.SetReadDeadline(time.Now().Add(timeout))
	defer c.SetReadDeadline(time.Time{})
	var q Request
	if err := Read(r, &q); err != nil {
		return q, err
	}
	return q, Validate(q)
}
func Validate(q Request) error {
	if q.Version != Version {
		return errors.New("unsupported tailcat-herdr protocol version")
	}
	if q.Op != "connect" && q.Op != "ping" {
		return errors.New("unsupported tailcat-herdr operation")
	}
	if q.Op == "ping" && q.Session != nil {
		return errors.New("ping does not accept a session")
	}
	return nil
}
func Write(w io.Writer, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if len(b)+1 > MaxLine {
		return fmt.Errorf("handshake exceeds 4 KiB")
	}
	_, e = w.Write(append(b, '\n'))
	return e
}
func Fail(w io.Writer, err error) { _ = Write(w, Response{Version: Version, Error: err.Error()}) }
