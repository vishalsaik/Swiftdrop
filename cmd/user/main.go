// Command user makes an account. It is how the first staff account is made: the
// web sign-up only ever makes customers, so nobody can sign themselves up as staff.
//
//	go run ./cmd/user -email dispatcher@example.com -name "Dee Patch"
//
// The password is made up and printed once; sign in and change it. Set
// SWIFTDROP_NEW_PASSWORD to choose it instead (the same rules apply). The database
// is the one the server uses: DATABASE_URL, or the local development database.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"

	"swiftdrop/internal/auth"
	"swiftdrop/internal/databases/adapters"
	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

const defaultDSN = "postgres://postgres:postgres@127.0.0.1:5434/swiftdrop?sslmode=disable"

// creator is the part of the auth service this command uses.
type creator interface {
	CreateStaff(ctx context.Context, email, name, password string) (domain.User, error)
	SignUp(ctx context.Context, ip, email, name, password string) (domain.User, error)
}

func main() {
	dsn := defaultDSN
	if v := os.Getenv("DATABASE_URL"); v != "" {
		dsn = v
	}
	db, err := adapters.NewDatabaseConnection(dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not connect to the database:", err)
		os.Exit(1)
	}
	defer db.Pool.Close()
	if code := run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr, auth.New(db)); code != 0 {
		db.Pool.Close()
		os.Exit(code)
	}
}

// run does the work and returns the exit code, so it can be tested.
func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer, svc creator) int {
	fs := flag.NewFlagSet("user", flag.ContinueOnError)
	fs.SetOutput(stderr)
	email := fs.String("email", "", "the account's email address (required)")
	name := fs.String("name", "", "the person's name (required)")
	role := fs.String("role", string(domain.RoleStaff), "staff or customer")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *email == "" || *name == "" {
		fmt.Fprintln(stderr, "both -email and -name are required")
		fs.Usage()
		return 2
	}

	password := getenv("SWIFTDROP_NEW_PASSWORD")
	generated := password == ""
	if generated {
		var err error
		if password, err = generatePassword(); err != nil {
			fmt.Fprintln(stderr, "could not make a password:", err)
			return 1
		}
	}

	var user domain.User
	var err error
	switch domain.Role(*role) {
	case domain.RoleStaff:
		user, err = svc.CreateStaff(ctx, *email, *name, password)
	case domain.RoleCustomer:
		user, err = svc.SignUp(ctx, "cli", *email, *name, password)
	default:
		fmt.Fprintf(stderr, "-role must be staff or customer, not %q\n", *role)
		return 2
	}
	var invalid *auth.InvalidError
	switch {
	case err == nil:
	case errors.Is(err, ports.ErrEmailTaken):
		fmt.Fprintf(stderr, "an account for %s already exists\n", *email)
		return 1
	case errors.As(err, &invalid):
		fields := make([]string, 0, len(invalid.Fields))
		for f := range invalid.Fields {
			fields = append(fields, f)
		}
		sort.Strings(fields)
		for _, f := range fields {
			fmt.Fprintf(stderr, "%s %s\n", f, invalid.Fields[f])
		}
		return 1
	default:
		fmt.Fprintln(stderr, "could not create the account:", err)
		return 1
	}

	fmt.Fprintf(stdout, "Created the %s account for %s (%s).\n", user.Role, user.Name, user.Email)
	if generated {
		fmt.Fprintf(stdout, "Its password, shown only now: %s\nSign in and change it.\n", password)
	}
	return 0
}

// generatePassword makes a strong random password: 18 random bytes, 24 characters.
func generatePassword() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
