package domain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestAnAlertSaysWhenItWasSentOutOnlyOnceItWas(t *testing.T) {
	a := Alert{ID: 1, Kind: AlertRefundGivenUp, Subject: "pay-1", Message: "m", Occurrences: 1}
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "notifiedAt") {
		t.Errorf("an alert never sent shows notifiedAt: %s", raw)
	}

	at := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	a.NotifiedAt = &at
	raw, _ = json.Marshal(a)
	if !strings.Contains(string(raw), `"notifiedAt":"2026-11-07T09:00:00Z"`) {
		t.Errorf("alert = %s", raw)
	}
}

func TestEveryAlertKindHasATitleAndAnUnknownOneStillReads(t *testing.T) {
	titles := map[string]bool{}
	for _, kind := range AlertKinds {
		title := AlertTitle(kind)
		if title == "" || title == strings.ReplaceAll(kind, "_", " ") {
			t.Errorf("kind %q has no title of its own: %q", kind, title)
		}
		if titles[title] {
			t.Errorf("title %q is shared", title)
		}
		titles[title] = true
	}
	if got := AlertTitle("something_new"); got != "something new" {
		t.Errorf("an unknown kind reads as %q", got)
	}
	if got := AlertTitle(""); got != "Needs a look" {
		t.Errorf("an empty kind reads as %q", got)
	}
}

// AlertKinds is what the title test walks, so it must hold every kind there is.
func TestAlertKindsListsEveryKind(t *testing.T) {
	all := []string{
		AlertUnrecordedCharge, AlertContradictingEvent, AlertRefundGivenUp,
		AlertRefundUnrecorded, AlertPaymentNeedsReview, AlertRefundReversed,
	}
	if len(AlertKinds) != len(all) {
		t.Fatalf("AlertKinds has %d kinds, want %d", len(AlertKinds), len(all))
	}
	for _, want := range all {
		found := false
		for _, k := range AlertKinds {
			found = found || k == want
		}
		if !found {
			t.Errorf("AlertKinds lacks %q", want)
		}
	}
}

// The dashboard words each kind in logic.js, and a message says it in Go. They are two
// copies of the same sentences, so they must not drift apart.
func TestTheTitlesMatchTheOnesTheDashboardShows(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "web", "ui", "src", "lib", "logic.js"))
	if err != nil {
		t.Fatal(err)
	}
	found := regexp.MustCompile(`(?s)(\w+): \{\s*title: '([^']+)'`).FindAllStringSubmatch(string(src), -1)
	if len(found) < len(AlertKinds) {
		t.Fatalf("found %d titles in logic.js, want at least %d (did its layout change?)", len(found), len(AlertKinds))
	}
	js := map[string]string{}
	for _, m := range found {
		js[m[1]] = m[2]
	}
	for _, kind := range AlertKinds {
		if got, want := AlertTitle(kind), js[kind]; got != want {
			t.Errorf("%s: the message says %q, the dashboard says %q", kind, got, want)
		}
	}
}
