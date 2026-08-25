package lookup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/tg"
	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	th "github.com/mymmrac/telego/telegohandler"
)

type lookupTelegramResult struct {
	messageID int
	err       error
}

type lookupTelegramCall struct {
	method string
	body   []byte
}

type lookupTelegramCaller struct {
	mu        sync.Mutex
	responses map[string][]lookupTelegramResult
	calls     []lookupTelegramCall
	deletes   chan telego.DeleteMessageParams
	nextID    int
}

func (c *lookupTelegramCaller) Call(_ context.Context, endpoint string, data *ta.RequestData) (*ta.Response, error) {
	method := endpoint[strings.LastIndexByte(endpoint, '/')+1:]
	body := append([]byte(nil), data.BodyRaw...)
	c.mu.Lock()
	c.calls = append(c.calls, lookupTelegramCall{method: method, body: body})
	var result lookupTelegramResult
	if queue := c.responses[method]; len(queue) != 0 {
		result = queue[0]
		c.responses[method] = queue[1:]
	}
	if result.messageID == 0 {
		c.nextID++
		result.messageID = 100 + c.nextID
	}
	c.mu.Unlock()
	if result.err != nil {
		return nil, result.err
	}
	var value any = true
	switch method {
	case "sendMessage", "sendRichMessage":
		value = &telego.Message{MessageID: result.messageID, Chat: telego.Chat{ID: -100}}
	case "deleteMessage":
		var params telego.DeleteMessageParams
		if err := json.Unmarshal(body, &params); err != nil {
			return nil, err
		}
		if c.deletes != nil {
			c.deletes <- params
		}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &ta.Response{Ok: true, Result: raw}, nil
}

func (c *lookupTelegramCaller) methodCalls(method string) []lookupTelegramCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	var calls []lookupTelegramCall
	for _, call := range c.calls {
		if call.method == method {
			calls = append(calls, call)
		}
	}
	return calls
}

func newLookupTestBot(t *testing.T, caller ta.Caller) *telego.Bot {
	t.Helper()
	bot, err := telego.NewBot("1:"+strings.Repeat("a", 35), telego.WithAPICaller(caller), telego.WithDiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	return bot
}

func runLookupHandler(t *testing.T, bot *telego.Bot, handler th.Handler, update telego.Update) {
	t.Helper()
	updates := make(chan telego.Update, 1)
	botHandler, err := th.NewBotHandler(bot, updates)
	if err != nil {
		t.Fatal(err)
	}
	handled := make(chan error, 1)
	botHandler.Handle(func(ctx *th.Context, update telego.Update) error {
		err := handler(ctx, update)
		handled <- err
		return err
	})
	started := make(chan error, 1)
	go func() { started <- botHandler.Start() }()
	updates <- update
	close(updates)
	if err := <-handled; err != nil {
		t.Fatalf("handler returned %v", err)
	}
	if err := <-started; err != nil {
		t.Fatalf("bot handler returned %v", err)
	}
}

func lookupMessage(text string, chatID, userID int64, chatType string) telego.Update {
	return telego.Update{Message: &telego.Message{
		MessageID: 41,
		Chat:      telego.Chat{ID: chatID, Type: chatType},
		From:      &telego.User{ID: userID, LanguageCode: "en"},
		Text:      text,
	}}
}

func sentLookupMessage(t *testing.T, caller *lookupTelegramCaller, index int) telego.SendMessageParams {
	t.Helper()
	calls := caller.methodCalls("sendMessage")
	if index >= len(calls) {
		t.Fatalf("sendMessage call %d missing; calls = %d", index, len(calls))
	}
	var params telego.SendMessageParams
	if err := json.Unmarshal(calls[index].body, &params); err != nil {
		t.Fatal(err)
	}
	return params
}

func withFreshNews(t *testing.T, items []NewsItem) {
	t.Helper()
	newsC.mu.Lock()
	oldItems, oldFetched, oldLoading := newsC.items, newsC.fetched, newsC.loading
	newsC.items = append([]NewsItem(nil), items...)
	newsC.fetched = time.Now()
	newsC.loading = false
	newsC.mu.Unlock()
	t.Cleanup(func() {
		newsC.mu.Lock()
		newsC.items, newsC.fetched, newsC.loading = oldItems, oldFetched, oldLoading
		newsC.mu.Unlock()
	})
}

func TestAllLookupCommandHandlersSendCatalogueAnswers(t *testing.T) {
	newsItems := []NewsItem{{Date: "2026-08-24", Title: "Kernel update", URL: "https://example.test/kernel"}}
	withFreshNews(t, newsItems)

	tests := []struct {
		name      string
		text      string
		handler   func(*Service) th.Handler
		want      string
		parseMode string
	}{
		{name: "pkg", text: "/pkg", handler: func(service *Service) th.Handler { return service.OnPkg }, want: i18n.Messages.LookupPackages.Pkg.Usage.For(i18n.LangEN)},
		{name: "use", text: "/use", handler: func(service *Service) th.Handler { return service.OnUse }, want: i18n.Messages.LookupPackages.Use.Usage.For(i18n.LangEN)},
		{name: "bug", text: "/bug", handler: func(service *Service) th.Handler { return service.OnBug }, want: i18n.Messages.LookupContent.Bug.Usage.For(i18n.LangEN)},
		{name: "news", text: "/news", handler: func(service *Service) th.Handler { return service.OnNews }, want: renderNews(i18n.LangEN, "", newsItems, true), parseMode: telego.ModeHTML},
		{name: "wiki", text: "/wiki", handler: func(service *Service) th.Handler { return service.OnWiki }, want: i18n.Messages.LookupContent.Wiki.Usage.For(i18n.LangEN)},
		{name: "bbs", text: "/bbs", handler: func(service *Service) th.Handler { return service.OnBbs }, want: i18n.Messages.LookupContent.BBS.Usage.For(i18n.LangEN)},
		{name: "pkgs", text: "/pkgs", handler: func(service *Service) th.Handler { return service.OnPkgs }, want: i18n.Messages.LookupDistros.Pkgs.Usage.For(i18n.LangEN)},
		{name: "distro alias", text: "/distro", handler: func(service *Service) th.Handler { return service.OnPkgs }, want: i18n.Messages.LookupDistros.Pkgs.Usage.For(i18n.LangEN)},
		{name: "arm", text: "/arm", handler: func(service *Service) th.Handler { return service.OnArm }, want: i18n.Messages.LookupPackages.Arm.Usage.For(i18n.LangEN)},
		{name: "armpkgs", text: "/armpkgs", handler: func(service *Service) th.Handler { return service.OnArmpkgs }, want: i18n.Messages.LookupDistros.Armpkgs.Usage.For(i18n.LangEN)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caller := &lookupTelegramCaller{}
			bot := newLookupTestBot(t, caller)
			service := New(nil, tg.New(bot), &config.Config{}, "")
			runLookupHandler(t, bot, tt.handler(service), lookupMessage(tt.text, 77, 77, telego.ChatTypePrivate))
			if got := len(caller.methodCalls("sendMessage")); got != 1 {
				t.Fatalf("sendMessage calls = %d, want 1", got)
			}
			params := sentLookupMessage(t, caller, 0)
			if params.Text != tt.want || params.ParseMode != tt.parseMode || params.ChatID.ID != 77 ||
				params.ReplyParameters == nil || params.ReplyParameters.MessageID != 41 {
				t.Fatalf("outbound answer = text %q parse_mode %q chat %d reply %+v; want catalogue text %q parse_mode %q chat 77 reply 41",
					params.Text, params.ParseMode, params.ChatID.ID, params.ReplyParameters, tt.want, tt.parseMode)
			}
		})
	}
}

type lookupRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn lookupRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func withLookupHTTP(t *testing.T, bodyFor func(*http.Request) (int, string)) {
	t.Helper()
	oldClient := httpClient
	httpClient = &http.Client{Transport: lookupRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		status, body := bodyFor(request)
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})}
	t.Cleanup(func() { httpClient = oldClient })
}

func resetLookupPackageCaches(t *testing.T) {
	t.Helper()
	oldOverlays := overlays
	pkgC.mu.Lock()
	oldPkgs, oldAvailable := pkgC.pkgs, pkgC.available
	oldFetched, oldLastAttempt, oldRefreshing := pkgC.fetched, pkgC.lastAttempt, pkgC.refreshing
	pkgC.pkgs = map[string]map[string]string{}
	pkgC.available = map[string]bool{}
	pkgC.fetched = time.Time{}
	pkgC.lastAttempt = time.Time{}
	pkgC.refreshing = false
	pkgC.mu.Unlock()
	verC.mu.Lock()
	oldVer := verC.m
	verC.m = map[string]verInfo{}
	verC.mu.Unlock()
	overlays = nil
	t.Cleanup(func() {
		overlays = oldOverlays
		pkgC.mu.Lock()
		pkgC.pkgs = oldPkgs
		pkgC.available = oldAvailable
		pkgC.fetched = oldFetched
		pkgC.lastAttempt = oldLastAttempt
		pkgC.refreshing = oldRefreshing
		pkgC.mu.Unlock()
		verC.mu.Lock()
		verC.m = oldVer
		verC.mu.Unlock()
	})
}

func TestLookupHandlerArgumentsAndTelegramFallbacks(t *testing.T) {
	t.Run("missing update data is ignored", func(t *testing.T) {
		caller := &lookupTelegramCaller{}
		bot := newLookupTestBot(t, caller)
		service := New(nil, tg.New(bot), &config.Config{}, "")
		runLookupHandler(t, bot, service.OnPkg, telego.Update{})
		withoutSender := lookupMessage("/pkg vim", 77, 77, telego.ChatTypePrivate)
		withoutSender.Message.From = nil
		runLookupHandler(t, bot, service.OnPkg, withoutSender)
		if got := len(caller.methodCalls("sendMessage")); got != 0 {
			t.Fatalf("missing message or sender produced %d replies", got)
		}
	})

	t.Run("extra bug argument is rejected from the catalogue", func(t *testing.T) {
		caller := &lookupTelegramCaller{}
		bot := newLookupTestBot(t, caller)
		service := New(nil, tg.New(bot), &config.Config{}, "")
		runLookupHandler(t, bot, service.OnBug, lookupMessage("/bug 123 extra", 77, 77, telego.ChatTypePrivate))
		want := i18n.Messages.LookupContent.Bug.Usage.For(i18n.LangEN)
		if got := sentLookupMessage(t, caller, 0).Text; got != want {
			t.Fatalf("extra-argument answer = %q, want catalogue usage %q", got, want)
		}
	})

	t.Run("quoted news query reaches the renderer intact", func(t *testing.T) {
		items := []NewsItem{{Date: "2026-08-24", Title: "Kernel update", URL: "https://example.test/kernel"}}
		withFreshNews(t, items)
		caller := &lookupTelegramCaller{}
		bot := newLookupTestBot(t, caller)
		service := New(nil, tg.New(bot), &config.Config{}, "")
		const arg = `"kernel update"`
		runLookupHandler(t, bot, service.OnNews, lookupMessage("/news "+arg, 77, 77, telego.ChatTypePrivate))
		want := renderNews(i18n.LangEN, arg, items, true)
		if got := sentLookupMessage(t, caller, 0); got.Text != want || got.ParseMode != telego.ModeHTML {
			t.Fatalf("quoted-query answer = text %q parse_mode %q, want catalogue rendering %q in HTML", got.Text, got.ParseMode, want)
		}
	})

	for _, tt := range []struct {
		name  string
		query string
		err   error
	}{
		{name: "category atom and oversized rich message", query: "app-editors/vim", err: errors.New("Bad Request: message is too long")},
		{name: "bare atom and rejected rich HTML", query: "vim", err: errors.New("Bad Request: can't parse entities")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resetLookupPackageCaches(t)
			withLookupHTTP(t, func(request *http.Request) (int, string) {
				if request.URL.Host == "api.github.com" {
					return http.StatusOK, `{"tree":[]}`
				}
				if strings.HasSuffix(request.URL.Path, ".json") {
					return http.StatusOK, `{"versions":[]}`
				}
				return http.StatusOK, "<html></html>"
			})
			caller := &lookupTelegramCaller{responses: map[string][]lookupTelegramResult{
				"sendRichMessage": {{err: tt.err}},
			}}
			bot := newLookupTestBot(t, caller)
			service := New(nil, tg.New(bot), &config.Config{
				RichMessages: true,
				Overlays:     []config.OverlayCfg{{Name: "test", Repo: "test/repo"}},
			}, "")
			runLookupHandler(t, bot, service.OnPkg, lookupMessage("/pkg "+tt.query, 77, 77, telego.ChatTypePrivate))
			if got := len(caller.methodCalls("sendRichMessage")); got != 1 {
				t.Fatalf("sendRichMessage calls = %d, want 1", got)
			}
			if got := len(caller.methodCalls("sendMessage")); got != 1 {
				t.Fatalf("HTML fallback calls = %d, want 1", got)
			}
			availability := pkgLookupAvailability{official: true, overlays: map[string]bool{"test": true}}
			want := renderPkg(i18n.LangEN, tt.query, nil, map[string][2]string{}, map[string][]string{}, availability)
			params := sentLookupMessage(t, caller, 0)
			if params.Text != want || params.ParseMode != telego.ModeHTML {
				t.Fatalf("HTML fallback = text %q parse_mode %q, want catalogue rendering %q in HTML", params.Text, params.ParseMode, want)
			}
		})
	}

	for _, sendErr := range []error{
		errors.New("Bad Request: message is too long"),
		errors.New("Bad Request: can't parse entities"),
	} {
		t.Run("bbs removes buttons after "+sendErr.Error(), func(t *testing.T) {
			withLookupHTTP(t, func(*http.Request) (int, string) { return http.StatusOK, `{}` })
			caller := &lookupTelegramCaller{responses: map[string][]lookupTelegramResult{
				"sendMessage": {{err: sendErr}, {}},
			}}
			bot := newLookupTestBot(t, caller)
			service := New(nil, tg.New(bot), &config.Config{}, "")
			const query = "kernel modules"
			runLookupHandler(t, bot, service.OnBbs, lookupMessage("/bbs "+query, 77, 77, telego.ChatTypePrivate))
			calls := caller.methodCalls("sendMessage")
			if len(calls) != 2 {
				t.Fatalf("sendMessage calls = %d, want button send and text-only fallback", len(calls))
			}
			want := i18n.Messages.LookupContent.BBS.Heading.Render(i18n.LangEN, query) +
				i18n.Messages.LookupContent.BBS.ArchCNNoMatches.For(i18n.LangEN) +
				i18n.Messages.LookupContent.BBS.OtherForums.For(i18n.LangEN)
			var first, fallback struct {
				Text        string          `json:"text"`
				ParseMode   string          `json:"parse_mode"`
				ReplyMarkup json.RawMessage `json:"reply_markup"`
			}
			if err := json.Unmarshal(calls[0].body, &first); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(calls[1].body, &fallback); err != nil {
				t.Fatal(err)
			}
			if first.Text != want || len(first.ReplyMarkup) == 0 || string(first.ReplyMarkup) == "null" ||
				fallback.Text != want || len(fallback.ReplyMarkup) != 0 || fallback.ParseMode != telego.ModeHTML {
				t.Fatalf("BBS send/fallback = first %+v fallback %+v, want catalogue HTML %q with buttons then without", first, fallback, want)
			}
		})
	}
}

func TestLookupHandlerAuthorizationRateAndScheduledCleanup(t *testing.T) {
	const (
		groupID = int64(-1007000000001)
		userID  = int64(88)
	)
	limit := 1
	disabledTTL := 0
	cfg := &config.Config{
		Groups:             []config.GroupConfig{{ID: groupID, Lang: "en"}},
		GroupIDs:           []int64{groupID},
		ControlGroupID:     groupID,
		PrivateQueryPerMin: limit,
		LookupTTLSeconds:   &disabledTTL,
	}
	caller := &lookupTelegramCaller{}
	bot := newLookupTestBot(t, caller)
	service := New(nil, tg.New(bot), cfg, "")

	runLookupHandler(t, bot, service.OnWiki, lookupMessage("/wiki", 900, userID, telego.ChatTypeSupergroup))
	if got := len(caller.methodCalls("sendMessage")); got != 0 {
		t.Fatalf("unguarded group produced %d replies", got)
	}

	runLookupHandler(t, bot, service.OnWiki, lookupMessage("/wiki", userID, userID, telego.ChatTypePrivate))
	runLookupHandler(t, bot, service.OnWiki, lookupMessage("/wiki", userID, userID, telego.ChatTypePrivate))
	if got := len(caller.methodCalls("sendMessage")); got != 2 {
		t.Fatalf("private lookup sends = %d, want usage then rate-limit notice", got)
	}
	if got, want := sentLookupMessage(t, caller, 0).Text, i18n.Messages.LookupContent.Wiki.Usage.For(i18n.LangEN); got != want {
		t.Fatalf("first private lookup = %q, want catalogue usage %q", got, want)
	}
	if got, want := sentLookupMessage(t, caller, 1).Text,
		i18n.Messages.LookupContent.Transport.PrivateRateLimited.Render(i18n.LangEN, limit); got != want {
		t.Fatalf("limited private lookup = %q, want catalogue notice %q", got, want)
	}

	beforeGroup := len(caller.methodCalls("sendMessage"))
	runLookupHandler(t, bot, service.OnWiki, lookupMessage("/wiki", groupID, userID, telego.ChatTypeSupergroup))
	runLookupHandler(t, bot, service.OnWiki, lookupMessage("/wiki", groupID, userID, telego.ChatTypeSupergroup))
	if got := len(caller.methodCalls("sendMessage")) - beforeGroup; got != 2 {
		t.Fatalf("guarded-group lookup sends = %d, want 2 rate-limit-exempt replies", got)
	}
	groupReplies := caller.methodCalls("sendMessage")[beforeGroup:]
	var firstGroup telego.SendMessageParams
	if err := json.Unmarshal(groupReplies[0].body, &firstGroup); err != nil {
		t.Fatal(err)
	}
	if firstGroup.Text != i18n.Messages.LookupContent.Wiki.Usage.For(i18n.LangEN) {
		t.Fatalf("group answer = %q, want catalogue usage", firstGroup.Text)
	}

	ttl := 1
	cleanupCaller := &lookupTelegramCaller{deletes: make(chan telego.DeleteMessageParams, 2)}
	cleanupBot := newLookupTestBot(t, cleanupCaller)
	cleanupService := New(nil, tg.New(cleanupBot), &config.Config{
		Groups:           []config.GroupConfig{{ID: groupID, Lang: "en"}},
		GroupIDs:         []int64{groupID},
		ControlGroupID:   groupID,
		LookupTTLSeconds: &ttl,
	}, "")
	runLookupHandler(t, cleanupBot, cleanupService.OnWiki, lookupMessage("/wiki", groupID, userID, telego.ChatTypeSupergroup))

	var deletes []telego.DeleteMessageParams
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for len(deletes) < 2 {
		select {
		case params := <-cleanupCaller.deletes:
			deletes = append(deletes, params)
		case <-deadline.C:
			t.Fatalf("scheduled cleanup calls = %+v, want response and command deletion", deletes)
		}
	}
	wantDeleteIDs := []int{101, 41}
	gotDeleteIDs := []int{deletes[0].MessageID, deletes[1].MessageID}
	if !reflect.DeepEqual(gotDeleteIDs, wantDeleteIDs) {
		t.Fatalf("scheduled cleanup message IDs = %v, want response then command %v", gotDeleteIDs, wantDeleteIDs)
	}
	for _, deletion := range deletes {
		if deletion.ChatID.ID != groupID {
			t.Fatalf("scheduled cleanup chat = %d, want %d", deletion.ChatID.ID, groupID)
		}
	}
}
