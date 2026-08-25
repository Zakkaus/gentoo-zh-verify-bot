package tg

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
)

type scriptedResult struct {
	value any
	err   error
}

type recordedCall struct {
	method string
	body   []byte
}

type scriptedCaller struct {
	mu        sync.Mutex
	responses map[string][]scriptedResult
	calls     []recordedCall
}

func (c *scriptedCaller) Call(_ context.Context, url string, data *ta.RequestData) (*ta.Response, error) {
	method := url[strings.LastIndexByte(url, '/')+1:]
	body := append([]byte(nil), data.BodyRaw...)
	c.mu.Lock()
	c.calls = append(c.calls, recordedCall{method: method, body: body})
	var result scriptedResult
	if queue := c.responses[method]; len(queue) > 0 {
		result = queue[0]
		c.responses[method] = queue[1:]
	}
	c.mu.Unlock()
	if result.err != nil {
		return nil, result.err
	}
	if result.value == nil {
		switch method {
		case "sendMessage", "sendRichMessage", "editMessageText":
			result.value = &telego.Message{MessageID: 101, Chat: telego.Chat{ID: -100}}
		default:
			result.value = true
		}
	}
	raw, err := json.Marshal(result.value)
	if err != nil {
		return nil, err
	}
	return &ta.Response{Ok: true, Result: raw}, nil
}

func (c *scriptedCaller) methodCalls(method string) []recordedCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	var calls []recordedCall
	for _, call := range c.calls {
		if call.method == method {
			calls = append(calls, call)
		}
	}
	return calls
}

func newTestClient(t *testing.T, caller *scriptedCaller) *Client {
	t.Helper()
	if caller.responses == nil {
		caller.responses = make(map[string][]scriptedResult)
	}
	bot, err := telego.NewBot("1:"+strings.Repeat("a", 35), telego.WithAPICaller(caller), telego.WithDiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	return New(bot)
}

func TestSendHTMLFallback(t *testing.T) {
	parseErr := errors.New("Bad Request: can't parse entities")
	tests := []struct {
		name      string
		responses []scriptedResult
		wantOK    bool
		wantCalls int
		wantPlain bool
	}{
		{name: "successful rich HTML stays one send", wantOK: true, wantCalls: 1},
		{name: "simpler HTML succeeds", responses: []scriptedResult{{err: parseErr}, {}}, wantOK: true, wantCalls: 2},
		{name: "plain text succeeds", responses: []scriptedResult{{err: parseErr}, {err: parseErr}, {}}, wantOK: true, wantCalls: 3, wantPlain: true},
		{name: "transient error is not retried", responses: []scriptedResult{{err: errors.New("Bad Gateway")}}, wantOK: false, wantCalls: 1},
		{name: "all renderings fail", responses: []scriptedResult{{err: parseErr}, {err: parseErr}, {err: parseErr}}, wantOK: false, wantCalls: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caller := &scriptedCaller{responses: map[string][]scriptedResult{"sendMessage": tt.responses}}
			client := newTestClient(t, caller)
			sent, err := client.SendHTMLFallback(context.Background(), 5,
				`<blockquote expandable><b>rich</b></blockquote>`, `<b>simple &amp; safe</b>`)
			ok := sent != nil
			if ok != tt.wantOK || (err == nil) != tt.wantOK {
				t.Fatalf("SendHTMLFallback() = (%v, %v), want success %v", sent, err, tt.wantOK)
			}
			if sent != nil && sent.MessageID != 101 {
				t.Errorf("sent message ID = %d, want 101", sent.MessageID)
			}
			calls := caller.methodCalls("sendMessage")
			if len(calls) != tt.wantCalls {
				t.Fatalf("sendMessage calls = %d, want %d", len(calls), tt.wantCalls)
			}
			if tt.wantPlain {
				var params telego.SendMessageParams
				if err := json.Unmarshal(calls[len(calls)-1].body, &params); err != nil {
					t.Fatal(err)
				}
				if params.ParseMode != "" || params.Text != "simple & safe" {
					t.Errorf("plain fallback = parse_mode %q text %q", params.ParseMode, params.Text)
				}
			}
		})
	}
}

func TestSendPrivateHTMLFallback(t *testing.T) {
	caller := &scriptedCaller{responses: map[string][]scriptedResult{
		"sendMessage": {{err: errors.New("network")}, {}},
	}}
	newTestClient(t, caller).SendPrivateHTMLFallback(context.Background(), 7, "<b>reply</b>")
	calls := caller.methodCalls("sendMessage")
	if len(calls) != 2 {
		t.Fatalf("sendMessage calls = %d, want 2", len(calls))
	}
	var fallback telego.SendMessageParams
	if err := json.Unmarshal(calls[1].body, &fallback); err != nil {
		t.Fatal(err)
	}
	if fallback.ParseMode != "" || fallback.Text != "<b>reply</b>" {
		t.Errorf("plain retry = parse_mode %q text %q", fallback.ParseMode, fallback.Text)
	}
}

func TestSendRichOrHTMLFallback(t *testing.T) {
	successCaller := &scriptedCaller{}
	newTestClient(t, successCaller).SendRichOrHTML(context.Background(), -100, 44, "<b>rich</b>", "<b>plain</b>", true, 0)
	if got := len(successCaller.methodCalls("sendRichMessage")); got != 1 {
		t.Fatalf("successful rich send calls = %d, want 1", got)
	}
	if got := len(successCaller.methodCalls("sendMessage")); got != 0 {
		t.Fatalf("successful rich send made %d HTML fallback calls, want 0", got)
	}

	caller := &scriptedCaller{responses: map[string][]scriptedResult{
		"sendRichMessage": {{err: errors.New("rich rejected")}},
	}}
	client := newTestClient(t, caller)
	client.SendRichOrHTML(context.Background(), -100, 44, "<b>rich</b>", "<b>plain</b>", true, 0)
	if got := len(caller.methodCalls("sendRichMessage")); got != 1 {
		t.Fatalf("sendRichMessage calls = %d, want 1", got)
	}
	calls := caller.methodCalls("sendMessage")
	if len(calls) != 1 {
		t.Fatalf("sendMessage calls = %d, want 1", len(calls))
	}
	var params telego.SendMessageParams
	if err := json.Unmarshal(calls[0].body, &params); err != nil {
		t.Fatal(err)
	}
	if params.ParseMode != telego.ModeHTML || params.ReplyParameters == nil || params.ReplyParameters.MessageID != 44 {
		t.Errorf("HTML fallback lost parse mode or reply binding: %+v", params)
	}
}

func TestReplyCleanupAndNotifyBound(t *testing.T) {
	t.Run("lookup response then command", func(t *testing.T) {
		caller := &scriptedCaller{}
		client := newTestClient(t, caller)
		client.ReplyPlain(context.Background(), -100, 9, "reply", time.Millisecond)
		waitForMethodCalls(t, caller, "deleteMessage", 2)
		waitForCleanupTimerCount(t, client, 0)
		calls := caller.methodCalls("deleteMessage")
		var first, second telego.DeleteMessageParams
		if err := json.Unmarshal(calls[0].body, &first); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(calls[1].body, &second); err != nil {
			t.Fatal(err)
		}
		if first.MessageID != 101 || second.MessageID != 9 {
			t.Errorf("delete order = %d, %d; want response 101 then command 9", first.MessageID, second.MessageID)
		}
	})

	t.Run("lookup cleanup respects capacity", func(t *testing.T) {
		caller := &scriptedCaller{}
		client := newTestClient(t, caller)
		client.cleanupTimers.Store(cleanupTimerMax)
		client.ScheduleCleanup(-100, 9, 101, time.Millisecond)
		time.Sleep(10 * time.Millisecond)
		if got := len(caller.methodCalls("deleteMessage")); got != 0 {
			t.Fatalf("deleteMessage calls = %d, want 0", got)
		}
		if got := client.cleanupTimers.Load(); got != cleanupTimerMax {
			t.Fatalf("cleanup timer count = %d, want %d", got, cleanupTimerMax)
		}
	})

	tests := []struct {
		name       string
		prefill    int32
		wantDelete bool
	}{
		{name: "below capacity", prefill: cleanupTimerMax - 1, wantDelete: true},
		{name: "at capacity", prefill: cleanupTimerMax, wantDelete: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caller := &scriptedCaller{}
			client := newTestClient(t, caller)
			client.cleanupTimers.Store(tt.prefill)
			client.Notify(context.Background(), -100, "notice", 0)
			if tt.wantDelete {
				waitForMethodCalls(t, caller, "deleteMessage", 1)
				waitForCleanupTimerCount(t, client, tt.prefill)
			} else {
				time.Sleep(10 * time.Millisecond)
				if got := len(caller.methodCalls("deleteMessage")); got != 0 {
					t.Fatalf("deleteMessage calls = %d, want 0", got)
				}
			}
			if got := client.cleanupTimers.Load(); got != tt.prefill {
				t.Fatalf("cleanup timer count = %d, want %d", got, tt.prefill)
			}
		})
	}
}

func waitForCleanupTimerCount(t *testing.T, client *Client, want int32) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if client.cleanupTimers.Load() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("cleanup timer count = %d, want %d", client.cleanupTimers.Load(), want)
}

func TestAlertsUseConfiguredOrFallbackChat(t *testing.T) {
	caller := &scriptedCaller{}
	client := newTestClient(t, caller)
	client.Alert(context.Background(), 0, "disabled")
	client.Alert(context.Background(), -200, "admin")
	client.FailAlert(context.Background(), 0, -100, "fallback")
	client.FailAlert(context.Background(), -200, -100, "configured")
	calls := caller.methodCalls("sendMessage")
	if len(calls) != 3 {
		t.Fatalf("sendMessage calls = %d, want 3", len(calls))
	}
	wantChats := []int64{-200, -100, -200}
	for i, call := range calls {
		var params telego.SendMessageParams
		if err := json.Unmarshal(call.body, &params); err != nil {
			t.Fatal(err)
		}
		if params.ChatID.ID != wantChats[i] {
			t.Errorf("call %d chat = %d, want %d", i, params.ChatID.ID, wantChats[i])
		}
	}
}

func TestAdminCacheAndFreshLookup(t *testing.T) {
	admin := &telego.ChatMemberAdministrator{Status: telego.MemberStatusAdministrator}
	member := &telego.ChatMemberMember{Status: telego.MemberStatusMember}
	caller := &scriptedCaller{responses: map[string][]scriptedResult{
		"getChatMember": {{value: admin}, {value: member}},
	}}
	client := newTestClient(t, caller)
	if ok, err := client.CachedAdmin(context.Background(), -100, 7); !ok || err != nil {
		t.Fatalf("initial CachedAdmin = (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := client.CachedAdmin(context.Background(), -100, 7); !ok || err != nil {
		t.Fatalf("cached CachedAdmin = (%v, %v), want (true, nil)", ok, err)
	}
	if got := len(caller.methodCalls("getChatMember")); got != 1 {
		t.Fatalf("cached path made %d Telegram calls, want 1", got)
	}
	if ok, err := client.FreshAdmin(context.Background(), -100, 7); ok || err != nil {
		t.Fatalf("fresh revoked admin = (%v, %v), want (false, nil)", ok, err)
	}
	if got := len(caller.methodCalls("getChatMember")); got != 2 {
		t.Fatalf("fresh path made total %d Telegram calls, want 2", got)
	}

	nonAdminCaller := &scriptedCaller{responses: map[string][]scriptedResult{
		"getChatMember": {{value: member}, {value: admin}},
	}}
	nonAdminClient := newTestClient(t, nonAdminCaller)
	if ok, err := nonAdminClient.CachedAdmin(context.Background(), -100, 8); ok || err != nil {
		t.Fatalf("member CachedAdmin = (%v, %v), want (false, nil)", ok, err)
	}
	if ok, err := nonAdminClient.CachedAdmin(context.Background(), -100, 8); !ok || err != nil {
		t.Fatalf("promoted CachedAdmin = (%v, %v), want (true, nil)", ok, err)
	}
	if got := len(nonAdminCaller.methodCalls("getChatMember")); got != 2 {
		t.Fatalf("non-admin status was cached: Telegram calls = %d, want 2", got)
	}

	errorCaller := &scriptedCaller{responses: map[string][]scriptedResult{
		"getChatMember": {{err: errors.New("network")}},
	}}
	errorClient := newTestClient(t, errorCaller)
	errorClient.adminCache[adminKey{chatID: -100, userID: 9}] = time.Now().Add(time.Minute)
	if ok, err := errorClient.FreshAdmin(context.Background(), -100, 9); ok || err == nil {
		t.Fatalf("failed fresh lookup = (%v, %v), want (false, error)", ok, err)
	}
	if got := len(errorCaller.methodCalls("getChatMember")); got != 1 {
		t.Fatalf("failed fresh lookup made %d Telegram calls, want 1 despite cached positive", got)
	}
}

func TestAdminCacheBoundAndRights(t *testing.T) {
	client := newTestClient(t, &scriptedCaller{})
	now := time.Now()
	for i := range adminCacheMax + 17 {
		client.adminCache[adminKey{chatID: -100, userID: int64(i)}] = now.Add(time.Duration(i+1) * time.Second)
	}
	client.pruneAdminCacheLocked(now)
	if len(client.adminCache) != adminCacheMax {
		t.Fatalf("admin cache entries = %d, want %d", len(client.adminCache), adminCacheMax)
	}

	expiredKey := adminKey{chatID: -1, userID: 1}
	freshKey := adminKey{chatID: -1, userID: 2}
	client.adminCache[expiredKey] = now.Add(-time.Minute)
	client.adminCache[freshKey] = now.Add(time.Minute)
	client.pruneAdminCacheLocked(now)
	if _, ok := client.adminCache[expiredKey]; ok {
		t.Error("expired admin-cache entry was not pruned")
	}
	if _, ok := client.adminCache[freshKey]; !ok {
		t.Error("fresh admin-cache entry was pruned")
	}
	for i := range 17 {
		if _, ok := client.adminCache[adminKey{chatID: -100, userID: int64(i)}]; ok {
			t.Errorf("oldest live entry %d was not evicted", i)
		}
	}

	if missing := MissingModRights(&telego.ChatMemberAdministrator{CanInviteUsers: true, CanRestrictMembers: true, CanDeleteMessages: true}); len(missing) != 0 {
		t.Errorf("fully privileged admin missing rights: %v", missing)
	}
	if missing := MissingModRights(&telego.ChatMemberAdministrator{}); len(missing) != 3 {
		t.Errorf("admin with no rights missing %d rights, want 3", len(missing))
	}
	if missing := MissingModRights(&telego.ChatMemberAdministrator{CanInviteUsers: true, CanDeleteMessages: true}); len(missing) != 1 {
		t.Errorf("admin missing only restrict permission reported %d rights, want 1", len(missing))
	}
	if missing := MissingModRights(&telego.ChatMemberOwner{}); len(missing) != 0 {
		t.Errorf("owner missing rights: %v", missing)
	}
}

func TestAdminCacheOneEntryOverBound(t *testing.T) {
	client := newTestClient(t, &scriptedCaller{})
	now := time.Now()
	for i := range adminCacheMax + 1 {
		client.adminCache[adminKey{chatID: -100, userID: int64(i)}] = now.Add(time.Duration(i+1) * time.Second)
	}
	client.pruneAdminCacheLocked(now)
	if len(client.adminCache) != adminCacheMax {
		t.Fatalf("admin cache entries = %d, want %d", len(client.adminCache), adminCacheMax)
	}
	if _, ok := client.adminCache[adminKey{chatID: -100, userID: 0}]; ok {
		t.Error("oldest live entry was not evicted")
	}
}

func TestEnforcementActions(t *testing.T) {
	defaults := telego.ChatPermissions{CanSendMessages: telego.ToPtr(false), CanInviteUsers: telego.ToPtr(true)}
	caller := &scriptedCaller{responses: map[string][]scriptedResult{
		"getChat": {{value: &telego.ChatFullInfo{Permissions: &defaults}}},
	}}
	client := newTestClient(t, caller)
	ctx := context.Background()
	before := time.Now().Unix()
	if err := client.Ban(ctx, -100, 5, 3600, true); err != nil {
		t.Fatal(err)
	}
	if err := client.Unban(ctx, -100, 5, true); err != nil {
		t.Fatal(err)
	}
	if err := client.Mute(ctx, -100, 5, 3600); err != nil {
		t.Fatal(err)
	}
	if err := client.Unmute(ctx, -100, 5); err != nil {
		t.Fatal(err)
	}
	if err := client.BanSenderChat(ctx, -100, -200); err != nil {
		t.Fatal(err)
	}
	if err := client.UnbanSenderChat(ctx, -100, -200); err != nil {
		t.Fatal(err)
	}

	banCalls := caller.methodCalls("banChatMember")
	if len(banCalls) != 1 {
		t.Fatalf("banChatMember calls = %d, want 1", len(banCalls))
	}
	var ban telego.BanChatMemberParams
	if err := json.Unmarshal(banCalls[0].body, &ban); err != nil {
		t.Fatal(err)
	}
	if !ban.RevokeMessages || ban.UntilDate < before+3599 || ban.UntilDate > time.Now().Unix()+3601 {
		t.Errorf("ban params = %+v", ban)
	}

	restrictions := caller.methodCalls("restrictChatMember")
	if len(restrictions) != 2 {
		t.Fatalf("restrictChatMember calls = %d, want 2", len(restrictions))
	}
	var unmute telego.RestrictChatMemberParams
	if err := json.Unmarshal(restrictions[1].body, &unmute); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(unmute.Permissions, defaults) {
		t.Errorf("unmute permissions = %+v, want group defaults %+v", unmute.Permissions, defaults)
	}
}

func TestMuteSurfacesTelegramError(t *testing.T) {
	caller := &scriptedCaller{responses: map[string][]scriptedResult{
		"restrictChatMember": {{err: errors.New("no rights")}},
	}}
	if err := newTestClient(t, caller).Mute(context.Background(), -100, 5, 3600); err == nil {
		t.Error("Mute returned nil for a Telegram restriction error")
	}
}

func TestUnmuteRejectsUnavailableDefaults(t *testing.T) {
	getChatErr := errors.New("temporary failure")
	var nilChat *telego.ChatFullInfo
	tests := []struct {
		name    string
		result  scriptedResult
		wantErr error
	}{
		{name: "GetChat failure", result: scriptedResult{err: getChatErr}, wantErr: getChatErr},
		{name: "nil chat", result: scriptedResult{value: nilChat}},
		{name: "missing defaults", result: scriptedResult{value: &telego.ChatFullInfo{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caller := &scriptedCaller{responses: map[string][]scriptedResult{"getChat": {tt.result}}}
			err := newTestClient(t, caller).Unmute(context.Background(), -100, 5)
			if err == nil {
				t.Fatal("Unmute returned nil without group default permissions")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("Unmute error = %v, want %v", err, tt.wantErr)
			}
			if calls := caller.methodCalls("restrictChatMember"); len(calls) != 0 {
				t.Fatalf("restrictChatMember calls = %d, want 0", len(calls))
			}
		})
	}
}

func TestErrorClassification(t *testing.T) {
	if !IsBlocked(&ta.Error{ErrorCode: 403, Description: "Forbidden"}) || IsBlocked(errors.New("Bad Gateway")) {
		t.Error("403 blocked classification failed")
	}
	initErr := &ta.Error{ErrorCode: 403, Description: "Forbidden: bot can't initiate conversation with a user"}
	blockedErr := &ta.Error{ErrorCode: 403, Description: "Forbidden: bot was blocked by the user"}
	if !CannotInitiateConversation(initErr) || CannotInitiateConversation(blockedErr) ||
		!BotWasBlockedByUser(blockedErr) || BotWasBlockedByUser(initErr) {
		t.Error("never-started and blocked-user 403 responses were not distinguished")
	}
	rateErr := &ta.Error{ErrorCode: 429, Description: "Too Many Requests", Parameters: &ta.ResponseParameters{RetryAfter: 30}}
	if !IsRateLimited(rateErr) || RetryAfter(rateErr) != 30*time.Second {
		t.Errorf("429 classification = rateLimited %v retryAfter %v", IsRateLimited(rateErr), RetryAfter(rateErr))
	}
	if RetryAfter(errors.New("Too Many Requests: retry after: 7")) != 7*time.Second {
		t.Error("text retry-after was not extracted")
	}
	if PermanentEditError(errors.New("Bad Request: chat not found")) {
		t.Error("chat not found must remain transient for edits")
	}
	for _, message := range []string{"message to edit not found", "message can't be edited", "MESSAGE_ID_INVALID"} {
		if !PermanentEditError(errors.New("Bad Request: " + message)) {
			t.Errorf("%q should be a permanent edit error", message)
		}
	}
	if CountablePermanentEditError(context.Canceled) || CountablePermanentEditError(errors.New("Bad Gateway")) {
		t.Error("cancellation and 5xx-like errors must not count as deterministic 400s")
	}
	if !CountablePermanentEditError(&ta.Error{ErrorCode: 400, Description: "Bad Request"}) {
		t.Error("structured 400 should count as deterministic")
	}
	if PermanentPostError(errors.New("Bad Request: chat not found")) || !PermanentPostError(errors.New("Bad Request: invalid entity")) {
		t.Error("post permanence classification changed")
	}
}

func TestPaceAndTextUnits(t *testing.T) {
	if !Pace(context.Background(), 0) {
		t.Error("disabled pacing should return immediately")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if Pace(ctx, time.Hour) {
		t.Error("cancelled pacing should return false")
	}

	text := "a😀界"
	if got := TextUnits(text); got != 4 {
		t.Fatalf("TextUnits(%q) = %d, want 4", text, got)
	}
	capped := CapText("a😀bc", 4)
	if capped != "a😀…" || !utf8.ValidString(capped) || TextUnits(capped) > 4 {
		t.Errorf("CapText() = %q (%d units)", capped, TextUnits(capped))
	}
	if got := CapText("unchanged", MessageLimit); got != "unchanged" {
		t.Errorf("short text changed to %q", got)
	}
}

func waitForMethodCalls(t *testing.T, caller *scriptedCaller, method string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(caller.methodCalls(method)) >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("%s calls = %d, want at least %d", method, len(caller.methodCalls(method)), want)
}
