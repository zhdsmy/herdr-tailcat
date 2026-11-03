package proto

import (
	"bufio"
	"strings"
	"testing"
)

func TestStrictPrelude(t *testing.T) {
	for _, s := range []string{`{"tailcat_herdr":1,"op":"ping","extra":true}` + "\n", strings.Repeat("x", 4096) + "\n", `{"tailcat_herdr":1} {}` + "\n"} {
		var q Request
		if Read(bufio.NewReaderSize(strings.NewReader(s), MaxLine), &q) == nil {
			t.Fatal("accepted invalid prelude")
		}
	}
	r := bufio.NewReaderSize(strings.NewReader(`{"tailcat_herdr":1,"op":"connect"}`+"\nraw\x00bytes"), MaxLine)
	var q Request
	if e := Read(r, &q); e != nil {
		t.Fatal(e)
	}
	b := make([]byte, 9)
	if _, e := r.Read(b); e != nil || string(b) != "raw\x00bytes" {
		t.Fatal("lost bytes after prelude")
	}
}
