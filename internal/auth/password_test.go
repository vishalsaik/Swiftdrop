package auth

import (
	"strings"
	"testing"
)

// cheap keeps the tests fast: the cost only changes how long hashing takes.
var cheap = Params{Time: 1, MemoryKiB: 64, Threads: 1, KeyLen: 16, SaltLen: 8}

func mustHash(t *testing.T, password string) string {
	t.Helper()
	h, err := Hash(password, cheap)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestAHashedPasswordVerifiesAndAWrongOneDoesNot(t *testing.T) {
	h := mustHash(t, "correct horse battery")
	if ok, err := Verify("correct horse battery", h); err != nil || !ok {
		t.Errorf("the right password: ok=%v err=%v", ok, err)
	}
	for _, wrong := range []string{"correct horse batterY", "", "correct horse battery ", "other"} {
		if ok, err := Verify(wrong, h); err != nil || ok {
			t.Errorf("%q: ok=%v err=%v, want a mismatch", wrong, ok, err)
		}
	}
}

func TestEveryHashHasItsOwnSaltAndNeverContainsThePassword(t *testing.T) {
	a, b := mustHash(t, "same password here"), mustHash(t, "same password here")
	if a == b {
		t.Error("two hashes of one password are identical: the salt is not random")
	}
	if strings.Contains(a, "same password here") {
		t.Error("the hash contains the password")
	}
	if !strings.HasPrefix(a, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Errorf("hash = %q, want the settings written into it", a)
	}
}

func TestTheDefaultCostWorks(t *testing.T) {
	h, err := Hash("a password to hash at full cost", DefaultParams)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := Verify("a password to hash at full cost", h); err != nil || !ok {
		t.Errorf("ok=%v err=%v", ok, err)
	}
	if !strings.Contains(h, "m=19456,t=2,p=1") {
		t.Errorf("hash = %q, want OWASP's minimum cost", h)
	}
}

func TestAStringThatIsNotOneOfOurHashesIsAnErrorNotAMismatch(t *testing.T) {
	good := mustHash(t, "whatever password")
	parts := strings.Split(good, "$") // "", argon2id, v=19, params, salt, hash
	rebuilt := func(i int, with string) string {
		p := append([]string(nil), parts...)
		p[i] = with
		return strings.Join(p, "$")
	}
	for name, bad := range map[string]string{
		"empty":                "",
		"plain text":           "hunter2",
		"another algorithm":    rebuilt(1, "argon2i"),
		"another version":      rebuilt(2, "v=16"),
		"no version":           rebuilt(2, "x"),
		"unreadable settings":  rebuilt(3, "m=abc,t=1,p=1"),
		"no memory":            rebuilt(3, "m=0,t=1,p=1"),
		"a gigabyte too much":  rebuilt(3, "m=4000000,t=1,p=1"),
		"no passes":            rebuilt(3, "m=64,t=0,p=1"),
		"far too many passes":  rebuilt(3, "m=64,t=11,p=1"),
		"no threads":           rebuilt(3, "m=64,t=1,p=0"),
		"far too many threads": rebuilt(3, "m=64,t=1,p=17"),
		"salt is not base64":   rebuilt(4, "!!!"),
		"empty salt":           rebuilt(4, ""),
		"hash is not base64":   rebuilt(5, "!!!"),
		"empty hash":           rebuilt(5, ""),
		"an extra part":        good + "$more",
		"a missing part":       strings.Join(parts[:5], "$"),
	} {
		t.Run(name, func(t *testing.T) {
			if ok, err := Verify("whatever password", bad); err == nil || ok {
				t.Errorf("ok=%v err=%v, want an error", ok, err)
			}
		})
	}
}

func TestAHashMadeWithCheaperSettingsIsReplacedAtSignIn(t *testing.T) {
	old := Params{Time: 1, MemoryKiB: 64, Threads: 1, KeyLen: 16, SaltLen: 8}
	h, _ := Hash("some password here", old)
	for name, c := range map[string]struct {
		want Params
		need bool
	}{
		"the same":          {old, false},
		"more memory":       {Params{Time: 1, MemoryKiB: 128, Threads: 1}, true},
		"more passes":       {Params{Time: 2, MemoryKiB: 64, Threads: 1}, true},
		"more lanes":        {Params{Time: 1, MemoryKiB: 64, Threads: 2}, true},
		"cheaper than ours": {Params{Time: 1, MemoryKiB: 32, Threads: 1}, false},
	} {
		if got := NeedsRehash(h, c.want); got != c.need {
			t.Errorf("%s: NeedsRehash = %v, want %v", name, got, c.need)
		}
	}
	if !NeedsRehash("not a hash", old) {
		t.Error("a string that is not a hash should be replaced")
	}
}

func TestPasswordRules(t *testing.T) {
	for _, c := range []struct {
		name, password, email, nameField, want string
	}{
		{"fine", "a long enough phrase", "a@b.co", "Ada", ""},
		{"exactly the minimum", "abcdefghi1", "a@b.co", "Ada", ""},
		{"one short", "abcdefghi", "a@b.co", "Ada", "at least 10"},
		{"empty", "", "a@b.co", "Ada", "at least 10"},
		{"the maximum", strings.Repeat("ab", 64), "a@b.co", "Ada", ""},
		{"one over the maximum", strings.Repeat("ab", 64) + "c", "a@b.co", "Ada", "at most 128"},
		{"a common one", "Password123", "a@b.co", "Ada", "most common"},
		{"a common one, numbers", "1234567890", "a@b.co", "Ada", "most common"},
		{"one character repeated", "aaaaaaaaaaaa", "a@b.co", "Ada", "repeated"},
		{"the email", "ada@example.com", "ada@example.com", "Ada", "email or name"},
		{"the email in other case", "Ada@Example.com", "ada@example.com", "Ada", "email or name"},
		{"the name", "Ada Lovelace", "ada@example.com", "ada lovelace", "email or name"},
		{"letters from other alphabets count once each", "пароль-пароль", "a@b.co", "Ada", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := ValidatePassword(c.password, c.email, c.nameField)
			if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
				t.Errorf("ValidatePassword(%q) = %q, want it to say %q", c.password, got, c.want)
			}
		})
	}
	// Invalid UTF-8 is counted by bytes as runes, so it passes the length rule and
	// is then refused for what it is.
	if got := ValidatePassword(strings.Repeat("\xff", 12), "a@b.co", "Ada"); !strings.Contains(got, "valid text") {
		t.Errorf("invalid UTF-8 = %q, want it refused", got)
	}
}
