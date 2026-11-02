package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"swiftdrop/internal/domain"
	"swiftdrop/web"
)

// ---------------------------------------------------------------------------
// GET /orders: the dashboard's board
// ---------------------------------------------------------------------------

func listOrders(t *testing.T, db *fakeDB, query string) []map[string]any {
	t.Helper()
	rec := do(t, db, http.MethodGet, "/orders"+query, "")
	wantStatus(t, rec, http.StatusOK)
	wantContentTypeJSON(t, rec)
	var body struct {
		Orders []map[string]any `json:"orders"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, rec.Body)
	}
	return body.Orders
}

func TestAnEmptyBoardIsAnEmptyListNotNull(t *testing.T) {
	rec := do(t, newFakeDB(), http.MethodGet, "/orders", "")
	wantStatus(t, rec, http.StatusOK)
	if got := strings.TrimSpace(rec.Body.String()); got != `{"orders":[]}` {
		t.Errorf("body = %s, want an empty list a screen can loop over", got)
	}
}

func TestTheListIsNewestFirstWithEachOrdersPaymentView(t *testing.T) {
	db := newFakeDB()
	now := time.Now()
	db.seedWith("old", domain.OrderCreated, func(o *domain.Order) { o.CreatedAt = now.Add(-2 * time.Hour) })
	db.seedWith("mid", domain.OrderCancelled, func(o *domain.Order) {
		o.CreatedAt, o.PaymentType = now.Add(-time.Hour), domain.PaymentCard
	})
	db.seedWith("new", domain.OrderPaid, func(o *domain.Order) { o.CreatedAt = now })
	db.payments["mid"] = domain.Payment{ID: "pay-mid", OrderID: "mid", Status: domain.PaymentAuthorized, AmountCents: 2500, ProviderPaymentID: "pi_secret"}

	got := listOrders(t, db, "")
	var order []string
	for _, o := range got {
		order = append(order, o["id"].(string))
	}
	if want := []string{"new", "mid", "old"}; fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("order = %v, want newest first %v", order, want)
	}
	if _, has := got[0]["payment"]; has {
		t.Errorf("an order with no payment has one: %v", got[0]["payment"])
	}
	mid := got[1]
	if mid["paymentIssue"] != "refund_pending" {
		t.Errorf("paymentIssue = %v, want refund_pending for a charged cancelled order", mid["paymentIssue"])
	}
	pay, _ := mid["payment"].(map[string]any)
	if pay["status"] != "authorized" || pay["amountCents"] != float64(2500) || pay["refundStatus"] != "none" {
		t.Errorf("payment = %v", pay)
	}
	body, _ := json.Marshal(got)
	if strings.Contains(string(body), "pi_secret") {
		t.Errorf("a provider id leaked into the list: %s", body)
	}
}

func TestTheListAnswersWhatASingleReadAnswers(t *testing.T) {
	g := approving()
	db := newFakeDB()
	seedPayable(db, "o1", domain.PaymentCard, 1000)
	wantStatus(t, payWith(t, db, routerOf(g, g), "o1"), http.StatusOK)
	wantStatus(t, do(t, db, http.MethodPost, "/orders/o1/cancel", ""), http.StatusOK)

	list := listOrders(t, db, "")
	single := getView(t, db, "o1")
	if len(list) != 1 {
		t.Fatalf("%d orders listed, want 1", len(list))
	}
	a, _ := json.Marshal(list[0])
	b, _ := json.Marshal(single)
	if string(a) != string(b) {
		t.Errorf("list entry %s differs from the single read %s", a, b)
	}
}

func TestTheListLimit(t *testing.T) {
	db := newFakeDB()
	for i := range 60 {
		db.seed(fmt.Sprintf("o%02d", i), domain.OrderCreated)
	}
	if got := len(listOrders(t, db, "")); got != defaultListLimit {
		t.Errorf("no limit returned %d, want the default %d", got, defaultListLimit)
	}
	if got := len(listOrders(t, db, "?limit=7")); got != 7 {
		t.Errorf("limit 7 returned %d", got)
	}
	if got := len(listOrders(t, db, "?limit=1")); got != 1 {
		t.Errorf("limit 1 returned %d", got)
	}
	if got := len(listOrders(t, db, fmt.Sprintf("?limit=%d", maxListLimit))); got != 60 {
		t.Errorf("the largest limit returned %d, want all 60 orders", got)
	}
}

func TestABadLimitIsA400AndNothingIsRead(t *testing.T) {
	for _, q := range []string{"0", "-1", "abc", "1.5", "201", "1000000", "%205", "5x"} {
		t.Run(q, func(t *testing.T) {
			db := newFakeDB()
			db.listErr = errors.New("must not be reached")
			rec := do(t, db, http.MethodGet, "/orders?limit="+q, "")
			wantStatus(t, rec, http.StatusBadRequest)
			wantErrorContains(t, rec, "limit", "1 to 200")
		})
	}
}

func TestAStoreFailureListingOrdersIsA500ThatLeaksNothing(t *testing.T) {
	logs := captureLog(t)
	db := newFakeDB()
	db.listErr = errors.New("connection refused")

	rec := do(t, db, http.MethodGet, "/orders", "")
	wantStatus(t, rec, http.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("the cause leaked: %s", rec.Body)
	}
	if !strings.Contains(logs.String(), "connection refused") {
		t.Errorf("the cause is not in the log:\n%s", logs)
	}
}

// Listing is a GET on the same path as creating, and neither disturbs the other.
func TestListingAndCreatingShareAPathWithoutClashing(t *testing.T) {
	db := newFakeDB()
	wantStatus(t, do(t, db, http.MethodPost, "/orders", validBody("o1")), http.StatusCreated)
	if got := len(listOrders(t, db, "")); got != 1 {
		t.Errorf("%d orders listed after one create", got)
	}
}

// ---------------------------------------------------------------------------
// the dashboard files
// ---------------------------------------------------------------------------

func TestTheDashboardIsServedFromTheServer(t *testing.T) {
	rec := do(t, newFakeDB(), http.MethodGet, "/dashboard/", "")
	wantStatus(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want HTML", ct)
	}
	if !strings.Contains(rec.Body.String(), "<title>SwiftDrop Dispatch</title>") {
		t.Errorf("the page is not the dashboard:\n%.200s", rec.Body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache so a new build is picked up", got)
	}
}

// localRefs are the files index.html loads from the server itself, cleaned
// ("./assets/x.js" is "assets/x.js"). The build gives them hashed names, so the tests
// read them from the page instead of knowing them.
func localRefs(t *testing.T) []string {
	t.Helper()
	index, err := fs.ReadFile(web.Static(), "index.html")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range regexp.MustCompile(`(?:src|href)="([^"]+)"`).FindAllStringSubmatch(string(index), -1) {
		ref := m[1]
		if strings.HasPrefix(ref, "http") || strings.HasPrefix(ref, "data:") || strings.HasPrefix(ref, "#") {
			continue
		}
		out = append(out, path.Clean(ref))
	}
	return out
}

func TestEveryDashboardFileIsServedWithItsOwnType(t *testing.T) {
	refs := localRefs(t)
	var sawScript, sawStyle bool
	for _, ref := range refs {
		wantType := map[string]string{".js": "javascript", ".css": "text/css"}[path.Ext(ref)]
		if wantType == "" {
			continue
		}
		sawScript = sawScript || wantType == "javascript"
		sawStyle = sawStyle || wantType == "text/css"
		rec := do(t, newFakeDB(), http.MethodGet, "/dashboard/"+ref, "")
		wantStatus(t, rec, http.StatusOK)
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, wantType) {
			t.Errorf("%s: Content-Type = %q, want %q", ref, ct, wantType)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("%s is empty", ref)
		}
	}
	if !sawScript || !sawStyle {
		t.Errorf("the page loads %v: want a script and a stylesheet, so there is something to check", refs)
	}
}

func TestTheRootAndTheBareDashboardPathLeadToTheDashboard(t *testing.T) {
	for _, path := range []string{"/", "/dashboard"} {
		rec := do(t, newFakeDB(), http.MethodGet, path, "")
		wantStatus(t, rec, http.StatusFound)
		if got := rec.Header().Get("Location"); got != "/dashboard/" {
			t.Errorf("%s redirects to %q, want /dashboard/", path, got)
		}
	}
}

func TestDashboardPathsThatAreNotFilesAreNotFound(t *testing.T) {
	for _, path := range []string{"/dashboard/nope.js", "/dashboard/missing/", "/nope", "/dashboardx/"} {
		wantStatus(t, do(t, newFakeDB(), http.MethodGet, path, ""), http.StatusNotFound)
	}
}

// Only the embedded dashboard folder can be read, never the project around it.
func TestNothingOutsideTheDashboardCanBeRead(t *testing.T) {
	for _, path := range []string{
		"/dashboard/../schema.sql", "/dashboard/%2e%2e/schema.sql", "/dashboard/..%2fschema.sql",
		"/dashboard/../../go.mod", "/dashboard/static/index.html", "/dashboard/web.go",
		"/dashboard/..\\schema.sql", "/dashboard//etc/passwd",
	} {
		rec := do(t, newFakeDB(), http.MethodGet, path, "")
		for _, secret := range []string{"CREATE TABLE", "module swiftdrop", "package web", "root:"} {
			if strings.Contains(rec.Body.String(), secret) {
				t.Errorf("GET %s answered %d with project content (%q)", path, rec.Code, secret)
			}
		}
	}
}

func TestOnlyReadingMethodsReachTheDashboard(t *testing.T) {
	wantStatus(t, do(t, newFakeDB(), http.MethodHead, "/dashboard/", ""), http.StatusOK)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		if got := do(t, newFakeDB(), method, "/dashboard/", "").Code; got != http.StatusMethodNotAllowed {
			t.Errorf("%s /dashboard/ = %d, want 405", method, got)
		}
	}
}

func TestTheAPIStillWorksBesideTheDashboard(t *testing.T) {
	wantStatus(t, do(t, newFakeDB(), http.MethodGet, "/health", ""), http.StatusOK)
	wantStatus(t, do(t, newFakeDB(), http.MethodGet, "/orders/ghost", ""), http.StatusNotFound)
}

// The page may only load files that exist, so it is never half broken.
func TestTheDashboardPageOnlyReferencesFilesThatExist(t *testing.T) {
	static := web.Static()
	refs := localRefs(t)
	for _, ref := range refs {
		if _, err := fs.Stat(static, ref); err != nil {
			t.Errorf("index.html references %q, which is not in the dashboard: %v (run npm run build in web/ui)", ref, err)
		}
	}
	if len(refs) < 2 {
		t.Errorf("found %d local references, want at least the stylesheet and the script", len(refs))
	}
}

// The page must never build markup or code from order data. React draws text as text
// unless the source asks otherwise, so the source (not the bundle, which holds React's
// own internals) is checked for the ways of asking otherwise. The names are joined from
// pieces so that this test does not itself contain them.
func TestTheSourceNeverTurnsTextIntoMarkupOrCode(t *testing.T) {
	banned := []string{
		"dangerously" + "SetInnerHTML", "inner" + "HTML", "outer" + "HTML", "insertAdjacent" + "HTML", "document." + "write", "ev" + "al(", "new " + "Function",
	}
	root := filepath.Join("..", "..", "web", "ui", "src")
	checked := 0
	err := filepath.WalkDir(root, func(file string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch filepath.Ext(file) {
		case ".ts", ".tsx", ".js", ".mjs":
		default:
			return nil
		}
		src, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		checked++
		for _, name := range banned {
			if strings.Contains(string(src), name) {
				t.Errorf("%s uses %s, which turns text into markup or code", file, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 10 {
		t.Errorf("checked %d source files, want the whole dashboard (is web/ui/src where the test looks?)", checked)
	}
}
