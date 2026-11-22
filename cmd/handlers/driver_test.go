package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"swiftdrop/internal/databases/drivertest"
	"swiftdrop/internal/domain"
	"swiftdrop/internal/geocode"
)

// ---------------------------------------------------------------------------
// Drivers: who may use the driver routes, what a driver can say about themselves, and what staff see.
// Everything goes through real sign-in (cookie and CSRF), like the dashboard does it.
// ---------------------------------------------------------------------------

type driverWorld struct {
	*world
	board *drivertest.Board
}

func newDriverWorld(t *testing.T) *driverWorld {
	t.Helper()
	w := newWorld(t)
	board := drivertest.New()
	w.s.Drivers = board
	return &driverWorld{world: w, board: board}
}

// driver makes a driver account that is signed in (its password is its own, not a temporary one).
func (w *driverWorld) driver(t *testing.T, email string) *browser {
	t.Helper()
	u, err := w.svc.CreateDriver(ctxBackground, email, "Dev Driver", password)
	if err != nil {
		t.Fatal(err)
	}
	w.board.AddDriver(u.ID, u.Name)
	b := w.browser(t)
	wantStatus(t, b.signIn(email, password), http.StatusOK)
	return b
}

type driverBody struct {
	Driver domain.Driver `json:"driver"`
}

type driversBody struct {
	Drivers []domain.Driver `json:"drivers"`
}

func TestOnlyADriverUsesTheDriverRoutes(t *testing.T) {
	w := newDriverWorld(t)
	staff := w.staff(t, "dee@example.com")
	customer := w.customer(t, "ada@example.com")
	anon := w.browser(t)

	for _, route := range [][3]string{
		{http.MethodGet, "/driver/me", ""},
		{http.MethodPost, "/driver/online", ""},
		{http.MethodPost, "/driver/offline", ""},
		{http.MethodPost, "/driver/location", `{"latitude":12.9,"longitude":77.6}`},
	} {
		wantStatus(t, anon.do(route[0], route[1], route[2]), http.StatusUnauthorized)
		for who, b := range map[string]*browser{"staff": staff, "customer": customer} {
			rec := b.do(route[0], route[1], route[2])
			wantStatus(t, rec, http.StatusForbidden)
			if !strings.Contains(rec.Body.String(), "needs a driver account") {
				t.Errorf("%s %s as %s: body %s, want it to say it needs a driver account", route[0], route[1], who, rec.Body.String())
			}
		}
	}

	// the list of drivers is for staff only
	driver := w.driver(t, "dev@example.com")
	wantStatus(t, anon.do(http.MethodGet, "/drivers", ""), http.StatusUnauthorized)
	wantStatus(t, customer.do(http.MethodGet, "/drivers", ""), http.StatusForbidden)
	wantStatus(t, driver.do(http.MethodGet, "/drivers", ""), http.StatusForbidden)
	wantStatus(t, staff.do(http.MethodGet, "/drivers", ""), http.StatusOK)
}

func TestADriverCannotReachWhatIsForCustomersAndStaff(t *testing.T) {
	w := newDriverWorld(t)
	driver := w.driver(t, "dev@example.com")

	// signed in, but none of the customer's or staff's routes: a driver sees the delivery they are doing and nothing else
	for _, route := range [][3]string{
		{http.MethodGet, "/orders", ""},
		{http.MethodPost, "/orders", `{}`},
		{http.MethodGet, "/orders/o1", ""},
		{http.MethodPost, "/orders/o1/cancel", ""},
		{http.MethodPost, "/orders/o1/pay", ""},
		{http.MethodGet, "/restaurants", ""},
		{http.MethodGet, "/restaurants/r1/menu", ""},
		{http.MethodGet, "/search?q=burger", ""},
		{http.MethodGet, "/geocode?q=road", ""},
		{http.MethodGet, "/addresses", ""},
		{http.MethodPost, "/addresses", `{}`},
		{http.MethodGet, "/users", ""},
		{http.MethodGet, "/alerts", ""},
		{http.MethodPost, "/orders/o1/ready", ""},
		{http.MethodPost, "/orders/o1/accept", ""},
	} {
		rec := driver.do(route[0], route[1], route[2])
		if rec.Code != http.StatusForbidden {
			t.Errorf("a driver on %s %s got %d (%s), want 403", route[0], route[1], rec.Code, strings.TrimSpace(rec.Body.String()))
		}
	}

	// but they can see who they are and sign out
	rec := driver.do(http.MethodGet, "/auth/me", "")
	wantStatus(t, rec, http.StatusOK)
	if b := decode[sessionBody](t, rec.Body); b.User.Role != domain.RoleDriver {
		t.Errorf("/auth/me role = %q, want driver", b.User.Role)
	}
	wantStatus(t, driver.do(http.MethodPost, "/auth/logout", ""), http.StatusNoContent)
	wantStatus(t, driver.do(http.MethodGet, "/auth/me", ""), http.StatusUnauthorized)
}

func TestStaffMakeADriverWhoMustChooseTheirOwnPasswordFirst(t *testing.T) {
	w := newDriverWorld(t)
	staff := w.staff(t, "dee@example.com")

	rec := staff.do(http.MethodPost, "/users", `{"email":"dev@example.com","name":"Dev Driver","role":"driver"}`)
	wantStatus(t, rec, http.StatusCreated)
	made := decode[struct {
		User              domain.User `json:"user"`
		TemporaryPassword string      `json:"temporaryPassword"`
	}](t, rec.Body)
	if made.User.Role != domain.RoleDriver || !made.User.MustChangePassword || made.TemporaryPassword == "" {
		t.Fatalf("created %+v, want a driver with a temporary password to replace", made)
	}
	w.board.AddDriver(made.User.ID, made.User.Name)

	driver := w.browser(t)
	wantStatus(t, driver.signIn("dev@example.com", made.TemporaryPassword), http.StatusOK)
	// until the password is replaced the account can do nothing else, the driver routes included
	rec = driver.do(http.MethodGet, "/driver/me", "")
	if rec.Code == http.StatusOK || !strings.Contains(rec.Body.String(), "password_change_required") {
		t.Fatalf("a driver on a temporary password reached /driver/me: %d %s", rec.Code, rec.Body.String())
	}
	wantStatus(t, driver.do(http.MethodPost, "/auth/password", `{"currentPassword":"`+made.TemporaryPassword+`","newPassword":"a brand new long phrase"}`), http.StatusNoContent)
	wantStatus(t, driver.signIn("dev@example.com", "a brand new long phrase"), http.StatusOK)
	wantStatus(t, driver.do(http.MethodGet, "/driver/me", ""), http.StatusOK)

	// a role that does not exist is refused with the roles there are
	rec = staff.do(http.MethodPost, "/users", `{"email":"x@example.com","name":"X Y","role":"courier"}`)
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	if f := decode[fieldsBody](t, rec.Body); f.Fields["role"] != "must be staff, customer or driver" {
		t.Errorf("role problem = %q", f.Fields["role"])
	}
}

func TestAccountsCanBeListedByTheDriverRole(t *testing.T) {
	w := newDriverWorld(t)
	staff := w.staff(t, "dee@example.com")
	w.driver(t, "dev@example.com")
	w.customer(t, "ada@example.com")

	rec := staff.do(http.MethodGet, "/users?role=driver", "")
	wantStatus(t, rec, http.StatusOK)
	got := decode[struct {
		Accounts []domain.User `json:"accounts"`
	}](t, rec.Body)
	if len(got.Accounts) != 1 || got.Accounts[0].Email != "dev@example.com" || got.Accounts[0].Role != domain.RoleDriver {
		t.Errorf("drivers listed = %+v, want only dev@example.com", got.Accounts)
	}
	rec = staff.do(http.MethodGet, "/users?role=courier", "")
	wantStatus(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), "staff, customer or driver") {
		t.Errorf("body = %s, want the roles named", rec.Body.String())
	}
}

func TestADriverGoesOnlineAndOfflineAndSeesTheirOwnState(t *testing.T) {
	w := newDriverWorld(t)
	driver := w.driver(t, "dev@example.com")

	rec := driver.do(http.MethodGet, "/driver/me", "")
	wantStatus(t, rec, http.StatusOK)
	if d := decode[driverBody](t, rec.Body).Driver; d.Online || d.Located() || d.Name != "Dev Driver" {
		t.Errorf("a new driver = %+v, want offline, unlocated, named", d)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("a driver's state must not be cached")
	}

	rec = driver.do(http.MethodPost, "/driver/online", "")
	wantStatus(t, rec, http.StatusOK)
	if d := decode[driverBody](t, rec.Body).Driver; !d.Online || d.OnlineSince == nil {
		t.Errorf("after going online = %+v", d)
	}
	rec = driver.do(http.MethodPost, "/driver/online", "") // saying it again is fine
	wantStatus(t, rec, http.StatusOK)

	rec = driver.do(http.MethodPost, "/driver/offline", "")
	wantStatus(t, rec, http.StatusOK)
	if d := decode[driverBody](t, rec.Body).Driver; d.Online {
		t.Errorf("after going offline = %+v", d)
	}
}

func TestADriverSendsWhereTheyAreOnlyWhileOnline(t *testing.T) {
	w := newDriverWorld(t)
	driver := w.driver(t, "dev@example.com")
	where := `{"latitude":12.9719,"longitude":77.6412}`

	rec := driver.do(http.MethodPost, "/driver/location", where)
	wantStatus(t, rec, http.StatusConflict)
	if !strings.Contains(rec.Body.String(), "go online") {
		t.Errorf("body = %s, want it to say to go online first", rec.Body.String())
	}

	wantStatus(t, driver.do(http.MethodPost, "/driver/online", ""), http.StatusOK)
	rec = driver.do(http.MethodPost, "/driver/location", where)
	wantStatus(t, rec, http.StatusOK)
	d := decode[driverBody](t, rec.Body).Driver
	if !d.Located() || *d.Latitude != 12.9719 || *d.Longitude != 77.6412 || d.LocationAt == nil {
		t.Errorf("driver = %+v, want the point they sent", d)
	}

	// the next point replaces it, and going offline forgets it
	wantStatus(t, driver.do(http.MethodPost, "/driver/location", `{"latitude":13,"longitude":77.7}`), http.StatusOK)
	rec = driver.do(http.MethodPost, "/driver/offline", "")
	if d := decode[driverBody](t, rec.Body).Driver; d.Located() {
		t.Errorf("a driver who went offline is still located: %+v", d)
	}
}

func TestALocationMustBeAPlaceThatExists(t *testing.T) {
	w := newDriverWorld(t)
	w.s.DriverLimit = nil // many requests below; the limit has its own test
	driver := w.driver(t, "dev@example.com")
	wantStatus(t, driver.do(http.MethodPost, "/driver/online", ""), http.StatusOK)

	for name, body := range map[string]string{
		"no latitude":       `{"longitude":77.6}`,
		"no longitude":      `{"latitude":12.9}`,
		"nothing":           `{}`,
		"latitude too big":  `{"latitude":91,"longitude":77.6}`,
		"latitude too low":  `{"latitude":-91,"longitude":77.6}`,
		"longitude too big": `{"latitude":12.9,"longitude":181}`,
		"longitude too low": `{"latitude":12.9,"longitude":-181}`,
	} {
		rec := driver.do(http.MethodPost, "/driver/location", body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d (%s), want 422", name, rec.Code, strings.TrimSpace(rec.Body.String()))
		}
	}
	for name, body := range map[string]string{
		"not JSON":      `latitude=12.9`,
		"unknown field": `{"latitude":12.9,"longitude":77.6,"speed":40}`,
		"wrong type":    `{"latitude":"12.9","longitude":77.6}`,
	} {
		if rec := driver.do(http.MethodPost, "/driver/location", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
		}
	}
	if d, _ := w.board.GetDriver(ctxBackground, driver.userID); d.Located() {
		t.Errorf("a refused location was kept: %+v", d)
	}
	rec := driver.do(http.MethodPost, "/driver/location", `{"latitude":12.9,"longitude":77.6,"pad":"`+strings.Repeat("x", 2<<20)+`"}`)
	wantStatus(t, rec, http.StatusRequestEntityTooLarge)
}

func TestADriverMaySendOnlySoManyLocationsAMinute(t *testing.T) {
	w := newDriverWorld(t)
	w.s.DriverLimit = geocode.NewLimiter(3, time.Minute)
	one := w.driver(t, "one@example.com")
	two := w.driver(t, "two@example.com")
	wantStatus(t, one.do(http.MethodPost, "/driver/online", ""), http.StatusOK)
	wantStatus(t, two.do(http.MethodPost, "/driver/online", ""), http.StatusOK)

	where := `{"latitude":12.9,"longitude":77.6}`
	for range 3 {
		wantStatus(t, one.do(http.MethodPost, "/driver/location", where), http.StatusOK)
	}
	rec := one.do(http.MethodPost, "/driver/location", where)
	wantStatus(t, rec, http.StatusTooManyRequests)
	if secs, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || secs < 1 || secs > 60 {
		t.Errorf("Retry-After = %q, want a number of seconds up to a minute", rec.Header().Get("Retry-After"))
	}
	// the limit is each driver's own
	wantStatus(t, two.do(http.MethodPost, "/driver/location", where), http.StatusOK)
}

func TestTheDriverRoutesSayWhenDriversAreNotSetUp(t *testing.T) {
	w := newDriverWorld(t)
	driver := w.driver(t, "dev@example.com")
	staff := w.staff(t, "dee@example.com")
	w.s.Drivers = nil

	for _, route := range [][3]string{
		{http.MethodGet, "/driver/me", ""},
		{http.MethodPost, "/driver/online", ""},
		{http.MethodPost, "/driver/offline", ""},
		{http.MethodPost, "/driver/location", `{"latitude":12.9,"longitude":77.6}`},
	} {
		wantStatus(t, driver.do(route[0], route[1], route[2]), http.StatusServiceUnavailable)
	}
	wantStatus(t, staff.do(http.MethodGet, "/drivers", ""), http.StatusServiceUnavailable)
}

func TestTheDriverRoutesSayWhenStorageBreaks(t *testing.T) {
	w := newDriverWorld(t)
	driver := w.driver(t, "dev@example.com")
	staff := w.staff(t, "dee@example.com")
	wantStatus(t, driver.do(http.MethodPost, "/driver/online", ""), http.StatusOK)
	w.board.Err = errors.New("database is down")

	for _, route := range [][3]string{
		{http.MethodGet, "/driver/me", ""},
		{http.MethodPost, "/driver/online", ""},
		{http.MethodPost, "/driver/offline", ""},
		{http.MethodPost, "/driver/location", `{"latitude":12.9,"longitude":77.6}`},
	} {
		rec := driver.do(route[0], route[1], route[2])
		wantStatus(t, rec, http.StatusInternalServerError)
		if strings.Contains(rec.Body.String(), "database is down") {
			t.Errorf("%s leaked the storage error: %s", route[1], rec.Body.String())
		}
	}
	wantStatus(t, staff.do(http.MethodGet, "/drivers", ""), http.StatusInternalServerError)
}

func TestADriverWhoseRecordIsGoneIsNotFound(t *testing.T) {
	w := newDriverWorld(t)
	driver := w.driver(t, "dev@example.com")
	w.board = drivertest.New() // the driver's state is not there (the account says driver, the store does not know it)
	w.s.Drivers = w.board
	wantStatus(t, driver.do(http.MethodGet, "/driver/me", ""), http.StatusNotFound)
}

func TestStaffSeeEveryDriverWhoIsOnlineFirst(t *testing.T) {
	w := newDriverWorld(t)
	staff := w.staff(t, "dee@example.com")
	zed := w.driver(t, "zed@example.com")
	w.driver(t, "amy@example.com")
	wantStatus(t, zed.do(http.MethodPost, "/driver/online", ""), http.StatusOK)
	wantStatus(t, zed.do(http.MethodPost, "/driver/location", `{"latitude":12.9,"longitude":77.6}`), http.StatusOK)

	rec := staff.do(http.MethodGet, "/drivers", "")
	wantStatus(t, rec, http.StatusOK)
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("the drivers list must not be cached")
	}
	drivers := decode[driversBody](t, rec.Body).Drivers
	if len(drivers) != 2 || !drivers[0].Online || drivers[1].Online {
		t.Fatalf("drivers = %+v, want the online one first", drivers)
	}
	if !drivers[0].Located() || drivers[1].Located() {
		t.Errorf("only the online driver has a point: %+v", drivers)
	}
}
