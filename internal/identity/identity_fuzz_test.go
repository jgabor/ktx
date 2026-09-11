package identity

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzDeriveNativeIdentity(f *testing.F) {
	for _, seed := range [][3]string{{"opencode", "session", "call"}, {" CODEX\t", " session\n", "call"}, {"", "s", "c"}, {"r", "\xff", "c"}, {"r", "\u00a0", "\x00"}, {"r", "a:b", "c"}, {"r", "a", "b:c"}} {
		f.Add(seed[0], seed[1], seed[2])
	}
	f.Fuzz(func(t *testing.T, runtime, session, call string) {
		if len(runtime)+len(session)+len(call) > 4096 {
			t.Skip()
		}
		valid := true
		for _, field := range []string{runtime, session, call} {
			valid = valid && utf8.ValidString(field) && strings.Trim(field, " \t\n\v\f\r") != ""
		}
		id, err := Derive(runtime, session, call)
		if (err == nil) != valid {
			t.Fatal("tuple validation differs from UTF-8/nonempty contract")
		}
		if !valid {
			return
		}
		if !IsCanonical(id) {
			t.Fatal("derived identity is not canonical")
		}
		again, err := Derive(runtime, session, call)
		if err != nil || id != again {
			t.Fatal("identity is not deterministic")
		}
		padded, err := Derive("\t"+runtime+"\r", " "+session+"\n", "\v"+call+"\f")
		if err != nil || id != padded {
			t.Fatal("ASCII boundary whitespace changes identity")
		}
	})
}
