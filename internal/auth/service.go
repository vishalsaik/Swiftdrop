package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/mail"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"

	"github.com/google/uuid"
)

// Defaults for New.
const (
	DefaultSessionLifetime = 14 * 24 * time.Hour // however active, a sign-in ends after this
	DefaultIdleTimeout     = 24 * time.Hour      // and after this long unused
	touchEvery             = time.Minute         // "last seen" is written at most this often

	tokenBytes = 32
	// tokenLength is the length of a token as the browser holds it.
	tokenLength = 43 // 32 bytes, base64url without padding

	maxEmailLength = 254
	maxNameLength  = 80
)

var (
	// ErrBadCredentials is the only thing a failed sign-in says: it does not tell
	// an unknown email from a wrong password.
	ErrBadCredentials = errors.New("email or password is incorrect")
	// ErrUnauthenticated means there is no valid sign-in.
	ErrUnauthenticated = errors.New("not signed in")
	// ErrWrongPassword means the current password given to change it was wrong.
	ErrWrongPassword = errors.New("current password is incorrect")
	// ErrNotStaff means someone who is not staff tried to manage accounts. The HTTP guard
	// stops that first; this is the second lock.
	ErrNotStaff = errors.New("only staff manage accounts")
)

// InvalidError says which fields of a request are not acceptable and why.
type InvalidError struct{ Fields map[string]string }

func (e *InvalidError) Error() string {
	var parts []string
	for f, why := range e.Fields {
		parts = append(parts, f+" "+why)
	}
	return "invalid: " + strings.Join(parts, "; ")
}

// TooManyAttemptsError means a limit was reached. It says how long to wait.
type TooManyAttemptsError struct{ RetryAfter time.Duration }

func (e *TooManyAttemptsError) Error() string {
	return fmt.Sprintf("too many attempts, try again in %s", e.RetryAfter.Round(time.Second))
}

// Principal is who a request comes from: a signed-in account and the sign-in it
// is using.
type Principal struct {
	User      domain.User
	SessionID string
	// CSRFToken is the session's token for state-changing requests.
	CSRFToken string
}

// Store is what the Service keeps accounts and sign-ins in: the Postgres adapter, or an
// in-memory one in tests.
type Store interface {
	ports.AuthStore
	ports.AccountAdminStore
}

// Service signs people up and in and tells the server who a request is from.
type Service struct {
	Store  Store
	Params Params
	// Now is the clock, for tests.
	Now func() time.Time

	SessionLifetime time.Duration
	IdleTimeout     time.Duration

	byEmail  *limiter // failed sign-ins for one email
	byIP     *limiter // failed sign-ins from one address
	signups  *limiter // sign-ups from one address
	changes  *limiter // failed password changes for one account
	slots    chan struct{}
	dummyMu  sync.Once
	dummyOne string
}

// New returns a Service with the default hashing cost, lifetimes and limits:
// 5 failed sign-ins per email and 30 per address per 15 minutes, 10 sign-ups per
// address per hour.
func New(store Store) *Service {
	return &Service{
		Store: store, Params: DefaultParams, Now: time.Now,
		SessionLifetime: DefaultSessionLifetime, IdleTimeout: DefaultIdleTimeout,
		byEmail: newLimiter(5, 15*time.Minute), byIP: newLimiter(30, 15*time.Minute),
		signups: newLimiter(10, time.Hour), changes: newLimiter(5, 15*time.Minute),
		// Hashing uses real memory, so only a few run at once: a burst of sign-ins
		// waits its turn instead of using all of it.
		slots: make(chan struct{}, 4),
	}
}

// ---------------------------------------------------------------------------
// accounts
// ---------------------------------------------------------------------------

// SignUp makes a customer account. ip is where the request came from, for the
// limit on sign-ups.
func (s *Service) SignUp(ctx context.Context, ip, email, name, password string) (domain.User, error) {
	now := s.Now()
	if wait, blocked := s.signups.blocked("ip:"+ip, now); blocked {
		return domain.User{}, &TooManyAttemptsError{RetryAfter: wait}
	}
	s.signups.hit("ip:"+ip, now)
	return s.createUser(ctx, domain.RoleCustomer, email, name, password)
}

// CreateStaff makes a staff account. It is for the command that sets up the first
// staff, not for the web: nobody can sign themselves up as staff.
func (s *Service) CreateStaff(ctx context.Context, email, name, password string) (domain.User, error) {
	return s.createUser(ctx, domain.RoleStaff, email, name, password)
}

func (s *Service) createUser(ctx context.Context, role domain.Role, email, name, password string) (domain.User, error) {
	email = NormalizeEmail(email)
	name = strings.TrimSpace(name)
	problems := map[string]string{}
	if !validEmail(email) {
		problems["email"] = "must be a valid email address"
	}
	if why := validName(name); why != "" {
		problems["name"] = why
	}
	if why := ValidatePassword(password, email, name); why != "" {
		problems["password"] = why
	}
	if len(problems) > 0 {
		return domain.User{}, &InvalidError{Fields: problems}
	}
	hash, err := s.hash(ctx, password)
	if err != nil {
		return domain.User{}, err
	}
	return s.Store.CreateUser(ctx, domain.User{ID: uuid.NewString(), Email: email, Name: name, Role: role}, hash)
}

// ---------------------------------------------------------------------------
// signing in and out
// ---------------------------------------------------------------------------

// SignIn checks a password and starts a session. It returns the token the browser
// must keep (it is not stored anywhere on our side) and who signed in.
//
// A failure says nothing about why: an unknown email takes as long as a wrong
// password, because the unknown email is checked against a stand-in hash.
func (s *Service) SignIn(ctx context.Context, ip, email, password string) (string, Principal, error) {
	now := s.Now()
	email = NormalizeEmail(email)
	emailKey, ipKey := "email:"+email, "ip:"+ip
	for _, c := range []struct {
		l   *limiter
		key string
	}{{s.byEmail, emailKey}, {s.byIP, ipKey}} {
		if wait, blocked := c.l.blocked(c.key, now); blocked {
			return "", Principal{}, &TooManyAttemptsError{RetryAfter: wait}
		}
	}

	user, hash, err := s.Store.GetUserByEmail(ctx, email)
	switch {
	case err == nil && user.DisabledAt == nil:
	case err == nil, errors.Is(err, ports.ErrUserNotFound):
		hash = s.dummy() // same work as a real check, then the same answer
	default:
		return "", Principal{}, fmt.Errorf("auth: looking up the account: %w", err)
	}
	ok, err := s.verify(ctx, password, hash)
	if err != nil && !errors.Is(err, errBadHash) {
		return "", Principal{}, err
	}
	if !ok || user.ID == "" || user.DisabledAt != nil {
		s.byEmail.hit(emailKey, now)
		s.byIP.hit(ipKey, now)
		return "", Principal{}, ErrBadCredentials
	}
	s.byEmail.reset(emailKey)

	// The hash was made with older, cheaper settings: replace it while we hold the
	// password. A failure here is not the person's problem.
	if NeedsRehash(hash, s.Params) {
		if fresh, herr := s.hash(ctx, password); herr == nil {
			if uerr := s.Store.UpdatePasswordHash(ctx, user.ID, fresh); uerr != nil {
				log.Printf("auth: upgrading the password hash of %s: %v", user.ID, uerr)
			}
		}
	}

	token, err := randomToken()
	if err != nil {
		return "", Principal{}, err
	}
	csrf, err := randomToken()
	if err != nil {
		return "", Principal{}, err
	}
	sess := domain.Session{
		ID: uuid.NewString(), UserID: user.ID, TokenHash: HashToken(token), CSRFToken: csrf,
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(s.SessionLifetime),
	}
	if err := s.Store.CreateSession(ctx, sess); err != nil {
		return "", Principal{}, fmt.Errorf("auth: starting the session: %w", err)
	}
	return token, Principal{User: user, SessionID: sess.ID, CSRFToken: csrf}, nil
}

// Authenticate turns the token a browser sent into who it is, or
// ErrUnauthenticated. A session past its end, or unused for too long, is removed.
func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	if len(token) != tokenLength {
		return Principal{}, ErrUnauthenticated
	}
	sess, user, err := s.Store.GetSession(ctx, HashToken(token))
	switch {
	case errors.Is(err, ports.ErrSessionNotFound):
		return Principal{}, ErrUnauthenticated
	case err != nil:
		return Principal{}, fmt.Errorf("auth: reading the session: %w", err)
	}
	now := s.Now()
	if !now.Before(sess.ExpiresAt) || !now.Before(sess.LastSeenAt.Add(s.IdleTimeout)) || user.DisabledAt != nil {
		if derr := s.Store.DeleteSession(ctx, sess.ID); derr != nil {
			log.Printf("auth: removing an ended session: %v", derr)
		}
		return Principal{}, ErrUnauthenticated
	}
	if now.Sub(sess.LastSeenAt) >= touchEvery {
		if terr := s.Store.TouchSession(ctx, sess.ID, now); terr != nil {
			log.Printf("auth: recording that a session was used: %v", terr)
		}
	}
	return Principal{User: user, SessionID: sess.ID, CSRFToken: sess.CSRFToken}, nil
}

// SignOut ends the session the principal is using.
func (s *Service) SignOut(ctx context.Context, p Principal) error {
	return s.Store.DeleteSession(ctx, p.SessionID)
}

// ChangePassword replaces the password of the principal's account and ends every
// other sign-in of it, so a password changed because it leaked also locks out
// whoever had it.
func (s *Service) ChangePassword(ctx context.Context, p Principal, current, next string) error {
	now := s.Now()
	key := "user:" + p.User.ID
	if wait, blocked := s.changes.blocked(key, now); blocked {
		return &TooManyAttemptsError{RetryAfter: wait}
	}
	_, hash, err := s.Store.GetUserByEmail(ctx, p.User.Email)
	if err != nil {
		return fmt.Errorf("auth: looking up the account: %w", err)
	}
	ok, err := s.verify(ctx, current, hash)
	if err != nil {
		return err
	}
	if !ok {
		s.changes.hit(key, now)
		return ErrWrongPassword
	}
	if why := ValidatePassword(next, p.User.Email, p.User.Name); why != "" {
		return &InvalidError{Fields: map[string]string{"newPassword": why}}
	}
	if next == current {
		return &InvalidError{Fields: map[string]string{"newPassword": "must be different from the current one"}}
	}
	fresh, err := s.hash(ctx, next)
	if err != nil {
		return err
	}
	// It also clears "must change your password" and writes that to the account's history.
	if err := s.Store.ChangePasswordHash(ctx, p.User.ID, fresh); err != nil {
		return fmt.Errorf("auth: saving the new password: %w", err)
	}
	if _, err := s.Store.DeleteUserSessions(ctx, p.User.ID, p.SessionID); err != nil {
		return fmt.Errorf("auth: ending the other sign-ins: %w", err)
	}
	s.changes.reset(key)
	return nil
}

// Cleanup removes sessions that can no longer be used.
func (s *Service) Cleanup(ctx context.Context) (int64, error) {
	now := s.Now()
	return s.Store.DeleteExpiredSessions(ctx, now, now.Add(-s.IdleTimeout))
}

// RunCleanup does Cleanup now and then every interval until ctx is cancelled. It
// blocks, so start it in its own goroutine.
func (s *Service) RunCleanup(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if n, err := s.Cleanup(ctx); err != nil && ctx.Err() == nil {
			log.Printf("auth: removing old sessions: %v", err)
		} else if n > 0 {
			log.Printf("auth: removed %d old sessions", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// hash and verify run argon2 a few at a time and give up if ctx does while waiting.
func (s *Service) hash(ctx context.Context, password string) (string, error) {
	if err := s.acquire(ctx); err != nil {
		return "", err
	}
	defer s.release()
	return Hash(password, s.Params)
}

func (s *Service) verify(ctx context.Context, password, hash string) (bool, error) {
	if err := s.acquire(ctx); err != nil {
		return false, err
	}
	defer s.release()
	return Verify(password, hash)
}

func (s *Service) acquire(ctx context.Context) error {
	select {
	case s.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) release() { <-s.slots }

// dummy is a hash nobody's password matches, with the current cost, that an
// unknown email is checked against.
func (s *Service) dummy() string {
	s.dummyMu.Do(func() {
		h, err := Hash("not-a-real-password-for-timing-only", s.Params)
		if err != nil {
			h = ""
		}
		s.dummyOne = h
	})
	return s.dummyOne
}

// NormalizeEmail trims and lower-cases an address, so one address is one account.
func NormalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

func validEmail(email string) bool {
	if email == "" || len(email) > maxEmailLength {
		return false
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Name != "" || addr.Address != email {
		return false
	}
	at := strings.LastIndex(email, "@")
	return at > 0 && strings.Contains(email[at+1:], ".") && !strings.HasSuffix(email, ".")
}

func validName(name string) string {
	switch {
	case name == "":
		return "is required"
	case utf8.RuneCountInString(name) > maxNameLength:
		return fmt.Sprintf("must be at most %d characters", maxNameLength)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "must not contain control characters"
		}
	}
	return ""
}

// HashToken is what is stored in place of a session token: a stolen database
// cannot be turned back into tokens a browser would accept.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := io.ReadFull(entropy, b); err != nil {
		return "", fmt.Errorf("auth: reading random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ---------------------------------------------------------------------------
// managing other people's accounts (staff only)
// ---------------------------------------------------------------------------

// DefaultAccountLimit and MaxAccountLimit bound a list of accounts.
const (
	DefaultAccountLimit = 100
	MaxAccountLimit     = 500
	// accountHistoryLength is how many lines of an account's history are read.
	accountHistoryLength = 50
)

func requireStaff(actor Principal) error {
	if actor.User.ID == "" || actor.User.Role != domain.RoleStaff {
		return ErrNotStaff
	}
	return nil
}

// ListAccounts lists accounts, newest first. A zero limit means the default; one over the
// maximum is cut to it.
func (s *Service) ListAccounts(ctx context.Context, actor Principal, q ports.UserQuery) ([]domain.User, error) {
	if err := requireStaff(actor); err != nil {
		return nil, err
	}
	if q.Limit <= 0 {
		q.Limit = DefaultAccountLimit
	}
	q.Limit = min(q.Limit, MaxAccountLimit)
	q.Search = strings.TrimSpace(q.Search)
	return s.Store.ListUsers(ctx, q)
}

// AccountDetail returns an account with the latest lines of its history, newest first.
// ports.ErrUserNotFound if there is no such account.
func (s *Service) AccountDetail(ctx context.Context, actor Principal, id string) (domain.User, []domain.UserEvent, error) {
	if err := requireStaff(actor); err != nil {
		return domain.User{}, nil, err
	}
	user, err := s.Store.GetUserByID(ctx, id)
	if err != nil {
		return domain.User{}, nil, err
	}
	events, err := s.Store.ListUserEvents(ctx, id, accountHistoryLength)
	if err != nil {
		return domain.User{}, nil, err
	}
	return user, events, nil
}

// CreateAccount makes an account for somebody else, with a temporary password it returns
// ONCE: it is not stored anywhere in the clear. The person must replace it at their first
// sign-in. Staff accounts are made this way too, so the command line is no longer the only
// way; nobody can make themselves staff.
func (s *Service) CreateAccount(ctx context.Context, actor Principal, email, name string, role domain.Role) (domain.User, string, error) {
	if err := requireStaff(actor); err != nil {
		return domain.User{}, "", err
	}
	email = NormalizeEmail(email)
	name = strings.TrimSpace(name)
	problems := map[string]string{}
	if !validEmail(email) {
		problems["email"] = "must be a valid email address"
	}
	if why := validName(name); why != "" {
		problems["name"] = why
	}
	if !role.Valid() {
		problems["role"] = "must be staff or customer"
	}
	if len(problems) > 0 {
		return domain.User{}, "", &InvalidError{Fields: problems}
	}
	password, err := GeneratePassword()
	if err != nil {
		return domain.User{}, "", err
	}
	hash, err := s.hash(ctx, password)
	if err != nil {
		return domain.User{}, "", err
	}
	user, err := s.Store.CreateUserBy(ctx, actor.User,
		domain.User{ID: uuid.NewString(), Email: email, Name: name, Role: role, MustChangePassword: true}, hash)
	if err != nil {
		return domain.User{}, "", err
	}
	return user, password, nil
}

// SetDisabled switches an account off (ending its sign-ins at once) or back on. It is not
// possible for your own account or for the last active staff account (ports.ErrOwnAccount,
// ports.ErrLastStaff). Doing it to an account that is already so changes nothing.
func (s *Service) SetDisabled(ctx context.Context, actor Principal, id string, disabled bool) (domain.User, error) {
	if err := requireStaff(actor); err != nil {
		return domain.User{}, err
	}
	user, _, err := s.Store.SetUserDisabled(ctx, actor.User, id, disabled)
	return user, err
}

// ResetPassword gives an account a new temporary password, returned ONCE, ends its sign-ins
// and makes the person choose their own at the next one. Not for your own account.
func (s *Service) ResetPassword(ctx context.Context, actor Principal, id string) (domain.User, string, error) {
	if err := requireStaff(actor); err != nil {
		return domain.User{}, "", err
	}
	if actor.User.ID == id {
		return domain.User{}, "", ports.ErrOwnAccount
	}
	password, err := GeneratePassword()
	if err != nil {
		return domain.User{}, "", err
	}
	hash, err := s.hash(ctx, password)
	if err != nil {
		return domain.User{}, "", err
	}
	user, err := s.Store.ResetUserPassword(ctx, actor.User, id, hash)
	if err != nil {
		return domain.User{}, "", err
	}
	return user, password, nil
}
