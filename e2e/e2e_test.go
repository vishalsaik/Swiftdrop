// Package e2e walks the whole product over HTTP, against a server that is already running with a
// real database behind it: the Docker Compose stack, locally or in CI. It has no mocks. It is skipped
// unless SWIFTDROP_E2E_URL says where the server is.
//
//	SWIFTDROP_E2E_URL=http://127.0.0.1:8080
//	SWIFTDROP_E2E_STAFF_EMAIL=...        a staff account that exists (see the README for making one)
//	SWIFTDROP_E2E_STAFF_PASSWORD=...     its password
//
// For the refund step to finish quickly start the server with REFUND_INTERVAL=2s; at the default of
// 30 seconds the test simply waits longer.
package e2e

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"testing"
	"time"
)

// session is one person's browser: a cookie jar and the CSRF token the server gave at sign-in.
type session struct {
	t    *testing.T
	base string
	http *http.Client
	csrf string
}

type answer struct {
	status int
	body   map[string]any
	raw    string
}

func newSession(t *testing.T, base string) *session {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &session{t: t, base: base, http: &http.Client{Jar: jar, Timeout: 20 * time.Second}}
}

func (s *session) do(method, path string, body any) answer {
	s.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			s.t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.base+path, rdr)
	if err != nil {
		s.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.csrf != "" && method != http.MethodGet {
		req.Header.Set("X-CSRF-Token", s.csrf)
	}
	res, err := s.http.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := answer{status: res.StatusCode, raw: string(raw)}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

// want fails the test unless the answer has this status; it shows the server's own words.
func (s *session) want(a answer, status int, what string) answer {
	s.t.Helper()
	if a.status != status {
		s.t.Fatalf("%s: status %d, want %d (%s)", what, a.status, status, a.raw)
	}
	return a
}

func (s *session) takeSession(a answer) {
	if tok, ok := a.body["csrfToken"].(string); ok {
		s.csrf = tok
	}
}

func (s *session) signIn(email, password string) {
	s.t.Helper()
	a := s.want(s.do("POST", "/auth/login", map[string]string{"email": email, "password": password}), 200, "sign in")
	s.takeSession(a)
}

func (s *session) signUp(email, name, password string) {
	s.t.Helper()
	a := s.want(s.do("POST", "/auth/signup", map[string]string{"email": email, "name": name, "password": password}), 201, "sign up")
	s.takeSession(a)
}

func unique() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func str(v any) string { s, _ := v.(string); return s }

func num(v any) int64 { f, _ := v.(float64); return int64(f) }

// totalOf is what an order's lines come to.
func totalOf(order map[string]any) int64 {
	var sum int64
	items, _ := order["items"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		sum += num(m["quantity"]) * num(m["unitPriceCents"])
	}
	return sum
}

func environment(t *testing.T) (base, staffEmail, staffPassword string) {
	t.Helper()
	base = strings.TrimRight(os.Getenv("SWIFTDROP_E2E_URL"), "/")
	if base == "" {
		t.Skip("SWIFTDROP_E2E_URL not set; skipping the end-to-end test")
	}
	staffEmail, staffPassword = os.Getenv("SWIFTDROP_E2E_STAFF_EMAIL"), os.Getenv("SWIFTDROP_E2E_STAFF_PASSWORD")
	if staffEmail == "" || staffPassword == "" {
		t.Fatal("SWIFTDROP_E2E_STAFF_EMAIL and SWIFTDROP_E2E_STAFF_PASSWORD must name an existing staff account")
	}
	return base, staffEmail, staffPassword
}

func (s *session) order(id string, restaurant string, items []map[string]any) answer {
	return s.do("POST", "/orders", map[string]any{"id": id, "restaurantId": restaurant, "paymentType": "card", "items": items})
}

func (s *session) fetchOrder(id string) map[string]any {
	s.t.Helper()
	return s.want(s.do("GET", "/orders/"+id, nil), 200, "read order "+id).body
}

// TestACustomerOrdersAndTheOrderIsDelivered is the journey: staff set up a menu, a customer signs up,
// browses, orders (and cannot name a price), pays, the kitchen and the road do their parts, and the
// customer sees it delivered. Then the other ways it can go: a price change, a cancel with a refund,
// a closed restaurant, and what a customer may not touch.
func TestACustomerOrdersAndTheOrderIsDelivered(t *testing.T) {
	base, staffEmail, staffPassword := environment(t)
	run := unique()

	// -- staff set up the menu
	staff := newSession(t, base)
	staff.signIn(staffEmail, staffPassword)
	r := staff.want(staff.do("POST", "/restaurants", map[string]string{"name": "E2E Kitchen " + run}), 201, "add restaurant").body
	restaurantID := str(r["restaurant"].(map[string]any)["id"])
	addItem := func(name string, cents int64) string {
		a := staff.want(staff.do("POST", "/restaurants/"+restaurantID+"/menu", map[string]any{"name": name, "priceCents": cents}), 201, "add "+name)
		return str(a.body["item"].(map[string]any)["id"])
	}
	burger, fries := addItem("Smash burger", 950), addItem("Fries", 400)

	// -- a customer signs up and sees the menu
	customer := newSession(t, base)
	customer.signUp("e2e-"+run+"@example.test", "Eve Eetwell", "a long unique passphrase "+run)
	list := customer.want(customer.do("GET", "/restaurants", nil), 200, "list restaurants").body
	found := false
	for _, x := range list["restaurants"].([]any) {
		if str(x.(map[string]any)["id"]) == restaurantID {
			found = true
		}
	}
	if !found {
		t.Fatalf("the new restaurant is not offered to a customer: %v", list)
	}
	menu := customer.want(customer.do("GET", "/restaurants/"+restaurantID+"/menu", nil), 200, "read menu").body
	if items := menu["items"].([]any); len(items) != 2 {
		t.Fatalf("menu = %v, want two items", items)
	}

	// -- a customer cannot name a price, or order anything but the menu, or change the menu
	cheat := "E2E-" + run + "-cheat"
	customer.want(customer.order(cheat, restaurantID, []map[string]any{
		{"menuItemId": burger, "quantity": 2, "unitPriceCents": 1, "name": "Free burger", "sku": "FREE"},
	}), 201, "order with a made-up price")
	got := customer.fetchOrder(cheat)
	if total := totalOf(got); total != 1900 {
		t.Fatalf("a customer's own price was believed: total = %d, want 1900 (%v)", total, got["items"])
	}
	if name := str(got["items"].([]any)[0].(map[string]any)["name"]); name != "Smash burger" {
		t.Errorf("the line is called %q, want the menu's name", name)
	}
	customer.want(customer.order("E2E-"+run+"-custom", restaurantID, []map[string]any{
		{"sku": "X", "name": "Free lunch", "quantity": 1, "unitPriceCents": 0},
	}), 422, "a custom line from a customer")
	customer.want(customer.do("PATCH", "/menu/"+burger, map[string]any{"priceCents": 1}), 403, "a customer changing a price")
	customer.want(customer.do("POST", "/restaurants", map[string]string{"name": "Mine now"}), 403, "a customer adding a restaurant")

	// -- the journey
	id := cheat
	customer.want(customer.do("POST", "/orders/"+id+"/pay", nil), 200, "pay")
	if st := str(customer.fetchOrder(id)["state"]); st != "paid" {
		t.Fatalf("after paying the state is %q, want paid", st)
	}
	customer.want(customer.do("POST", "/orders/"+id+"/accept", nil), 403, "a customer accepting their own order")
	for _, step := range []string{"accept", "ready", "pickup", "deliver"} {
		staff.want(staff.do("POST", "/orders/"+id+"/"+step, nil), 200, step)
	}
	if st := str(customer.fetchOrder(id)["state"]); st != "delivered" {
		t.Fatalf("the customer sees %q, want delivered", st)
	}

	// -- a new price applies to new orders; the delivered one keeps its own
	staff.want(staff.do("PATCH", "/menu/"+burger, map[string]any{"priceCents": 1100}), 200, "change the price")
	if total := totalOf(customer.fetchOrder(id)); total != 1900 {
		t.Errorf("an old order changed with the menu: total = %d, want 1900", total)
	}
	again := "E2E-" + run + "-again"
	customer.want(customer.order(again, restaurantID, []map[string]any{{"menuItemId": burger, "quantity": 1}, {"menuItemId": fries, "quantity": 2}}), 201, "order at the new price")
	if total := totalOf(customer.fetchOrder(again)); total != 1100+800 {
		t.Errorf("new order total = %d, want %d", total, 1100+800)
	}

	// -- cancel after paying: the money comes back by itself
	customer.want(customer.do("POST", "/orders/"+again+"/pay", nil), 200, "pay the second order")
	customer.want(customer.do("POST", "/orders/"+again+"/cancel", nil), 200, "cancel")
	deadline := time.Now().Add(75 * time.Second)
	for {
		view := customer.fetchOrder(again)
		payment, _ := view["payment"].(map[string]any)
		if str(payment["refundStatus"]) == "refunded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the refund never finished: %v", view)
		}
		time.Sleep(time.Second)
	}

	// -- a closed restaurant takes no orders and is not shown
	staff.want(staff.do("PATCH", "/restaurants/"+restaurantID, map[string]any{"active": false}), 200, "close")
	customer.want(customer.do("GET", "/restaurants/"+restaurantID+"/menu", nil), 404, "the menu of a closed restaurant")
	closed := customer.order("E2E-"+run+"-closed", restaurantID, []map[string]any{{"menuItemId": fries, "quantity": 1}})
	if closed.status != 422 || !strings.Contains(closed.raw, "closed") {
		t.Errorf("ordering from a closed restaurant = %d %s, want 422 saying it is closed", closed.status, closed.raw)
	}
	list = customer.want(customer.do("GET", "/restaurants", nil), 200, "list restaurants again").body
	for _, x := range list["restaurants"].([]any) {
		if str(x.(map[string]any)["id"]) == restaurantID {
			t.Error("a closed restaurant is still offered to customers")
		}
	}

	// -- signing out ends the session
	customer.want(customer.do("POST", "/auth/logout", nil), 204, "sign out")
	customer.want(customer.do("GET", "/orders", nil), 401, "reading orders after signing out")
}
