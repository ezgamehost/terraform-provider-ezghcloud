package validate

import "testing"

func TestProblem(t *testing.T) {
	for _, c := range []struct {
		s                string
		wildcard, record bool
		bad              bool
	}{
		{"example.com", false, false, false}, {"Example.com", false, false, true}, {"example.com.", false, false, true},
		{"*.example.com", false, false, true}, {"*.example.com", true, false, false}, {"a*.example.com", true, false, true},
		{"xn--bcher-kva.example", false, false, false}, {"bücher.example", false, false, true}, {"a..b", false, false, true}, {"-a.com", false, false, true},
		{"@", false, true, false}, {"www", false, true, false}, {"WWW", false, true, true}, {"_dmarc.mail", false, true, false}, {"*", false, true, false},
		{"*.sub", false, true, false}, {"a.*", false, true, true}, {"www.", false, true, true},
	} {
		if got := Problem(c.s, c.wildcard, c.record) != ""; got != c.bad {
			t.Errorf("Problem(%q, %v, %v) bad=%v, want %v", c.s, c.wildcard, c.record, got, c.bad)
		}
	}
}
