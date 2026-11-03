package stream

import (
	"io"
	"net"
	"time"
)

func HalfClose(c net.Conn) {
	if w, ok := c.(interface{ CloseWrite() error }); ok {
		_ = w.CloseWrite()
	}
}

// Relay drains after either EOF, but bounds abandoned peers to five seconds.
func Relay(a, b net.Conn, ar, br io.Reader) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(b, ar); HalfClose(b); done <- struct{}{} }()
	go func() { _, _ = io.Copy(a, br); HalfClose(a); done <- struct{}{} }()
	<-done
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	_ = a.Close()
	_ = b.Close()
}
