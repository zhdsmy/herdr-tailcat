package redact

import (
	"strings"
	"testing"
)

func TestSecrets(t *testing.T) {
	Add("specific-secret-value")
	for _, s := range []string{"tc" + strings.Repeat("z", 90), "psk:" + strings.Repeat("a", 64), "priv" + "key:" + strings.Repeat("1", 64), "specific-secret-value"} {
		if strings.Contains(Clean("prefix "+s+" suffix"), s) {
			t.Fatal("secret survived")
		}
	}
	if strings.Contains(OneLine(errorsForTest{}), "\n") {
		t.Fatal("multiline error")
	}
}

type errorsForTest struct{}

func (errorsForTest) Error() string { return "bad\nreason\rtest" }
