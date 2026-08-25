package bot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/lookup"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/verify"
)

const testLookupGroup int64 = -1

type testLookupApplication struct {
	cfg      *config.Config
	settings *store.Settings
	verifier *verify.Service
}

func newTestApplication(t *testing.T, ttl *int) *testLookupApplication {
	cfg := &config.Config{GroupIDs: []int64{testLookupGroup},
		Questions:        []config.Question{{Q: "x", Options: []string{"a", "b"}, Answer: 0}},
		LookupTTLSeconds: ttl}
	settings, err := store.NewSettings("", botTestSettingsBaseline(t, cfg))
	if err != nil {
		panic(err)
	}
	verifier := verify.New(settings, nil, cfg, &i18n.Messages, nil, verify.Identity{}, "")
	return &testLookupApplication{cfg: cfg, settings: settings, verifier: verifier}
}

func testLookupService(v *testLookupApplication) *lookup.Service {
	return lookup.New(v.settings, nil, v.cfg, "")
}

func TestLookupAutoDelete(t *testing.T) {
	// default: unset => enabled, 3 minutes
	if ttl, on := testLookupService(newTestApplication(t, nil)).AutoDelete(testLookupGroup); !on || ttl != 3*time.Minute {
		t.Errorf("default = (%v, %v), want (3m, true)", ttl, on)
	}
	// config 0 => disabled
	zero := 0
	disabled := newTestApplication(t, &zero)
	if _, on := testLookupService(disabled).AutoDelete(testLookupGroup); on {
		t.Errorf("lookup_ttl_seconds=0 should disable")
	}
	if err := disabled.verifier.SetAutoDelete(testLookupGroup, 0, true); err != nil {
		t.Fatal(err)
	}
	if ttl, on := testLookupService(disabled).AutoDelete(testLookupGroup); !on || ttl != 3*time.Minute {
		t.Errorf("re-enabled zero baseline = (%v, %v), want (3m, true)", ttl, on)
	}
	// config positive => enabled with that duration
	seconds := 600
	if ttl, on := testLookupService(newTestApplication(t, &seconds)).AutoDelete(testLookupGroup); !on || ttl != 10*time.Minute {
		t.Errorf("lookup_ttl_seconds=600 = (%v, %v), want (10m, true)", ttl, on)
	}
	// runtime: set minutes, then disable — the TTL must persist for a later re-enable
	v := newTestApplication(t, nil)
	if err := v.verifier.SetAutoDelete(testLookupGroup, 5*time.Minute, true); err != nil {
		t.Fatal(err)
	}
	if err := v.verifier.SetAutoDelete(testLookupGroup, 0, false); err != nil {
		t.Fatal(err)
	}
	if ttl, on := testLookupService(v).AutoDelete(testLookupGroup); on || ttl != 5*time.Minute {
		t.Errorf("after off = (%v, %v), want (5m, false)", ttl, on)
	}
}

func botTestSettingsBaseline(t *testing.T, cfg *config.Config) store.SettingsBaseline {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	baseline, err := store.LoadBaseline(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return baseline
}

func TestDistroAliasVisible(t *testing.T) {
	var description string
	for _, command := range memberCommands(i18n.LangZH) {
		if command.Command == "distro" {
			description = command.Description
			break
		}
	}
	want := i18n.Messages.Bot.Menu.Member.Distro.For(i18n.LangZH)
	if description != want {
		t.Errorf("/distro menu description = %q, want catalogue text %q", description, want)
	}
	if !strings.Contains(description, "/pkgs") {
		t.Errorf("/distro menu description = %q, want an explicit /pkgs alias", description)
	}
}

func TestBuiltInPrivateReplyUsesCatalogue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{"groups":[{"id":-1001,"verify_mode":"kernel"}]}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !isBuiltInPrivateReply(cfg.PrivateReply) {
		t.Fatal("normalized built-in private reply was not recognized")
	}
	handler := dmHandler{cfg: cfg, catalogueReply: true}
	for _, language := range i18n.Languages() {
		got := handler.privateReply(language)
		want := i18n.Messages.Bot.DirectMessage.AutoReply.Render(language, cfg.PrivateQueryPerMin)
		if got != want {
			t.Errorf("private reply for %s = %q, want catalogue text %q", language, got, want)
		}
	}

	const customReply = "Contact a group administrator."
	if isBuiltInPrivateReply(customReply) {
		t.Fatal("custom private reply was recognized as built-in")
	}
	handler = dmHandler{cfg: &config.Config{PrivateReply: customReply}}
	if got := handler.privateReply(i18n.LangZHHant); got != customReply {
		t.Errorf("custom private reply = %q, want %q", got, customReply)
	}
}

func TestBuiltInPrivateReplyUsesLiveQueryRate(t *testing.T) {
	cfg := &config.Config{PrivateQueryPerMin: 3}
	settings, err := store.NewSettings("", botTestSettingsBaseline(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	service := New(cfg, settings, nil, nil, nil, nil, nil)
	global := settings.Global()
	overrides := global.Overrides()
	rate := 5
	overrides.PrivateQueryPerMin = &rate
	if _, err := settings.CommitGlobal(global.Revision(), overrides); err != nil {
		t.Fatal(err)
	}
	got := service.dm.privateReply(i18n.LangEN)
	want := i18n.Messages.Bot.DirectMessage.AutoReply.Render(i18n.LangEN, rate)
	if got != want {
		t.Errorf("private reply = %q, want catalogue text %q", got, want)
	}
}
