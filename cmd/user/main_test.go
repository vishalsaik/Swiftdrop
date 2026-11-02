package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"swiftdrop/internal/auth"
	"swiftdrop/internal/auth/authtest"
	"swiftdrop/internal/domain"
)

var cheap = auth.Params{Time: 1, MemoryKiB: 64, Threads: 1, KeyLen: 16, SaltLen: 8}

func service() (*auth.Service, *authtest.Store) {
	store := authtest.New()
	svc := auth.New(store)
	svc.Params = cheap
	return svc, store
}

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func runCmd(svc creator, vars map[string]string, args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(context.Background(), args, env(vars), &out, &errOut, svc)
	return code, out.String(), errOut.String()
}

func TestAStaffAccountIsMadeWithAPasswordShownOnce(t *testing.T) {
	svc, store := service()
	code, out, errOut := runCmd(svc, nil, "-email", "Dee@Example.com", "-name", "Dee Patch")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	u := store.Users["dee@example.com"]
	if u == nil || u.User.Role != domain.RoleStaff || u.User.Name != "Dee Patch" {
		t.Fatalf("account = %+v", u)
	}
	// the printed password is the real one: signing in with it works
	i := strings.Index(out, "shown only now: ")
	if i < 0 {
		t.Fatalf("no password in the output: %s", out)
	}
	printed := strings.Fields(out[i+len("shown only now: "):])[0]
	if len(printed) != 24 {
		t.Errorf("printed password %q has length %d, want 24", printed, len(printed))
	}
	if _, _, err := svc.SignIn(context.Background(), "1.1.1.1", "dee@example.com", printed); err != nil {
		t.Errorf("signing in with the printed password: %v", err)
	}
	if strings.Contains(u.Hash, printed) {
		t.Error("the stored hash contains the password")
	}
}

func TestAPasswordGivenInTheEnvironmentIsUsedAndNeverPrinted(t *testing.T) {
	svc, _ := service()
	code, out, errOut := runCmd(svc, map[string]string{"SWIFTDROP_NEW_PASSWORD": "my own long passphrase"}, "-email", "dee@example.com", "-name", "Dee")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if strings.Contains(out, "my own long passphrase") || strings.Contains(out, "shown only now") {
		t.Errorf("a password the person chose was printed: %s", out)
	}
	if _, _, err := svc.SignIn(context.Background(), "1.1.1.1", "dee@example.com", "my own long passphrase"); err != nil {
		t.Errorf("signing in: %v", err)
	}
}

func TestACustomerAccountCanBeMadeToo(t *testing.T) {
	svc, store := service()
	if code, _, errOut := runCmd(svc, nil, "-email", "c@example.com", "-name", "Cu", "-role", "customer"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if store.Users["c@example.com"].User.Role != domain.RoleCustomer {
		t.Error("not a customer")
	}
}

func TestWhatIsMissingOrWrongIsSaidAndNothingIsMade(t *testing.T) {
	svc, store := service()
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"no email":        {[]string{"-name", "Dee"}, "-email and -name are required"},
		"no name":         {[]string{"-email", "d@example.com"}, "-email and -name are required"},
		"an odd role":     {[]string{"-email", "d@example.com", "-name", "Dee", "-role", "admin"}, "staff or customer"},
		"an unknown flag": {[]string{"-nope"}, "flag provided but not defined"},
	} {
		code, _, errOut := runCmd(svc, nil, c.args...)
		if code != 2 || !strings.Contains(errOut, c.want) {
			t.Errorf("%s: exit %d, %q, want 2 and %q", name, code, errOut, c.want)
		}
	}
	if len(store.Users) != 0 {
		t.Error("an account was made anyway")
	}
}

func TestAnEmailThatIsInUseAndAWeakPasswordAreRefusedClearly(t *testing.T) {
	svc, _ := service()
	if code, _, e := runCmd(svc, nil, "-email", "d@example.com", "-name", "Dee"); code != 0 {
		t.Fatal(e)
	}
	code, _, errOut := runCmd(svc, nil, "-email", "D@example.com", "-name", "Other")
	if code != 1 || !strings.Contains(errOut, "an account for D@example.com already exists") {
		t.Errorf("duplicate: exit %d, %q", code, errOut)
	}
	code, _, errOut = runCmd(svc, map[string]string{"SWIFTDROP_NEW_PASSWORD": "short"}, "-email", "e@example.com", "-name", "Eve")
	if code != 1 || !strings.Contains(errOut, "password must be at least") {
		t.Errorf("weak password: exit %d, %q", code, errOut)
	}
	code, _, errOut = runCmd(svc, nil, "-email", "not an email", "-name", "")
	if code != 2 {
		t.Errorf("empty name: exit %d, %q", code, errOut)
	}
	code, _, errOut = runCmd(svc, nil, "-email", "not an email", "-name", "Eve")
	if code != 1 || !strings.Contains(errOut, "email must be a valid email address") {
		t.Errorf("bad email: exit %d, %q", code, errOut)
	}
}

func TestAStorageFailureIsReportedWithoutThePassword(t *testing.T) {
	svc, store := service()
	store.CreateUserErr = errors.New("connection refused")
	code, out, errOut := runCmd(svc, nil, "-email", "d@example.com", "-name", "Dee")
	if code != 1 || !strings.Contains(errOut, "connection refused") || strings.Contains(out, "shown only now") {
		t.Errorf("exit %d, out %q, err %q", code, out, errOut)
	}
}

func TestGeneratedPasswordsAreLongAndDifferent(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		p, err := generatePassword()
		if err != nil || len(p) != 24 || seen[p] {
			t.Fatalf("password %q, err %v: wrong length or repeated", p, err)
		}
		seen[p] = true
		if why := auth.ValidatePassword(p, "a@b.co", "A"); why != "" {
			t.Errorf("a generated password fails our own rules: %q %s", p, why)
		}
	}
}

func TestEveryProblemIsListedInAlphabeticalOrder(t *testing.T) {
	svc, _ := service()
	code, _, errOut := runCmd(svc, map[string]string{"SWIFTDROP_NEW_PASSWORD": "short"}, "-email", "nope", "-name", "Eve")
	if code != 1 {
		t.Fatalf("exit %d, %q", code, errOut)
	}
	e, p := strings.Index(errOut, "email "), strings.Index(errOut, "password ")
	if e < 0 || p < 0 || e > p {
		t.Errorf("problems = %q, want email before password", errOut)
	}
}
