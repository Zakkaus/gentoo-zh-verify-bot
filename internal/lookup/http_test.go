package lookup

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/mymmrac/telego"
)

func TestRequesterLanguageFallbackChain(t *testing.T) {
	const groupID int64 = -100
	service := New(nil, nil, &config.Config{
		Groups:   []config.GroupConfig{{ID: groupID, Lang: "zh-Hant"}},
		GroupIDs: []int64{groupID},
	}, "")
	tests := []struct {
		name     string
		chat     telego.Chat
		code     string
		expected i18n.Lang
	}{
		{name: "requester overrides group", chat: telego.Chat{ID: groupID, Type: "supergroup"}, code: "en", expected: i18n.LangEN},
		{name: "supported Chinese overrides group", chat: telego.Chat{ID: groupID, Type: "supergroup"}, code: "zh-CN", expected: i18n.LangZH},
		{name: "unsupported falls back to group", chat: telego.Chat{ID: groupID, Type: "supergroup"}, code: "fr", expected: i18n.LangZHHant},
		{name: "missing falls back to group", chat: telego.Chat{ID: groupID, Type: "supergroup"}, expected: i18n.LangZHHant},
		{name: "unsupported DM falls back to English", chat: telego.Chat{ID: 7, Type: "private"}, code: "fr", expected: i18n.LangEN},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			msg := &telego.Message{Chat: test.chat, From: &telego.User{ID: 7, LanguageCode: test.code}}
			if got := service.requesterLanguage(msg); got != test.expected {
				t.Fatalf("requester language = %s, want %s", got, test.expected)
			}
		})
	}
}

func TestRuntimeRegisteredGroupUsesLiveMembership(t *testing.T) {
	const groupID int64 = -1009000000401
	cfg := &config.Config{Lang: "zh-Hant"}
	baseline, err := store.LoadBaseline(filepath.Join(t.TempDir(), "missing-config.json"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := store.NewSettings(filepath.Join(t.TempDir(), "settings.json"), baseline)
	if err != nil {
		t.Fatal(err)
	}
	service := New(settings, nil, cfg, "")
	registration := settings.Registrations()
	registration.RegisteredGroups = []store.RegisteredGroup{{ID: groupID, RegisteredBy: 42}}
	if _, err := settings.CommitRegistrations(registration.Revision, registration); err != nil {
		t.Fatal(err)
	}
	msg := &telego.Message{
		Chat: telego.Chat{ID: groupID, Type: telego.ChatTypeSupergroup},
		From: &telego.User{ID: 7, LanguageCode: "fr"},
	}
	if got := service.requesterLanguage(msg); got != i18n.LangZHHant {
		t.Errorf("runtime group requester language = %s, want %s", got, i18n.LangZHHant)
	}
	if !service.queryAllowed(nil, msg, i18n.LangZHHant) {
		t.Error("runtime group lookup was not exempted from the private-query rate limit")
	}
	if got := service.controlGroupID(); got != groupID {
		t.Errorf("runtime control group = %d, want %d", got, groupID)
	}
}

func TestHTTPStatusCode(t *testing.T) {
	if got := httpStatusCode(&httpStatusError{url: "u", code: 404}); got != 404 {
		t.Errorf("httpStatusCode(404) = %d, want 404", got)
	}
	if got := httpStatusCode(&httpStatusError{url: "u", code: 503}); got != 503 {
		t.Errorf("httpStatusCode(503) = %d, want 503", got)
	}
	if got := httpStatusCode(errors.New("context deadline exceeded")); got != 0 {
		t.Errorf("a non-HTTP (timeout/network) error must report 0, got %d", got)
	}
	if got := httpStatusCode(nil); got != 0 {
		t.Errorf("a nil error must report 0, got %d", got)
	}
}

func TestHTTPGetBodyLimit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		limit    int64
		tooLarge bool
	}{
		{name: "below limit", body: "ab", limit: 3},
		{name: "exact limit", body: "abc", limit: 3},
		{name: "one byte over", body: "abcd", limit: 3, tooLarge: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			got, err := httpGetBody(context.Background(), srv.URL, tc.limit)
			var tooLarge *httpBodyTooLargeError
			if errors.As(err, &tooLarge) != tc.tooLarge {
				t.Fatalf("httpGetBody() error = %v, want body-too-large=%v", err, tc.tooLarge)
			}
			if !tc.tooLarge && string(got) != tc.body {
				t.Errorf("httpGetBody() = %q, want %q", got, tc.body)
			}
			if tc.tooLarge && got != nil {
				t.Errorf("oversized response returned a parser-visible prefix %q", got)
			}
		})
	}
}

func TestHTTPGetStatusFromServer(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(code)
			}))
			defer srv.Close()

			_, err := httpGet(context.Background(), srv.URL, nil)
			if got := httpStatusCode(err); got != code {
				t.Errorf("httpStatusCode(httpGet()) = %d, want %d (error %v)", got, code, err)
			}
		})
	}
}

func TestAcquireHTTPSlotBusy(t *testing.T) {
	sem := make(chan struct{}, 1)
	sem <- struct{}{}
	err := acquireHTTPSlot(context.Background(), "https://example.invalid", sem, time.Millisecond)
	var busy *httpBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("acquireHTTPSlot() error = %v, want *httpBusyError", err)
	}
}

func TestPrivateQueryRate(t *testing.T) {
	service := New(nil, nil, &config.Config{PrivateQueryPerMin: 3}, "")
	pass := 0
	for range 5 {
		if service.queryRateOK(7) {
			pass++
		}
	}
	if pass != 3 {
		t.Errorf("user 7: %d/5 allowed, want 3", pass)
	}
	if !service.queryRateOK(8) {
		t.Error("user 8 should be allowed")
	}
}
