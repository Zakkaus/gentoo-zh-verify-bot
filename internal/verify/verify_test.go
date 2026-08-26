package verify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	th "github.com/mymmrac/telego/telegohandler"
)

func newTestService(cfg *config.Config) *Service {
	effective := *cfg
	if len(effective.Groups) == 0 && len(effective.GroupIDs) == 0 {
		effective.GroupIDs = []int64{-100}
	}
	baseline, err := store.LoadBaseline("", &effective)
	if err != nil {
		panic(fmt.Sprintf("test settings baseline: %v", err))
	}
	settings, err := store.NewSettings("", baseline)
	if err != nil {
		panic(fmt.Sprintf("test settings: %v", err))
	}
	return newService(settings, nil, &effective, &i18n.Messages)
}

func TestJoinerLabel(t *testing.T) {
	const evil = `繁星帮<&>"` // an advert-style name with HTML metacharacters
	on := joinerLabel(42, evil, true)
	if !strings.HasPrefix(on, "<tg-spoiler>") || !strings.HasSuffix(on, "</tg-spoiler>") {
		t.Errorf("spoiler-on should wrap the name in one <tg-spoiler> entity, got %q", on)
	}
	if strings.Contains(on, "<a ") || strings.Contains(on, "tg://user") {
		t.Errorf("spoiler-on must NOT emit a nested mention link (parse-safety), got %q", on)
	}
	if strings.Contains(on, "<&>") || strings.Contains(on, "\"") {
		t.Errorf("spoiler-on must HTML-escape the name, got %q", on)
	}
	off := joinerLabel(42, evil, false)
	if !strings.Contains(off, `href="tg://user?id=42"`) {
		t.Errorf("spoiler-off should render a clickable mention, got %q", off)
	}
	if strings.Contains(off, "<&>") {
		t.Errorf("spoiler-off must HTML-escape the name, got %q", off)
	}
}

func TestNameSpoilerDefaultAndToggle(t *testing.T) {
	const groupID int64 = -100
	v := newTestService(&config.Config{Groups: []config.GroupConfig{{ID: groupID}}, GroupIDs: []int64{groupID}})
	if !v.NameSpoilerOn(groupID) {
		t.Error("name spoiler should default ON (spam names are often adverts)")
	}
	enabled, err := v.ToggleNameSpoiler(groupID)
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Error("toggle should turn it OFF and return the new state (false)")
	}
	if v.NameSpoilerOn(groupID) {
		t.Error("name spoiler should now be OFF")
	}
}
func TestJoinResolvesApplicantAndGroupLanguagesSeparately(t *testing.T) {
	const (
		groupID int64 = -100
		userID  int64 = 42
	)
	v := newTestService(&config.Config{
		Groups:         []config.GroupConfig{{ID: groupID}},
		GroupIDs:       []int64{groupID},
		Lang:           "zh",
		VerifyMode:     config.ModeKernel,
		TimeoutSeconds: 240,
	})
	group, _ := v.settings.Group(groupID)
	overrides := group.Overrides()
	deliveryMode := config.DeliveryGroup
	overrides.DeliveryMode = &deliveryMode
	if _, err := v.settings.CommitGroup(groupID, group.Revision(), overrides); err != nil {
		t.Fatal(err)
	}
	bot := newFakeVerifyBot()
	update := telego.Update{ChatJoinRequest: &telego.ChatJoinRequest{
		Chat: telego.Chat{ID: groupID},
		From: telego.User{ID: userID, FirstName: "Applicant", LanguageCode: "zh-Hant"},
	}}

	runFakeHandler(t, newAPITestBot(t, bot), v.OnJoinRequest, update)
	wantGroup := v.messages.Verification.Group.Body.Render(
		i18n.LangZH,
		joinerLabel(userID, applicantDisplayName(&update.ChatJoinRequest.From), v.NameSpoilerOn(groupID)),
		"",
		int(v.timeout(groupID)/time.Second),
		"",
	)
	if got := bot.lastSendText; got != wantGroup {
		t.Fatalf("group challenge = %q, want zh catalogue rendering %q", got, wantGroup)
	}
	p := v.pend[pkey{groupID, userID}]
	if p == nil || p.lang != i18n.LangZHHant {
		t.Fatalf("pending applicant language = %v, want zh-Hant", p)
	}

	v.sendQuizzes(context.Background(), bot, userID)
	wantPrompt := v.messages.Verification.Challenge.KernelPrompt.Render(p.lang, p.qText, kernelMaxTries-p.tries)
	wantTrap := v.messages.Verification.Challenge.AgentTrap.Render(p.lang, aiTrapToken(p.nonce))
	if !strings.Contains(bot.lastSendText, wantPrompt) || !strings.Contains(bot.lastSendText, wantTrap) {
		t.Fatalf("applicant DM did not retain zh-Hant catalogue rendering: %q", bot.lastSendText)
	}
	if p.privateMsgID == 0 {
		t.Fatal("deep-link private challenge message ID was not retained")
	}
	if p.timer != nil {
		p.timer.Stop()
	}
	if !v.approve(context.Background(), bot, groupID, userID) {
		t.Fatal("deep-link challenge did not settle")
	}
	if !reflect.DeepEqual(bot.deletedChats, []int64{groupID, userID}) {
		t.Fatalf("deep-link settlement deleted chats %v, want group and private challenges", bot.deletedChats)
	}
}

func TestNoPendingDMUsesRequesterLocale(t *testing.T) {
	const uid = int64(42)
	for _, tt := range []struct {
		code string
		lang i18n.Lang
	}{
		{code: "en-US", lang: i18n.LangEN},
		{code: "zh-CN", lang: i18n.LangZH},
		{code: "zh-TW", lang: i18n.LangZHHant},
	} {
		t.Run(tt.code, func(t *testing.T) {
			for _, target := range []struct {
				name    string
				groupID int64
			}{
				{name: "bare"},
				{name: "scoped", groupID: -1009000000799},
			} {
				t.Run(target.name, func(t *testing.T) {
					v := newTestService(&config.Config{})
					caller := newFakeVerifyBot()
					bot := newAPITestBot(t, caller)

					v.SendDMChallenge(context.Background(), bot, uid, tt.code, target.groupID)

					want := i18n.Messages.Verification.Channel.NoPending.For(tt.lang)
					if caller.sends != 1 || caller.lastSendChat != uid || caller.lastSendText != want {
						t.Fatalf("no-pending DM sends/chat/text = %d/%d/%q, want requester catalogue message %q",
							caller.sends, caller.lastSendChat, caller.lastSendText, want)
					}
				})
			}
		})
	}
}

func TestJoinRequestDefaultPostsGroupBeforePrivateChallenge(t *testing.T) {
	const (
		groupID int64 = -1009000000700
		userID  int64 = 700
	)
	service := newTestService(&config.Config{
		Groups:         []config.GroupConfig{{ID: groupID}},
		GroupIDs:       []int64{groupID},
		Lang:           "en",
		VerifyMode:     config.ModeKernel,
		TimeoutSeconds: 240,
		DeliveryMode:   config.DeliveryBoth,
	})
	service.botUsername = "settings_test_bot"
	caller := newFakeVerifyBot()
	release := make(chan struct{}, 1)
	caller.sendStarted = make(chan struct{}, 1)
	caller.releaseSend = release
	caller.blockSendN = 2
	done := make(chan struct{})
	go func() {
		runFakeHandler(t, newAPITestBot(t, caller), service.OnJoinRequest, telego.Update{ChatJoinRequest: &telego.ChatJoinRequest{
			Chat: telego.Chat{ID: groupID},
			From: telego.User{ID: userID, FirstName: "Applicant", LanguageCode: "en"},
		}})
		close(done)
	}()
	defer func() {
		select {
		case release <- struct{}{}:
		default:
		}
		service.Shutdown()
	}()

	select {
	case <-caller.sendStarted:
	case <-time.After(time.Second):
		t.Fatal("private challenge was not attempted after the group challenge")
	}
	if len(caller.sendChats) != 2 || caller.sendChats[0] != groupID || caller.sendChats[1] != userID {
		t.Fatalf("challenge send order = %v, want group then private", caller.sendChats)
	}
	release <- struct{}{}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("join request did not finish after private delivery")
	}
	pending := service.pend[pkey{groupID, userID}]
	if pending == nil || !pending.challengeDelivered || pending.groupMsgID == 0 || pending.privateMsgID == 0 {
		t.Fatalf("default delivery pending state = %+v, want confirmed group and private delivery", pending)
	}
	if !service.approve(context.Background(), caller, groupID, userID) {
		t.Fatal("approved challenge was not settled")
	}
	if caller.deletes != 2 || !reflect.DeepEqual(caller.deletedChats, []int64{groupID, userID}) {
		t.Fatalf("settlement deleted chats = %v, want group and private challenges", caller.deletedChats)
	}
}

func TestRequiredChannelPromptMessageIsRetainedForCleanup(t *testing.T) {
	const (
		groupID   int64 = -1009000000703
		channelID int64 = -1009000000803
		userID    int64 = 703
	)
	service := newTestService(&config.Config{
		Groups:            []config.GroupConfig{{ID: groupID}},
		GroupIDs:          []int64{groupID},
		Lang:              "en",
		VerifyMode:        config.ModeKernel,
		DeliveryMode:      config.DeliveryBoth,
		TimeoutSeconds:    240,
		RequiredChannelID: channelID,
		ChannelDisplay:    "@required",
	})
	caller := newFakeVerifyBot()
	caller.member = &telego.ChatMemberLeft{Status: telego.MemberStatusLeft}
	runFakeHandler(t, newAPITestBot(t, caller), service.OnJoinRequest, telego.Update{ChatJoinRequest: &telego.ChatJoinRequest{
		Chat: telego.Chat{ID: groupID},
		From: telego.User{ID: userID, FirstName: "Applicant", LanguageCode: "en"},
	}})
	defer service.Shutdown()

	pending := service.pend[pkey{groupID, userID}]
	if pending == nil || pending.groupMsgID == 0 || pending.privateMsgID == 0 {
		t.Fatalf("required-channel delivery pending state = %+v, want retained group and private message IDs", pending)
	}
	if pending.prompted {
		t.Fatal("required-channel prompt incorrectly marked the kernel question as delivered")
	}
	if !service.approve(context.Background(), caller, groupID, userID) {
		t.Fatal("required-channel pending did not settle")
	}
	if !reflect.DeepEqual(caller.deletedChats, []int64{groupID, userID}) {
		t.Fatalf("required-channel settlement deleted chats %v, want group and private prompts", caller.deletedChats)
	}
}

func TestRequiredChannelDeepLinkPromptMessageIsRetainedForCleanup(t *testing.T) {
	const (
		groupID   int64 = -1009000000704
		channelID int64 = -1009000000804
		userID    int64 = 704
	)
	service := newTestService(&config.Config{
		Groups:            []config.GroupConfig{{ID: groupID}},
		GroupIDs:          []int64{groupID},
		Lang:              "en",
		VerifyMode:        config.ModeKernel,
		DeliveryMode:      config.DeliveryGroup,
		TimeoutSeconds:    240,
		RequiredChannelID: channelID,
		ChannelDisplay:    "@required",
	})
	caller := newFakeVerifyBot()
	caller.member = &telego.ChatMemberLeft{Status: telego.MemberStatusLeft}
	bot := newAPITestBot(t, caller)
	runFakeHandler(t, bot, service.OnJoinRequest, telego.Update{ChatJoinRequest: &telego.ChatJoinRequest{
		Chat: telego.Chat{ID: groupID},
		From: telego.User{ID: userID, FirstName: "Applicant", LanguageCode: "en"},
	}})
	defer service.Shutdown()

	service.SendDMChallenge(context.Background(), bot, userID, "en", groupID)

	pending := service.pend[pkey{groupID, userID}]
	if pending == nil || pending.groupMsgID == 0 || pending.privateMsgID == 0 {
		t.Fatalf("deep-link channel delivery pending state = %+v, want retained group and private message IDs", pending)
	}
	if pending.prompted {
		t.Fatal("required-channel prompt incorrectly marked the kernel question as delivered")
	}
	if !service.approve(context.Background(), caller, groupID, userID) {
		t.Fatal("deep-link channel pending did not settle")
	}
	if !reflect.DeepEqual(caller.deletedChats, []int64{groupID, userID}) {
		t.Fatalf("deep-link channel settlement deleted chats %v, want group and private prompts", caller.deletedChats)
	}
}

func TestJoinRequestBothCountsEitherConfirmedDelivery(t *testing.T) {
	const (
		groupID int64 = -1009000000790
		userID  int64 = 790
	)
	tests := []struct {
		name             string
		errors           map[int]error
		wantDelivered    bool
		wantGroupMessage bool
	}{
		{
			name:             "private succeeds after group failure",
			errors:           map[int]error{1: errors.New("group delivery failed")},
			wantDelivered:    true,
			wantGroupMessage: false,
		},
		{
			name:             "definite private rejection keeps group delivery",
			errors:           map[int]error{2: &ta.Error{ErrorCode: 403, Description: "Forbidden: bot was blocked by the user"}},
			wantDelivered:    true,
			wantGroupMessage: true,
		},
		{
			name:             "uncertain private delivery keeps group delivery",
			errors:           map[int]error{2: errors.New("connection reset after request write")},
			wantDelivered:    true,
			wantGroupMessage: true,
		},
		{
			name: "two unconfirmed deliveries remain strike-free",
			errors: map[int]error{
				1: errors.New("group delivery failed"),
				2: errors.New("connection reset after request write"),
			},
			wantDelivered:    false,
			wantGroupMessage: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := newTestService(&config.Config{
				Groups:         []config.GroupConfig{{ID: groupID}},
				GroupIDs:       []int64{groupID},
				Lang:           "en",
				VerifyMode:     config.ModeKernel,
				TimeoutSeconds: 240,
			})
			caller := newFakeVerifyBot()
			caller.sendErrAt = tt.errors
			runFakeHandler(t, newAPITestBot(t, caller), service.OnJoinRequest, telego.Update{ChatJoinRequest: &telego.ChatJoinRequest{
				Chat: telego.Chat{ID: groupID},
				From: telego.User{ID: userID, FirstName: "Applicant", LanguageCode: "en"},
			}})
			defer service.Shutdown()

			if caller.sends != 2 || len(caller.sendChats) != 2 ||
				caller.sendChats[0] != groupID || caller.sendChats[1] != userID {
				t.Fatalf("challenge sends/chats = %d/%v, want one group attempt then one private attempt",
					caller.sends, caller.sendChats)
			}
			pending := service.pend[pkey{groupID, userID}]
			if pending == nil || pending.challengeDelivered != tt.wantDelivered ||
				(pending.groupMsgID != 0) != tt.wantGroupMessage {
				t.Fatalf("pending delivery state = %+v, want delivered=%t group-message=%t",
					pending, tt.wantDelivered, tt.wantGroupMessage)
			}
			if got := challengeExpiryReason(pending.challengeDelivered); !tt.wantDelivered && got != "challenge-post-failed" {
				t.Fatalf("unconfirmed delivery expiry reason = %q, want strike-free challenge-post-failed", got)
			}
		})
	}
}

func TestJoinRequestDMModeDeliversPrivatelyOrFallsBackToScopedGroupLink(t *testing.T) {
	const (
		groupID int64 = -1009000000701
		userID  int64 = 701
	)
	newVerifier := func() *Service {
		service := newTestService(&config.Config{
			Groups:         []config.GroupConfig{{ID: groupID}},
			GroupIDs:       []int64{groupID},
			Lang:           "en",
			VerifyMode:     config.ModeKernel,
			TimeoutSeconds: 240,
			DeliveryMode:   config.DeliveryDM,
		})
		service.botUsername = "settings_test_bot"
		return service
	}
	update := telego.Update{ChatJoinRequest: &telego.ChatJoinRequest{
		Chat: telego.Chat{ID: groupID},
		From: telego.User{ID: userID, FirstName: "Applicant", LanguageCode: "en"},
	}}

	t.Run("successful DM suppresses group challenge", func(t *testing.T) {
		service := newVerifier()
		caller := newFakeVerifyBot()
		runFakeHandler(t, newAPITestBot(t, caller), service.OnJoinRequest, update)
		defer service.Shutdown()

		if caller.sends != 1 || len(caller.sendChats) != 1 || caller.sendChats[0] != userID {
			t.Fatalf("challenge sends/chats = %d/%v, want one private send", caller.sends, caller.sendChats)
		}
		if pending := service.pend[pkey{groupID, userID}]; pending == nil ||
			!pending.challengeDelivered || pending.groupMsgID != 0 {
			t.Fatalf("private delivery pending state = %+v, want delivered without group message", pending)
		}
	})

	t.Run("cannot initiate DM falls back with group-scoped link", func(t *testing.T) {
		service := newVerifier()
		caller := newFakeVerifyBot()
		caller.sendErr = &ta.Error{ErrorCode: 403, Description: "Forbidden: bot can't initiate conversation with a user"}
		caller.sendFailN = 1
		runFakeHandler(t, newAPITestBot(t, caller), service.OnJoinRequest, update)
		defer service.Shutdown()

		if caller.sends != 2 || len(caller.sendChats) != 2 ||
			caller.sendChats[0] != userID || caller.sendChats[1] != groupID {
			t.Fatalf("challenge sends/chats = %d/%v, want failed private send then group fallback", caller.sends, caller.sendChats)
		}
		wantLink := fmt.Sprintf("https://t.me/settings_test_bot?start=verify_%d", groupID)
		if !strings.Contains(caller.sendTexts[1], wantLink) {
			t.Fatalf("group fallback does not contain scoped link %q: %q", wantLink, caller.sendTexts[1])
		}
		if pending := service.pend[pkey{groupID, userID}]; pending == nil ||
			!pending.challengeDelivered || pending.groupMsgID == 0 {
			t.Fatalf("group fallback pending state = %+v, want confirmed group delivery", pending)
		}
	})

	t.Run("per-group setting keeps group-first behavior", func(t *testing.T) {
		service := newVerifier()
		group, _ := service.settings.Group(groupID)
		overrides := group.Overrides()
		deliveryMode := config.DeliveryGroup
		overrides.DeliveryMode = &deliveryMode
		if _, err := service.settings.CommitGroup(groupID, group.Revision(), overrides); err != nil {
			t.Fatal(err)
		}
		caller := newFakeVerifyBot()
		runFakeHandler(t, newAPITestBot(t, caller), service.OnJoinRequest, update)
		defer service.Shutdown()

		if caller.sends != 1 || len(caller.sendChats) != 1 || caller.sendChats[0] != groupID {
			t.Fatalf("group-first challenge sends/chats = %d/%v, want one group send", caller.sends, caller.sendChats)
		}
	})

	for _, rejection := range []struct {
		name string
		err  error
	}{
		{name: "blocked after an earlier conversation", err: &ta.Error{ErrorCode: 403, Description: "Forbidden: bot was blocked by the user"}},
		{name: "rate limited without a retry delay", err: &ta.Error{ErrorCode: 429, Description: "Too Many Requests"}},
	} {
		t.Run(rejection.name, func(t *testing.T) {
			service := newVerifier()
			caller := newFakeVerifyBot()
			caller.sendErr = rejection.err
			caller.sendFailN = 1
			runFakeHandler(t, newAPITestBot(t, caller), service.OnJoinRequest, update)
			defer service.Shutdown()

			if caller.sends != 2 || caller.sendChats[0] != userID || caller.sendChats[1] != groupID {
				t.Fatalf("%s produced sends/chats = %d/%v, want private rejection then group fallback",
					rejection.name, caller.sends, caller.sendChats)
			}
		})
	}
}

func TestJoinRequestAmbiguousDMFailureDoesNotRiskDuplicateGroupChallenge(t *testing.T) {
	const (
		groupID int64 = -1009000000702
		userID  int64 = 702
	)
	service := newTestService(&config.Config{
		Groups:         []config.GroupConfig{{ID: groupID}},
		GroupIDs:       []int64{groupID},
		Lang:           "en",
		VerifyMode:     config.ModeKernel,
		TimeoutSeconds: 240,
		DeliveryMode:   config.DeliveryDM,
	})
	caller := newFakeVerifyBot()
	caller.sendErr = errors.New("connection reset after request write")
	caller.sendFailN = 1
	runFakeHandler(t, newAPITestBot(t, caller), service.OnJoinRequest, telego.Update{ChatJoinRequest: &telego.ChatJoinRequest{
		Chat: telego.Chat{ID: groupID},
		From: telego.User{ID: userID, FirstName: "Applicant", LanguageCode: "en"},
	}})
	defer service.Shutdown()

	if caller.sends != 1 || len(caller.sendChats) != 1 || caller.sendChats[0] != userID {
		t.Fatalf("ambiguous send failure produced sends/chats = %d/%v, want only the uncertain DM attempt", caller.sends, caller.sendChats)
	}
	if pending := service.pend[pkey{groupID, userID}]; pending == nil || pending.challengeDelivered {
		t.Fatalf("uncertain private delivery pending state = %+v, want strike-free unconfirmed delivery", pending)
	}
}

// fakeVerifyBot is a verifyBot stand-in so the approve / decline / ban handler branches can be
// exercised without a real Telegram connection; it records call counts and returns configured
// errors for those network actions.
type muteCall struct {
	chatID  int64
	userID  int64
	seconds int
}

type fakeVerifyBot struct {
	approveErr        error
	declineErr        error
	banErr            error
	getMeErr          error
	sendErr           error // returned by the first sendFailN SendMessage calls (markup-rejection tests)
	sendFailN         int
	sendErrAt         map[int]error
	deleteErrAt       map[int]error
	sendMessageID     int
	sendStarted       chan struct{}
	releaseSend       <-chan struct{}
	blockSendN        int
	approves          int
	declines          int
	bans              int
	unbans            int
	mutes             int
	unmutes           int
	unbanErr          error
	muteErr           error
	unmuteErr         error
	unbanned          [][2]int64
	unmuted           [][2]int64
	muted             []muteCall
	deletes           int
	sends             int
	getMeCalls        int
	lastSendChat      int64
	lastSendText      string
	lastParseMode     string
	sendChats         []int64
	sendTexts         []string
	deletedChats      []int64
	deletedMessageIDs []int
	member            telego.ChatMember
	memberByID        map[int64]telego.ChatMember
	memberErr         error
	memberRequests    []telego.GetChatMemberParams
	answers           int
	callbackAnswers   []telego.AnswerCallbackQueryParams
}

func newFakeVerifyBot() *fakeVerifyBot { return &fakeVerifyBot{} }

// GetMe lets the fake stand in for the heartbeat's liveness probe (liveProbe / heartbeatBot).
func (b *fakeVerifyBot) GetMe(context.Context) (*telego.User, error) {
	b.getMeCalls++
	if b.getMeErr != nil {
		return nil, b.getMeErr
	}
	return &telego.User{ID: 1, IsBot: true}, nil
}

func (b *fakeVerifyBot) ApproveChatJoinRequest(context.Context, *telego.ApproveChatJoinRequestParams) error {
	b.approves++
	return b.approveErr
}
func (b *fakeVerifyBot) DeclineChatJoinRequest(context.Context, *telego.DeclineChatJoinRequestParams) error {
	b.declines++
	return b.declineErr
}
func (b *fakeVerifyBot) BanChatMember(context.Context, *telego.BanChatMemberParams) error {
	b.bans++
	return b.banErr
}
func (b *fakeVerifyBot) DeleteMessage(_ context.Context, p *telego.DeleteMessageParams) error {
	b.deletes++
	b.deletedChats = append(b.deletedChats, p.ChatID.ID)
	b.deletedMessageIDs = append(b.deletedMessageIDs, p.MessageID)
	return b.deleteErrAt[b.deletes]
}
func (b *fakeVerifyBot) SendMessage(_ context.Context, p *telego.SendMessageParams) (*telego.Message, error) {
	b.sends++
	sendN := b.sends
	b.lastSendChat = p.ChatID.ID
	b.lastSendText = p.Text
	b.lastParseMode = p.ParseMode
	b.sendChats = append(b.sendChats, p.ChatID.ID)
	b.sendTexts = append(b.sendTexts, p.Text)
	block := b.releaseSend != nil && (b.blockSendN == 0 || b.blockSendN == sendN)
	if block && b.sendStarted != nil {
		select {
		case b.sendStarted <- struct{}{}:
		default:
		}
	}
	if block {
		<-b.releaseSend
	}
	if err := b.sendErrAt[sendN]; err != nil {
		return nil, err
	}
	if b.sendErr != nil && sendN <= b.sendFailN {
		return nil, b.sendErr
	}
	messageID := b.sendMessageID
	if messageID == 0 {
		messageID = 1
	}
	return &telego.Message{MessageID: messageID}, nil
}

func (b *fakeVerifyBot) SendHTMLFallback(ctx context.Context, chatID int64, rich, simpler string) (*telego.Message, error) {
	send := func(text, parseMode string) (*telego.Message, error) {
		return b.SendMessage(ctx, &telego.SendMessageParams{
			ChatID:    telego.ChatID{ID: chatID},
			Text:      text,
			ParseMode: parseMode,
		})
	}
	sent, err := send(rich, telego.ModeHTML)
	if err == nil {
		return sent, nil
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "parse") && !strings.Contains(message, "entit") && !strings.Contains(message, "bad request") {
		return nil, err
	}
	if simpler != "" && simpler != rich {
		sent, err = send(simpler, telego.ModeHTML)
		if err == nil {
			return sent, nil
		}
		message = strings.ToLower(err.Error())
		if !strings.Contains(message, "parse") && !strings.Contains(message, "entit") && !strings.Contains(message, "bad request") {
			return nil, err
		}
	}
	return send(simpler, "")
}

func (b *fakeVerifyBot) Delete(ctx context.Context, chatID int64, messageID int) {
	if messageID != 0 {
		_ = b.DeleteMessage(ctx, &telego.DeleteMessageParams{ChatID: telego.ChatID{ID: chatID}, MessageID: messageID})
	}
}

func (b *fakeVerifyBot) Alert(ctx context.Context, adminLogChatID int64, text string) {
	if adminLogChatID != 0 {
		_, _ = b.SendMessage(ctx, &telego.SendMessageParams{ChatID: telego.ChatID{ID: adminLogChatID}, Text: text})
	}
}

func (b *fakeVerifyBot) AuditLog(ctx context.Context, adminLogChatID int64, text string) {
	b.Alert(ctx, adminLogChatID, text)
}

func (b *fakeVerifyBot) FailAlert(ctx context.Context, adminLogChatID, groupID int64, text string) {
	if adminLogChatID == 0 {
		adminLogChatID = groupID
	}
	_, _ = b.SendMessage(ctx, &telego.SendMessageParams{ChatID: telego.ChatID{ID: adminLogChatID}, Text: text})
}

func (b *fakeVerifyBot) Unban(_ context.Context, chatID, userID int64, _ bool) error {
	b.unbans++
	b.unbanned = append(b.unbanned, [2]int64{chatID, userID})
	return b.unbanErr
}

func (b *fakeVerifyBot) Mute(_ context.Context, chatID, userID int64, seconds int) error {
	b.mutes++
	b.muted = append(b.muted, muteCall{chatID: chatID, userID: userID, seconds: seconds})
	return b.muteErr
}

func (b *fakeVerifyBot) Unmute(_ context.Context, chatID, userID int64) error {
	b.unmutes++
	b.unmuted = append(b.unmuted, [2]int64{chatID, userID})
	return b.unmuteErr
}

func (b *fakeVerifyBot) Ban(ctx context.Context, chatID, userID int64, _ int, revokeMessages bool) error {
	return b.BanChatMember(ctx, &telego.BanChatMemberParams{
		ChatID:         telego.ChatID{ID: chatID},
		UserID:         userID,
		RevokeMessages: revokeMessages,
	})
}

func (b *fakeVerifyBot) GetChatMember(_ context.Context, params *telego.GetChatMemberParams) (telego.ChatMember, error) {
	b.memberRequests = append(b.memberRequests, *params)
	if b.memberErr != nil {
		return nil, b.memberErr
	}
	if member, ok := b.memberByID[params.UserID]; ok {
		return member, nil
	}
	return b.member, nil
}

func (b *fakeVerifyBot) AnswerCallbackQuery(_ context.Context, params *telego.AnswerCallbackQueryParams) error {
	b.answers++
	b.callbackAnswers = append(b.callbackAnswers, *params)
	return nil
}

func (b *fakeVerifyBot) CachedAdmin(ctx context.Context, chatID, userID int64) (bool, error) {
	return b.adminStatus(ctx, chatID, userID)
}

func (b *fakeVerifyBot) FreshAdmin(ctx context.Context, chatID, userID int64) (bool, error) {
	return b.adminStatus(ctx, chatID, userID)
}

func (b *fakeVerifyBot) Notify(ctx context.Context, chatID int64, text string, _ int) {
	_, _ = b.SendMessage(ctx, &telego.SendMessageParams{ChatID: telego.ChatID{ID: chatID}, Text: text})
}

func (b *fakeVerifyBot) adminStatus(ctx context.Context, chatID, userID int64) (bool, error) {
	member, err := b.GetChatMember(ctx, &telego.GetChatMemberParams{ChatID: telego.ChatID{ID: chatID}, UserID: userID})
	if err != nil {
		return false, err
	}
	if member == nil {
		return false, nil
	}
	status := member.MemberStatus()
	return status == telego.MemberStatusCreator || status == telego.MemberStatusAdministrator, nil
}

func fakeTelegramResponse(value any, err error) (*ta.Response, error) {
	if err != nil {
		return nil, err
	}
	if value == nil {
		return &ta.Response{Ok: true}, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &ta.Response{Ok: true, Result: raw}, nil
}

func fakeSendMessageParams(raw []byte) (*telego.SendMessageParams, error) {
	var wire struct {
		ChatID              int64                   `json:"chat_id"`
		Text                string                  `json:"text"`
		ParseMode           string                  `json:"parse_mode"`
		DisableNotification bool                    `json:"disable_notification"`
		ReplyParameters     *telego.ReplyParameters `json:"reply_parameters"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, err
	}
	return &telego.SendMessageParams{
		ChatID:              telego.ChatID{ID: wire.ChatID},
		Text:                wire.Text,
		ParseMode:           wire.ParseMode,
		DisableNotification: wire.DisableNotification,
		ReplyParameters:     wire.ReplyParameters,
	}, nil
}

// Call adapts fakeVerifyBot to telego's transport hook for concrete handler tests.
func (b *fakeVerifyBot) Call(ctx context.Context, url string, data *ta.RequestData) (*ta.Response, error) {
	method := url[strings.LastIndexByte(url, '/')+1:]
	switch method {
	case "getChatMember":
		var params telego.GetChatMemberParams
		if err := json.Unmarshal(data.BodyRaw, &params); err != nil {
			return nil, err
		}
		member, err := b.GetChatMember(ctx, &params)
		return fakeTelegramResponse(member, err)
	case "answerCallbackQuery":
		var params telego.AnswerCallbackQueryParams
		if err := json.Unmarshal(data.BodyRaw, &params); err != nil {
			return nil, err
		}
		return fakeTelegramResponse(nil, b.AnswerCallbackQuery(ctx, &params))
	case "approveChatJoinRequest":
		var params telego.ApproveChatJoinRequestParams
		if err := json.Unmarshal(data.BodyRaw, &params); err != nil {
			return nil, err
		}
		return fakeTelegramResponse(nil, b.ApproveChatJoinRequest(ctx, &params))
	case "declineChatJoinRequest":
		var params telego.DeclineChatJoinRequestParams
		if err := json.Unmarshal(data.BodyRaw, &params); err != nil {
			return nil, err
		}
		return fakeTelegramResponse(nil, b.DeclineChatJoinRequest(ctx, &params))
	case "banChatMember":
		var params telego.BanChatMemberParams
		if err := json.Unmarshal(data.BodyRaw, &params); err != nil {
			return nil, err
		}
		return fakeTelegramResponse(nil, b.BanChatMember(ctx, &params))
	case "deleteMessage":
		var params telego.DeleteMessageParams
		if err := json.Unmarshal(data.BodyRaw, &params); err != nil {
			return nil, err
		}
		return fakeTelegramResponse(nil, b.DeleteMessage(ctx, &params))
	case "sendMessage":
		params, err := fakeSendMessageParams(data.BodyRaw)
		if err != nil {
			return nil, err
		}
		message, err := b.SendMessage(ctx, params)
		return fakeTelegramResponse(message, err)
	default:
		return nil, fmt.Errorf("unexpected Telegram method %q", method)
	}
}

func newAPITestBot(t *testing.T, caller ta.Caller) *telego.Bot {
	t.Helper()
	bot, err := telego.NewBot("1:"+strings.Repeat("a", 35), telego.WithAPICaller(caller), telego.WithDiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	return bot
}

func runFakeHandler(t *testing.T, bot *telego.Bot, handler th.Handler, update telego.Update) {
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

type blockingTerminalBot struct {
	*fakeVerifyBot
	approveStarted chan struct{}
	declineStarted chan struct{}
	release        chan struct{}
}

func newBlockingTerminalBot() *blockingTerminalBot {
	return &blockingTerminalBot{
		fakeVerifyBot:  &fakeVerifyBot{},
		approveStarted: make(chan struct{}),
		declineStarted: make(chan struct{}),
		release:        make(chan struct{}),
	}
}

func (b *blockingTerminalBot) ApproveChatJoinRequest(context.Context, *telego.ApproveChatJoinRequestParams) error {
	close(b.approveStarted)
	<-b.release
	return nil
}

func (b *blockingTerminalBot) DeclineChatJoinRequest(context.Context, *telego.DeclineChatJoinRequestParams) error {
	close(b.declineStarted)
	<-b.release
	return nil
}

func livePending(msgID int) *pending {
	return &pending{nonce: "n", deadline: time.Now().Add(time.Hour), groupMsgID: msgID}
}

func TestOnAnswer(t *testing.T) {
	const gid, uid = int64(-100), int64(5)
	tests := []struct {
		name        string
		from        int64
		data        string
		required    bool
		wantApprove int
		wantDecline int
		wantSend    int
		wantPending bool
		wantAlert   bool
	}{
		{name: "another applicant", from: 6, data: "v:-100:5:current:1", wantPending: true, wantAlert: true},
		{name: "stale nonce", from: uid, data: "v:-100:5:stale:1", wantPending: true, wantAlert: true},
		{name: "wrong option", from: uid, data: "v:-100:5:current:0", wantDecline: 1, wantAlert: true},
		{name: "correct option", from: uid, data: "v:-100:5:current:1", wantApprove: 1, wantSend: 1},
		{name: "correct option before joining channel", from: uid, data: "v:-100:5:current:1", required: true, wantPending: true, wantAlert: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{VerifyMaxFails: 3}
			if tt.required {
				cfg.RequiredChannelID = -400
			}
			v := newTestService(cfg)
			key := pkey{gid: gid, uid: uid}
			v.pend[key] = &pending{nonce: "current", correctIdx: 1, groupMsgID: 42, deadline: time.Now().Add(time.Hour)}
			bot := newFakeVerifyBot()
			if tt.required {
				bot.member = &telego.ChatMemberLeft{Status: telego.MemberStatusLeft}
			}
			update := telego.Update{CallbackQuery: &telego.CallbackQuery{
				ID: "answer", From: telego.User{ID: tt.from}, Data: tt.data,
			}}
			runFakeHandler(t, newAPITestBot(t, bot), v.OnAnswer, update)

			if bot.approves != tt.wantApprove || bot.declines != tt.wantDecline || bot.sends != tt.wantSend {
				t.Errorf("actions = approve %d, decline %d, send %d; want %d, %d, %d",
					bot.approves, bot.declines, bot.sends, tt.wantApprove, tt.wantDecline, tt.wantSend)
			}
			_, pending := v.pend[key]
			if pending != tt.wantPending {
				t.Errorf("pending remains = %v, want %v", pending, tt.wantPending)
			}
			if len(bot.callbackAnswers) != 1 {
				t.Fatalf("callback answers = %d, want 1", len(bot.callbackAnswers))
			}
			if got := bot.callbackAnswers[0].ShowAlert; got != tt.wantAlert {
				t.Errorf("callback show_alert = %v, want %v", got, tt.wantAlert)
			}
		})
	}
}

func TestAdminCallbackReportsActionInProgress(t *testing.T) {
	const gid, uid, adminID = int64(-100), int64(5), int64(9)
	tests := []struct {
		action       string
		wantText     func(*Service) string
		wantApproves int
		wantBans     int
		wantDeclines int
	}{
		{
			action:       "pass",
			wantText:     func(_ *Service) string { return i18n.Messages.Verification.Admin.Approving.For(i18n.LangZH) },
			wantApproves: 1,
		},
		{
			action: "ban",
			wantText: func(v *Service) string {
				duration := verificationBanDurationText(v.messages, i18n.LangZH, v.verificationBanDuration(gid))
				return i18n.Messages.Verification.Admin.Banning.Render(i18n.LangZH, duration)
			},
			wantBans:     1,
			wantDeclines: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			v := newTestService(&config.Config{BanSeconds: 3600})
			key := pkey{gid, uid}
			v.pend[key] = livePending(42)
			bot := newFakeVerifyBot()
			bot.member = &telego.ChatMemberAdministrator{Status: telego.MemberStatusAdministrator}
			update := telego.Update{CallbackQuery: &telego.CallbackQuery{
				ID: "admin", From: telego.User{ID: adminID},
				Data: AdminCallbackPrefix + tt.action + ":-100:5",
			}}

			runFakeHandler(t, newAPITestBot(t, bot), v.OnAdminAction, update)

			if bot.approves != tt.wantApproves || bot.bans != tt.wantBans || bot.declines != tt.wantDeclines {
				t.Errorf("actions = approve %d, ban %d, decline %d; want %d, %d, %d",
					bot.approves, bot.bans, bot.declines, tt.wantApproves, tt.wantBans, tt.wantDeclines)
			}
			if len(bot.callbackAnswers) != 1 {
				t.Fatalf("callback answers = %d, want 1", len(bot.callbackAnswers))
			}
			if got, want := bot.callbackAnswers[0].Text, tt.wantText(v); got != want {
				t.Errorf("callback text = %q, want in-progress text %q", got, want)
			}
			if bot.deletes != 1 {
				t.Errorf("successful action deleted %d challenge messages, want 1", bot.deletes)
			}
			if _, retained := v.pend[key]; retained {
				t.Error("successful action retained its challenge evidence")
			}
		})
	}
}

func TestOnAdminActionTelegramFailureAfterAcknowledgement(t *testing.T) {
	const gid, uid, adminID = int64(-100), int64(5), int64(9)
	errTelegram := errors.New("Forbidden: test transport failure")
	tests := []struct {
		name           string
		action         string
		adminLogChatID int64
		telegramMethod string
		newBot         func() *fakeVerifyBot
		wantCallback   func(*Service) string
		wantAlert      func(*Service, error) string
		wantAlertChat  int64
		wantApproves   int
		wantBans       int
		wantDeclines   int
	}{
		{
			name:           "approval failure falls back to the group",
			action:         "pass",
			telegramMethod: "approveChatJoinRequest",
			newBot:         func() *fakeVerifyBot { return &fakeVerifyBot{approveErr: errTelegram} },
			wantCallback:   func(_ *Service) string { return i18n.Messages.Verification.Admin.Approving.For(i18n.LangZH) },
			wantAlert: func(v *Service, err error) string {
				return v.messages.Verification.Admin.ApproveFailed.Render(i18n.LangZH, uid, gid, err)
			},
			wantAlertChat: gid,
			wantApproves:  1,
		},
		{
			name:           "ban failure uses the configured admin log",
			action:         "ban",
			adminLogChatID: -999,
			telegramMethod: "banChatMember",
			newBot:         func() *fakeVerifyBot { return &fakeVerifyBot{banErr: errTelegram} },
			wantCallback: func(v *Service) string {
				duration := verificationBanDurationText(v.messages, i18n.LangZH, v.verificationBanDuration(gid))
				return v.messages.Verification.Admin.Banning.Render(i18n.LangZH, duration)
			},
			wantAlert: func(v *Service, err error) string {
				return v.messages.Verification.Admin.BanFailed.Render(i18n.LangZH, uid, gid, err)
			},
			wantAlertChat: -999,
			wantBans:      1,
		},
		{
			name:           "ban failure falls back to the group without an admin log",
			action:         "ban",
			telegramMethod: "banChatMember",
			newBot:         func() *fakeVerifyBot { return &fakeVerifyBot{banErr: errTelegram} },
			wantCallback: func(v *Service) string {
				duration := verificationBanDurationText(v.messages, i18n.LangZH, v.verificationBanDuration(gid))
				return v.messages.Verification.Admin.Banning.Render(i18n.LangZH, duration)
			},
			wantAlert: func(v *Service, err error) string {
				return v.messages.Verification.Admin.BanFailed.Render(i18n.LangZH, uid, gid, err)
			},
			wantAlertChat: gid,
			wantBans:      1,
		},
		{
			name:           "decline failure after a confirmed ban retains the evidence",
			action:         "ban",
			telegramMethod: "declineChatJoinRequest",
			newBot:         func() *fakeVerifyBot { return &fakeVerifyBot{declineErr: errTelegram} },
			wantCallback: func(v *Service) string {
				duration := verificationBanDurationText(v.messages, i18n.LangZH, v.verificationBanDuration(gid))
				return v.messages.Verification.Admin.Banning.Render(i18n.LangZH, duration)
			},
			wantAlert: func(v *Service, err error) string {
				return v.messages.Verification.Admin.DeclineFailed.Render(i18n.LangZH, uid, gid, err)
			},
			wantAlertChat: gid,
			wantBans:      1,
			wantDeclines:  1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := newTestService(&config.Config{AdminLogChatID: tt.adminLogChatID, BanSeconds: 3600})
			key := pkey{gid, uid}
			p := livePending(42)
			v.pend[key] = p
			bot := tt.newBot()
			bot.member = &telego.ChatMemberAdministrator{Status: telego.MemberStatusAdministrator}
			update := telego.Update{CallbackQuery: &telego.CallbackQuery{
				ID:   "admin-failure",
				From: telego.User{ID: adminID},
				Data: AdminCallbackPrefix + tt.action + ":-100:5",
			}}

			runFakeHandler(t, newAPITestBot(t, bot), v.OnAdminAction, update)

			if bot.approves != tt.wantApproves || bot.bans != tt.wantBans || bot.declines != tt.wantDeclines {
				t.Errorf("actions = approve %d, ban %d, decline %d; want %d, %d, %d",
					bot.approves, bot.bans, bot.declines, tt.wantApproves, tt.wantBans, tt.wantDeclines)
			}
			if len(bot.callbackAnswers) != 1 {
				t.Fatalf("callback answers = %d, want 1", len(bot.callbackAnswers))
			}
			if got, want := bot.callbackAnswers[0].Text, tt.wantCallback(v); got != want {
				t.Errorf("callback text = %q, want truthful catalogue acknowledgement %q", got, want)
			}
			if bot.callbackAnswers[0].ShowAlert {
				t.Error("in-progress callback acknowledgement must not claim a terminal alert")
			}
			if cur, ok := v.pend[key]; !ok || cur != p || p.done || p.timer == nil {
				t.Fatalf("failure pending = %+v, want original evidence reopened and re-armed", cur)
			}
			t.Cleanup(func() { p.timer.Stop() })
			if _, claimed := v.terminal[key]; claimed {
				t.Error("failed action left the pending terminally claimed")
			}
			if bot.deletes != 0 || p.groupMsgID != 42 {
				t.Errorf("failure cleanup = deletes:%d message:%d, want retained evidence", bot.deletes, p.groupMsgID)
			}
			if bot.sends != 2 {
				t.Fatalf("failure messages = %d, want one operator alert and one group result", bot.sends)
			}
			handlerErr := fmt.Errorf("telego: %s: internal execution: request call: %w", tt.telegramMethod, errTelegram)
			wantAlert := tt.wantAlert(v, handlerErr)
			if bot.sendChats[0] != tt.wantAlertChat || bot.sendTexts[0] != wantAlert {
				t.Errorf("operator alert = chat %d text %q, want chat %d catalogue failure %q",
					bot.sendChats[0], bot.sendTexts[0], tt.wantAlertChat, wantAlert)
			}
			wantGroupResult := v.messages.Verification.Admin.ActionFailed.For(i18n.LangZH)
			if bot.sendChats[1] != gid || bot.sendTexts[1] != wantGroupResult {
				t.Errorf("group result = chat %d text %q, want chat %d catalogue result %q",
					bot.sendChats[1], bot.sendTexts[1], gid, wantGroupResult)
			}
		})
	}
}

func TestFailureCopyNamesDecisionAndRetry(t *testing.T) {
	const gid = int64(-100)
	v := newTestService(&config.Config{
		BanSeconds:         86400,
		VerifyMaxFails:     3,
		VerifyRetrySeconds: 600,
	})

	retry := v.wrongAnswerText(gid, i18n.LangZH, gateRequest, false)
	wantRetry := v.messages.Verification.Result.WrongRetry.Render(i18n.LangZH, 600)
	if retry != wantRetry {
		t.Errorf("wrong-answer retry = %q, want catalogue result %q", retry, wantRetry)
	}
	banned := v.wrongAnswerText(gid, i18n.LangZH, gateRequest, true)
	duration := verificationBanDurationText(v.messages, i18n.LangZH, v.verificationBanDuration(gid))
	wantBanned := i18n.Messages.Verification.Result.WrongBanned.Render(i18n.LangZH, duration)
	if banned != wantBanned {
		t.Errorf("ban result = %q, want catalogue result %q", banned, wantBanned)
	}
	agent := v.agentCaughtText(gid, i18n.LangEN, gateRequest, false)
	wantAgent := v.messages.Verification.Result.AICaught.Render(i18n.LangEN, 600)
	if agent != wantAgent {
		t.Errorf("automated-agent result = %q, want catalogue result %q", agent, wantAgent)
	}
}

func TestBannedResultTextIgnoresConfiguredThreshold(t *testing.T) {
	const gid = int64(-100)
	lowerThreshold := newTestService(&config.Config{BanSeconds: 86400, VerifyMaxFails: 3})
	higherThreshold := newTestService(&config.Config{BanSeconds: 86400, VerifyMaxFails: 4})

	for _, l := range i18n.Languages() {
		got := lowerThreshold.wrongAnswerText(gid, l, gateRequest, true)
		want := higherThreshold.wrongAnswerText(gid, l, gateRequest, true)
		if got != want {
			t.Errorf("%s banned result changed with threshold: got %q, want %q", l, got, want)
		}
	}
}

func TestApproveSuccess(t *testing.T) {
	v := newTestService(&config.Config{})
	key := pkey{-100, 5}
	v.pend[key] = livePending(42)
	v.vfail[key] = &vfailRec{count: 2, last: time.Now()} // had strikes; approve should clear them
	fb := &fakeVerifyBot{}
	if !v.approve(context.Background(), fb, -100, 5) {
		t.Fatal("approve should return true on success")
	}
	if fb.approves != 1 {
		t.Errorf("ApproveChatJoinRequest should be called once, got %d", fb.approves)
	}
	if _, ok := v.pend[key]; ok {
		t.Error("the pending should be consumed after a successful approve")
	}
	if _, ok := v.vfail[key]; ok {
		t.Error("a successful approve should clear the user's verify-fail strikes")
	}
	if fb.bans != 0 {
		t.Error("approve must never ban")
	}
}

func TestSettlementDeletesGroupAndPrivateChallenges(t *testing.T) {
	const gid, uid, adminID = int64(-100), int64(5), int64(9)
	tests := []struct {
		name string
		run  func(*testing.T, *Service, *fakeVerifyBot)
	}{
		{
			name: "applicant approval",
			run: func(t *testing.T, v *Service, bot *fakeVerifyBot) {
				t.Helper()
				if !v.approve(context.Background(), bot, gid, uid) {
					t.Fatal("applicant approval did not settle")
				}
			},
		},
		{
			name: "applicant decline",
			run: func(t *testing.T, v *Service, bot *fakeVerifyBot) {
				t.Helper()
				outcome, _ := v.decline(context.Background(), bot, gid, uid, "n", "wrong answer")
				handled, settled := outcome != declineNoPending, outcome.settled()
				if !handled || !settled {
					t.Fatalf("applicant decline = handled %t settled %t, want both true", handled, settled)
				}
			},
		},
		{
			name: "administrator approval",
			run: func(t *testing.T, v *Service, bot *fakeVerifyBot) {
				t.Helper()
				bot.member = &telego.ChatMemberAdministrator{Status: telego.MemberStatusAdministrator}
				runFakeHandler(t, newAPITestBot(t, bot), v.OnAdminAction, telego.Update{CallbackQuery: &telego.CallbackQuery{
					ID: "admin-pass", From: telego.User{ID: adminID}, Data: AdminCallbackPrefix + "pass:-100:5",
				}})
			},
		},
		{
			name: "administrator decline and ban",
			run: func(t *testing.T, v *Service, bot *fakeVerifyBot) {
				t.Helper()
				bot.member = &telego.ChatMemberAdministrator{Status: telego.MemberStatusAdministrator}
				runFakeHandler(t, newAPITestBot(t, bot), v.OnAdminAction, telego.Update{CallbackQuery: &telego.CallbackQuery{
					ID: "admin-ban", From: telego.User{ID: adminID}, Data: AdminCallbackPrefix + "ban:-100:5",
				}})
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := newTestService(&config.Config{VerifyMaxFails: 3})
			v.pend[pkey{gid, uid}] = &pending{
				nonce: "n", deadline: time.Now().Add(time.Hour), groupMsgID: 42, privateMsgID: 43,
			}
			bot := newFakeVerifyBot()
			bot.deleteErrAt = map[int]error{1: errors.New("group challenge already gone")}

			tt.run(t, v, bot)

			if bot.deletes != 2 || !reflect.DeepEqual(bot.deletedChats, []int64{gid, uid}) ||
				!reflect.DeepEqual(bot.deletedMessageIDs, []int{42, 43}) {
				t.Fatalf("settlement cleanup = chats %v messages %v, want both challenges despite first delete failure",
					bot.deletedChats, bot.deletedMessageIDs)
			}
		})
	}
}

func TestApproveFailureReopens(t *testing.T) {
	v := newTestService(&config.Config{})
	key := pkey{-100, 5}
	p := livePending(42)
	v.pend[key] = p
	fb := &fakeVerifyBot{approveErr: errors.New("Forbidden: not enough rights")}
	if v.approve(context.Background(), fb, -100, 5) {
		t.Fatal("approve should return false when ApproveChatJoinRequest fails")
	}
	if cur, ok := v.pend[key]; !ok || cur != p {
		t.Error("a failed approve must keep the pending (retryable), not strand the applicant")
	}
	if p.done {
		t.Error("a failed approve must re-open the pending (done=false) so it can retry / time out")
	}
	if fb.bans != 0 {
		t.Error("a failed approve must never ban the user")
	}
	if p.timer != nil {
		p.timer.Stop() // reopenPending re-armed a (far-future) timer; tidy
	}
}

func TestDeclineBelowThreshold(t *testing.T) {
	v := newTestService(&config.Config{VerifyMaxFails: 3})
	key := pkey{-100, 5}
	v.pend[key] = livePending(42)
	fb := &fakeVerifyBot{}
	outcome, banned := v.decline(context.Background(), fb, -100, 5, "n", "wrong answer")
	handled, settled := outcome != declineNoPending, outcome.settled()
	if !handled || !settled || banned {
		t.Fatalf("first failure should settle the decline without a ban: handled=%v settled=%v banned=%v", handled, settled, banned)
	}
	if fb.declines != 1 || fb.bans != 0 {
		t.Errorf("below threshold: want 1 decline + 0 bans, got declines=%d bans=%d", fb.declines, fb.bans)
	}
	if r := v.vfail[key]; r == nil || r.count != 1 {
		t.Errorf("a strike should be recorded, got %+v", r)
	}
	if _, ok := v.pend[key]; ok {
		t.Error("decline should consume the pending")
	}
}

func TestDeclineAutoBan(t *testing.T) {
	v := newTestService(&config.Config{VerifyMaxFails: 1}) // the first failure trips the auto-ban
	key := pkey{-100, 5}
	v.pend[key] = livePending(42)
	fb := &fakeVerifyBot{}
	outcome, banned := v.decline(context.Background(), fb, -100, 5, "n", "wrong answer")
	handled, settled := outcome != declineNoPending, outcome.settled()
	if !handled || !settled || !banned {
		t.Fatalf("threshold decline = handled %v, settled %v, banned %v; want all true", handled, settled, banned)
	}
	if fb.bans != 1 {
		t.Errorf("BanChatMember should be called once, got %d", fb.bans)
	}
	if _, ok := v.vfail[key]; ok {
		t.Error("strikes should be cleared after a successful auto-ban")
	}
}

func TestBanApplicant(t *testing.T) {
	v := newTestService(&config.Config{})
	key := pkey{-100, 5}
	v.pend[key] = livePending(42)
	fb := &fakeVerifyBot{}
	handled, banned := v.banApplicant(context.Background(), fb, -100, 5)
	if !handled || !banned {
		t.Fatalf("banApplicant should decline + ban: handled=%v banned=%v", handled, banned)
	}
	if fb.declines != 1 || fb.bans != 1 {
		t.Errorf("want 1 decline + 1 ban, got declines=%d bans=%d", fb.declines, fb.bans)
	}
	if _, ok := v.pend[key]; ok {
		t.Error("banApplicant should consume the pending")
	}

	failed := livePending(0)
	v.pend[key] = failed
	fbFail := &fakeVerifyBot{banErr: errors.New("not enough rights")}
	if _, banned := v.banApplicant(context.Background(), fbFail, -100, 5); banned {
		t.Error("a failed BanChatMember must report banned=false (honest feedback)")
	}
	if cur, ok := v.pend[key]; !ok || cur != failed || failed.done || failed.timer == nil {
		t.Fatalf("failed ban pending = %+v, want retryable evidence", cur)
	}
	if fbFail.declines != 0 || fbFail.deletes != 0 {
		t.Errorf("failed ban must not decline or delete evidence, got declines=%d deletes=%d", fbFail.declines, fbFail.deletes)
	}
	t.Cleanup(func() { failed.timer.Stop() })
}

func TestClaimThenExecuteApprove(t *testing.T) {
	v := newTestService(&config.Config{})
	key := pkey{-100, 5}
	v.pend[key] = livePending(42)

	p, ok := v.claimPending(-100, 5)
	if !ok {
		t.Fatal("claimPending should claim a live pending")
	}
	if cur, ok := v.pend[key]; !ok || cur != p || !p.done {
		t.Fatal("claimPending must KEEP the entry in the map, marked done (so a failed approve can reopen it)")
	}
	if _, ok := v.claimPending(-100, 5); ok {
		t.Error("an already-claimed pending must not be re-claimable (a timer/second callback can't double-act)")
	}
	fb := &fakeVerifyBot{}
	if v.executeApprove(context.Background(), fb, -100, 5, p) != approveConfirmed {
		t.Fatal("executeApprove should succeed")
	}
	if fb.approves != 1 {
		t.Errorf("want 1 ApproveChatJoinRequest, got %d", fb.approves)
	}
	if _, ok := v.pend[key]; ok {
		t.Error("a successful executeApprove should remove the pending")
	}
}

func TestTerminalActionBlocksReapplication(t *testing.T) {
	tests := []struct {
		name    string
		action  string
		started func(*blockingTerminalBot) <-chan struct{}
	}{
		{name: "approval in flight", action: "approve", started: func(b *blockingTerminalBot) <-chan struct{} { return b.approveStarted }},
		{name: "decline in flight", action: "decline", started: func(b *blockingTerminalBot) <-chan struct{} { return b.declineStarted }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const gid, uid = int64(-100), int64(5)
			key := pkey{gid, uid}
			v := newTestService(&config.Config{TimeoutSeconds: 3600, VerifyMaxFails: 3})
			old := &pending{nonce: "old", deadline: time.Now().Add(time.Hour)}
			v.pend[key] = old
			bot := newBlockingTerminalBot()
			result := make(chan bool, 1)
			go func() {
				if tt.action == "approve" {
					p, ok := v.claimPendingNonce(gid, uid, old.nonce)
					result <- ok && v.executeApprove(context.Background(), bot, gid, uid, p) == approveConfirmed
					return
				}
				declineOut, _ := v.decline(context.Background(), bot, gid, uid, old.nonce, "wrong answer")
				handled := declineOut != declineNoPending
				result <- handled
			}()
			select {
			case <-tt.started(bot):
			case <-time.After(time.Second):
				t.Fatal("terminal Telegram call did not start")
			}

			replacement := &pending{nonce: "replacement"}
			if _, status := v.startPending(bot, gid, uid, replacement); status != pendingBlockedTerminal {
				t.Fatalf("startPending status = %v, want pendingBlockedTerminal", status)
			}
			if v.pend[key] == replacement {
				t.Fatal("re-application replaced a pending while its terminal action was in flight")
			}

			close(bot.release)
			select {
			case ok := <-result:
				if !ok {
					t.Fatal("terminal action did not complete")
				}
			case <-time.After(time.Second):
				t.Fatal("terminal action did not return")
			}

			next := &pending{nonce: "next"}
			if _, status := v.startPending(bot, gid, uid, next); status != pendingStarted {
				t.Fatalf("startPending after terminal completion = %v, want pendingStarted", status)
			}
			next.timer.Stop()
		})
	}
}

func TestBlockedDeclineCountsStrikeAtClaimTime(t *testing.T) {
	const gid, uid = int64(-100), int64(5)
	failedAt := time.Unix(2_000_000_000, 0)
	now := failedAt
	v := newTestService(&config.Config{VerifyMaxFails: 2, TimeoutSeconds: 3600})
	v.timeNow = func() time.Time { return now }
	key := pkey{gid, uid}
	p := &pending{nonce: "current", deadline: failedAt.Add(time.Hour)}
	v.pend[key] = p
	v.vfail[key] = &vfailRec{count: 1, last: failedAt.Add(-verifyFailWindow + 10*time.Second)}
	bot := newBlockingTerminalBot()
	bot.release = make(chan struct{}, 1)
	type outcome struct {
		handled bool
		settled bool
		banned  bool
	}
	result := make(chan outcome, 1)
	go func() {
		got, banned := v.decline(context.Background(), bot, gid, uid, p.nonce, "wrong answer")
		result <- outcome{handled: got != declineNoPending, settled: got.settled(), banned: banned}
	}()
	select {
	case <-bot.declineStarted:
	case <-time.After(time.Second):
		t.Fatal("decline did not reach the blocking Telegram call")
	}
	defer func() {
		select {
		case bot.release <- struct{}{}:
		default:
		}
	}()

	now = failedAt.Add(20 * time.Second)
	bot.release <- struct{}{}
	select {
	case got := <-result:
		if !got.handled || !got.settled || !got.banned {
			t.Fatalf("blocked decline outcome = %+v, want handled, settled, and banned", got)
		}
	case <-time.After(time.Second):
		t.Fatal("decline did not finish after release")
	}
	if bot.bans != 1 {
		t.Fatalf("automatic bans = %d, want 1", bot.bans)
	}
}

func TestRemoveGroupCancelsBlockedDeclineWithoutStrike(t *testing.T) {
	const gid, uid = int64(-100), int64(6)
	v := newTestService(&config.Config{VerifyMaxFails: 1, TimeoutSeconds: 3600})
	key := pkey{gid, uid}
	p := &pending{nonce: "current", deadline: time.Now().Add(time.Hour)}
	v.pend[key] = p
	bot := newBlockingTerminalBot()
	bot.release = make(chan struct{}, 1)
	type outcome struct {
		handled bool
		settled bool
		banned  bool
	}
	result := make(chan outcome, 1)
	go func() {
		got, banned := v.decline(context.Background(), bot, gid, uid, p.nonce, "wrong answer")
		result <- outcome{handled: got != declineNoPending, settled: got.settled(), banned: banned}
	}()
	select {
	case <-bot.declineStarted:
	case <-time.After(time.Second):
		t.Fatal("decline did not reach the blocking Telegram call")
	}
	defer func() {
		select {
		case bot.release <- struct{}{}:
		default:
		}
	}()

	v.RemoveGroup(gid)
	bot.release <- struct{}{}
	select {
	case got := <-result:
		if !got.handled || !got.settled || got.banned {
			t.Fatalf("cancelled decline outcome = %+v, want settled without a ban", got)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled decline did not finish after release")
	}
	if _, struck := v.vfail[key]; struck {
		t.Error("group removal allowed an in-flight decline to record a strike")
	}
	if v.declined != 0 || bot.bans != 0 {
		t.Fatalf("group removal recorded decline/ban counters %d/%d, want 0/0", v.declined, bot.bans)
	}
}

func TestConsumeThenExecuteBan(t *testing.T) {
	v := newTestService(&config.Config{})
	key := pkey{-100, 5}
	v.pend[key] = livePending(42)

	p, ok := v.consume(-100, 5)
	if !ok {
		t.Fatal("consume should claim a live pending")
	}
	if current, ok := v.pend[key]; !ok || current != p || !p.done {
		t.Error("consume must keep the claimed pending recoverable until settlement")
	}
	fb := &fakeVerifyBot{}
	if banned := v.executeBan(context.Background(), fb, -100, 5, p); !banned {
		t.Fatal("executeBan should report banned=true on success")
	}
	if fb.declines != 1 || fb.bans != 1 {
		t.Errorf("want 1 decline + 1 ban, got declines=%d bans=%d", fb.declines, fb.bans)
	}
}

func TestFailAlertFallsBackToGroup(t *testing.T) {
	v := newTestService(&config.Config{}) // AdminLogChatID == 0
	fb := &fakeVerifyBot{}
	v.failAlert(context.Background(), fb, -555, "x")
	if fb.lastSendChat != -555 {
		t.Errorf("with no admin-log chat, failAlert should post to the group, got chat %d", fb.lastSendChat)
	}
	// The destination is a live setting: changing it in the panel must take effect at once.
	target := int64(-999)
	if _, err := v.settings.CommitGlobal(v.settings.Global().Revision(), store.GlobalOverrides{AdminLogChatID: &target}); err != nil {
		t.Fatal(err)
	}
	v.failAlert(context.Background(), fb, -555, "y")
	if fb.lastSendChat != -999 {
		t.Errorf("with an admin-log chat set, failAlert should post there, got chat %d", fb.lastSendChat)
	}
}

func TestApproveClaimBlocksTimeoutDecline(t *testing.T) {
	v := newTestService(&config.Config{})
	key := pkey{gid: -100, uid: 5}
	v.pend[key] = &pending{nonce: "abc", deadline: time.Now().Add(time.Hour)}

	// approve claims the pending (marks it done) before its network call.
	v.mu.Lock()
	v.pend[key].done = true
	v.mu.Unlock()

	// A second claim must refuse the pending while approval is in flight.
	if _, ok := v.claimPendingNonce(-100, 5, "abc"); ok {
		t.Error("a claimed pending must not be claimable again; otherwise a verified user could be declined or struck")
	}
}

func TestStopForShutdownFreezesPending(t *testing.T) {
	v := newTestService(&config.Config{})
	key := pkey{gid: -100, uid: 42}
	tmr := time.AfterFunc(time.Hour, func() {}) // a live timer that stopForShutdown should stop
	v.pend[key] = &pending{nonce: "n1", deadline: time.Now().Add(time.Hour), timer: tmr}

	v.stopForShutdown()

	if !v.shuttingDown {
		t.Error("stopForShutdown must set the shutting-down flag")
	}
	if _, ok := v.pend[key]; !ok {
		t.Fatal("stopForShutdown must NOT remove pendings — they must persist across the restart")
	}
	// A timer firing now reaches a claim helper, which must refuse during shutdown.
	if _, ok := v.claimPendingNonce(-100, 42, "n1"); ok {
		t.Error("claimPendingNonce must refuse during shutdown")
	}
	if _, ok := v.pend[key]; !ok {
		t.Error("the pending must remain intact after the refused claim")
	}
}

func TestTrustedBypass(t *testing.T) {
	ctx := context.Background()
	const gid, src, uid = int64(-1003265952923), int64(-1001163306055), int64(5)
	mkV := func() *Service {
		v := newTestService(&config.Config{Groups: []config.GroupConfig{{ID: gid, TrustedMemberGroupIDs: []int64{src}}}})
		v.loc = time.UTC
		return v
	}

	v := mkV()
	v.vfail[pkey{gid, uid}] = &vfailRec{count: 1, last: time.Now()} // a prior failed-verify strike
	member := newFakeVerifyBot()
	member.memberByID = map[int64]telego.ChatMember{uid: &telego.ChatMemberMember{}}
	if handled, trusted := v.tryTrustedBypass(ctx, member, gid, uid); !handled || !trusted {
		t.Fatalf("a member must be approved: handled=%v trusted=%v", handled, trusted)
	}
	if member.approves != 1 {
		t.Errorf("must approve exactly once, got %d", member.approves)
	}
	if _, still := v.vfail[pkey{gid, uid}]; still {
		t.Error("a successful bypass must clearVerifyFails (clean slate)")
	}

	left := newFakeVerifyBot()
	left.memberByID = map[int64]telego.ChatMember{uid: &telego.ChatMemberLeft{}}
	if handled, trusted := mkV().tryTrustedBypass(ctx, left, gid, uid); handled || trusted || left.approves != 0 {
		t.Errorf("a non-member must be (false,false): handled=%v trusted=%v", handled, trusted)
	}

	errBot := newFakeVerifyBot()
	errBot.memberErr = errors.New("bot not in the trusted group")
	if handled, trusted := mkV().tryTrustedBypass(ctx, errBot, gid, uid); handled || trusted || errBot.approves != 0 {
		t.Errorf("a lookup error must be (false,false) — fail-closed: handled=%v trusted=%v", handled, trusted)
	}

	fail := newFakeVerifyBot()
	fail.memberByID = map[int64]telego.ChatMember{uid: &telego.ChatMemberMember{}}
	fail.approveErr = errors.New("no rights")
	if handled, trusted := mkV().tryTrustedBypass(ctx, fail, gid, uid); handled || !trusted {
		t.Errorf("a confirmed member with a failed approve must be (false,true): handled=%v trusted=%v", handled, trusted)
	}

	plain := newTestService(&config.Config{Groups: []config.GroupConfig{{ID: gid}}})
	if handled, trusted := plain.tryTrustedBypass(ctx, newFakeVerifyBot(), gid, uid); handled || trusted {
		t.Errorf("no trusted config -> (false,false): handled=%v trusted=%v", handled, trusted)
	}
}

func TestJoinGate(t *testing.T) {
	ctx := context.Background()
	const gid, src, uid = int64(-1003265952923), int64(-1001163306055), int64(5)
	mkV := func() *Service {
		v := newTestService(&config.Config{VerifyRetrySeconds: 600, Groups: []config.GroupConfig{{ID: gid, TrustedMemberGroupIDs: []int64{src}}}})
		v.loc = time.UTC
		return v
	}
	cooldown := func(v *Service) { v.vfail[pkey{gid, uid}] = &vfailRec{count: 1, last: time.Now()} }

	// trusted member IN cooldown -> bypassed (handled), approved, NOT declined
	v := mkV()
	cooldown(v)
	bot := newFakeVerifyBot()
	bot.memberByID = map[int64]telego.ChatMember{uid: &telego.ChatMemberMember{}}
	if !v.joinGate(ctx, bot, gid, uid, i18n.LangEN) {
		t.Error("a trusted member in cooldown must be handled (bypassed)")
	}
	if bot.approves != 1 || bot.declines != 0 {
		t.Errorf("a trusted member in cooldown must be APPROVED, not declined: approves=%d declines=%d", bot.approves, bot.declines)
	}

	// trusted member in cooldown whose approve FAILS -> not handled (proceed to quiz), NOT declined
	vf := mkV()
	cooldown(vf)
	failBot := newFakeVerifyBot()
	failBot.memberByID = map[int64]telego.ChatMember{uid: &telego.ChatMemberMember{}}
	failBot.approveErr = errors.New("no rights")
	if vf.joinGate(ctx, failBot, gid, uid, i18n.LangEN) {
		t.Error("a confirmed trusted member whose approve failed must proceed to verification (not be handled by the cooldown)")
	}
	if failBot.declines != 0 {
		t.Errorf("a confirmed trusted member must NOT be cooldown-declined, got %d declines", failBot.declines)
	}

	// NON-member in cooldown -> declined (the ordinary cooldown applies)
	vn := mkV()
	cooldown(vn)
	nonBot := newFakeVerifyBot()
	nonBot.memberByID = map[int64]telego.ChatMember{uid: &telego.ChatMemberLeft{}}
	if !vn.joinGate(ctx, nonBot, gid, uid, i18n.LangEN) {
		t.Error("a non-member in cooldown must be handled (declined)")
	}
	if nonBot.declines != 1 || nonBot.approves != 0 {
		t.Errorf("a non-member in cooldown must be DECLINED: declines=%d approves=%d", nonBot.declines, nonBot.approves)
	}

	// non-member, NO cooldown -> proceed to the challenge (not handled)
	vp := mkV()
	pBot := newFakeVerifyBot()
	pBot.memberByID = map[int64]telego.ChatMember{uid: &telego.ChatMemberLeft{}}
	if vp.joinGate(ctx, pBot, gid, uid, i18n.LangEN) {
		t.Error("a non-member with no cooldown must proceed to the challenge (not handled)")
	}
}

func TestCooldownReapplicationDMsCurrentWait(t *testing.T) {
	const (
		gid      = int64(-1003265952923)
		uid      = int64(5)
		cooldown = 600
	)
	v := newTestService(&config.Config{
		Groups:             []config.GroupConfig{{ID: gid}},
		GroupIDs:           []int64{gid},
		VerifyRetrySeconds: cooldown,
	})
	v.vfail[pkey{gid, uid}] = &vfailRec{count: 1, last: time.Now()}
	caller := newFakeVerifyBot()
	update := telego.Update{ChatJoinRequest: &telego.ChatJoinRequest{
		Chat: telego.Chat{ID: gid},
		From: telego.User{ID: uid, FirstName: "Applicant", LanguageCode: "zh-Hant"},
	}}

	runFakeHandler(t, newAPITestBot(t, caller), v.OnJoinRequest, update)

	if caller.declines != 1 {
		t.Fatalf("cooldown reapplication decline calls = %d, want 1", caller.declines)
	}
	want := i18n.Messages.Verification.Result.CooldownActive.Render(i18n.LangZHHant, cooldown)
	if caller.sends != 1 || caller.lastSendChat != uid || caller.lastSendText != want {
		t.Fatalf("cooldown DM sends/chat/text = %d/%d/%q, want one applicant catalogue message %q", caller.sends, caller.lastSendChat, caller.lastSendText, want)
	}
}

func TestStrikesUser(t *testing.T) {
	tests := []struct {
		reason string
		want   bool
	}{
		{reason: "timeout", want: true},
		{reason: "wrong answer", want: true},
		{reason: "something-else", want: true},
		{reason: "approve-retry"},
		{reason: "decline-retry"},
		{reason: "restart-lapsed"},
		{reason: "recovered"},
		{reason: "challenge-post-failed"},
	}
	for _, tt := range tests {
		if got := strikesUser(tt.reason); got != tt.want {
			t.Errorf("strikesUser(%q) = %v, want %v", tt.reason, got, tt.want)
		}
	}
}

func TestDeclineNoStrike(t *testing.T) {
	const gid, uid = int64(-100), int64(5)
	mkV := func() *Service {
		v := newTestService(&config.Config{})
		v.loc = time.UTC
		return v
	}
	for _, reason := range []string{"approve-retry", "decline-retry", "restart-lapsed", "challenge-post-failed"} {
		v := mkV()
		v.pend[pkey{gid, uid}] = &pending{nonce: "n", deadline: time.Now().Add(time.Hour)}
		fb := &fakeVerifyBot{}
		v.decline(context.Background(), fb, gid, uid, "n", reason)
		if _, struck := v.vfail[pkey{gid, uid}]; struck {
			t.Errorf("decline(%q) must NOT record a strike", reason)
		}
		if fb.declines != 1 {
			t.Errorf("decline(%q) should still decline the join request, got %d", reason, fb.declines)
		}
		if _, still := v.pend[pkey{gid, uid}]; still {
			t.Errorf("decline(%q) should still consume the pending", reason)
		}
	}
	// a genuine timeout still strikes
	v := mkV()
	v.pend[pkey{gid, uid}] = &pending{nonce: "n", deadline: time.Now().Add(time.Hour)}
	v.decline(context.Background(), &fakeVerifyBot{}, gid, uid, "n", "timeout")
	if r := v.vfail[pkey{gid, uid}]; r == nil || r.count != 1 {
		t.Errorf("decline(timeout) must record a strike, got %+v", r)
	}
}

func TestReopenPendingRestoresRetryable(t *testing.T) {
	v := newTestService(&config.Config{})
	key := pkey{gid: -100, uid: 5}
	p := &pending{nonce: "abc", deadline: time.Now().Add(time.Hour), done: true}
	v.pend[key] = p

	v.reopenPending(nil, -100, 5, p, "approve-retry") // bot unused: a 1h deadline means the re-armed timer won't fire in-test
	if p.done {
		t.Error("reopenPending should re-open the pending (done=false) for retry")
	}
	if p.timer == nil {
		t.Fatal("reopenPending should re-arm the timeout timer")
	}
	p.timer.Stop() // tidy: don't let it fire after the test

	// a pending already replaced by a newer request must NOT be re-opened.
	v.pend[key] = &pending{nonce: "new", deadline: time.Now().Add(time.Hour)}
	stale := &pending{nonce: "abc", deadline: time.Now().Add(time.Hour), done: true}
	v.reopenPending(nil, -100, 5, stale, "approve-retry")
	if !stale.done {
		t.Error("a replaced pending must not be re-opened")
	}
}

func TestDeclineFailureAlertsAdmins(t *testing.T) {
	const gid, uid = int64(-100), int64(5)
	v := newTestService(&config.Config{AdminLogChatID: -200})
	v.loc = time.UTC
	p := livePending(42)
	v.pend[pkey{gid, uid}] = p
	fb := &fakeVerifyBot{declineErr: errors.New("Forbidden: missing can_invite_users")}
	outcome, _ := v.decline(context.Background(), fb, gid, uid, "n", "wrong answer")
	handled, settled := outcome != declineNoPending, outcome.settled()
	if !handled || settled || fb.declines != 1 {
		t.Fatalf("decline result = handled %v, settled %v, calls %d; want true, false, 1", handled, settled, fb.declines)
	}
	if fb.sends != 1 || fb.lastSendChat != -200 {
		t.Fatalf("admin alert sends/chat = %d/%d, want 1/-200", fb.sends, fb.lastSendChat)
	}
	wantAlert := v.messages.Verification.Admin.DeclineFailed.Render(v.groupLanguage(gid), uid, gid, fb.declineErr)
	if got := fb.lastSendText; got != wantAlert {
		t.Errorf("admin alert = %q, want catalogue failure %q", got, wantAlert)
	}
	if p.timer != nil {
		p.timer.Stop()
	}
}

func TestDeclineFailureReopensWithoutStrikeAndUsesGroupAlertFallback(t *testing.T) {
	const gid, uid = int64(-100), int64(5)
	v := newTestService(&config.Config{VerifyMaxFails: 3})
	key := pkey{gid, uid}
	p := &pending{
		nonce:      "current",
		lang:       i18n.LangZHHant,
		correctIdx: 1,
		groupMsgID: 42,
		deadline:   time.Now().Add(time.Hour),
	}
	v.pend[key] = p
	caller := &fakeVerifyBot{declineErr: errors.New("Forbidden: missing can_invite_users")}
	update := telego.Update{CallbackQuery: &telego.CallbackQuery{
		ID:   "answer",
		From: telego.User{ID: uid},
		Data: "v:-100:5:current:0",
	}}

	runFakeHandler(t, newAPITestBot(t, caller), v.OnAnswer, update)

	if cur, ok := v.pend[key]; !ok || cur != p || cur.done || cur.timer == nil {
		t.Fatalf("failed decline pending = %+v, want the original request reopened and re-armed", cur)
	}
	t.Cleanup(func() {
		if p.timer != nil {
			p.timer.Stop()
		}
	})
	if _, struck := v.vfail[key]; struck {
		t.Error("a failed decline must not record a verification strike")
	}
	if caller.deletes != 0 {
		t.Errorf("a failed decline deleted %d group challenges, want 0", caller.deletes)
	}
	if caller.sends != 1 || caller.lastSendChat != gid {
		t.Errorf("failure-alert fallback sends/chat = %d/%d, want 1/%d", caller.sends, caller.lastSendChat, gid)
	}
	if len(caller.callbackAnswers) != 1 {
		t.Fatalf("callback answers = %d, want 1", len(caller.callbackAnswers))
	}
	got := caller.callbackAnswers[0].Text
	want := i18n.Messages.Verification.Result.DeclinePending.For(i18n.LangZHHant)
	if got != want {
		t.Errorf("decline failure callback = %q, want applicant catalogue warning %q", got, want)
	}
}

func TestSendQuizzesMarksPromptedOnlyAfterDelivery(t *testing.T) {
	tests := []struct {
		name      string
		bot       *fakeVerifyBot
		want      bool
		wantSends int
	}{
		{name: "rich delivered", bot: &fakeVerifyBot{}, want: true, wantSends: 1},
		{name: "simpler delivered", bot: &fakeVerifyBot{sendErr: errors.New("Bad Request: can't parse entities"), sendFailN: 1}, want: true, wantSends: 2},
		{name: "all renderings failed", bot: &fakeVerifyBot{sendErr: errors.New("Bad Request: can't parse entities"), sendFailN: 3}, want: false, wantSends: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const gid, uid = int64(-100), int64(5)
			v := newTestService(&config.Config{})
			p := &pending{mode: config.ModeKernel, lang: i18n.LangZH, qText: "kernel", nonce: "n", deadline: time.Now().Add(time.Hour)}
			v.pend[pkey{gid, uid}] = p
			v.sendQuizzes(context.Background(), tt.bot, uid)
			if p.prompted != tt.want {
				t.Errorf("prompted = %v, want %v", p.prompted, tt.want)
			}
			if tt.bot.sends != tt.wantSends {
				t.Errorf("SendMessage calls = %d, want %d", tt.bot.sends, tt.wantSends)
			}
		})
	}
}

func TestDeliveredKernelPromptSurvivesRestart(t *testing.T) {
	const gid, uid = int64(-100), int64(5)
	cfg := &config.Config{
		Groups:         []config.GroupConfig{{ID: gid}},
		GroupIDs:       []int64{gid},
		VerifyMode:     config.ModeKernel,
		TimeoutSeconds: 240,
	}
	path := t.TempDir() + "/pending.json"
	before := newTestService(cfg)
	before.statePath = path
	before.pend[pkey{gid, uid}] = &pending{
		mode:     config.ModeKernel,
		lang:     i18n.LangEN,
		qText:    kernelQuestion(&i18n.Messages, i18n.LangEN),
		nonce:    "n",
		deadline: time.Now().Add(time.Hour),
	}
	before.save()
	bot := newFakeVerifyBot()
	before.sendQuizzes(context.Background(), bot, uid)

	after := newTestService(cfg)
	after.statePath = path
	after.load(bot)
	t.Cleanup(after.stopForShutdown)
	answer := telego.Update{Message: &telego.Message{
		Chat: telego.Chat{ID: uid, Type: "private"},
		From: &telego.User{ID: uid},
		Text: "6.12.3",
	}}
	if !after.KernelAnswerDM(context.Background(), answer) {
		t.Fatal("a delivered prompt was not gradeable after restart")
	}
	after.gradeKernelAnswer(context.Background(), bot, gid, uid, answer.Message.Text)
	if bot.approves != 1 {
		t.Errorf("correct answer after restart produced %d approvals, want 1", bot.approves)
	}
}

func TestChallengeResendCapacityKeepsActiveCooldown(t *testing.T) {
	v := newTestService(&config.Config{})
	now := time.Now()
	const gid, activeUID = int64(-100), int64(1)
	v.challengeAt[pkey{gid, activeUID}] = now
	for uid := int64(2); uid <= challengeResendMapMax; uid++ {
		v.challengeAt[pkey{gid, uid}] = now.Add(-challengeResendCooldown - time.Second)
	}

	if !v.challengeResendOK(gid, challengeResendMapMax+1) {
		t.Fatal("new user was unexpectedly throttled")
	}
	if v.challengeResendOK(gid, activeUID) {
		t.Error("capacity event discarded an active challenge resend cooldown")
	}
}

func TestChannelAccessAlertPrunesExpiredChannels(t *testing.T) {
	v := newTestService(&config.Config{})
	now := time.Now()
	const activeChannel int64 = -1
	v.chanAlert[activeChannel] = now
	for channelID := int64(-2); channelID >= -100; channelID-- {
		v.chanAlert[channelID] = now.Add(-channelAccessAlertCooldown - time.Second)
	}

	v.channelAccessAlert(context.Background(), newFakeVerifyBot(), i18n.LangEN, -101)

	if _, ok := v.chanAlert[activeChannel]; !ok {
		t.Error("pruning discarded an active channel alert cooldown")
	}
	if len(v.chanAlert) != 2 {
		t.Errorf("channel alert throttle retained %d channels, want active and current channels only", len(v.chanAlert))
	}
}

func TestPendingCaps(t *testing.T) {
	tests := []struct {
		name       string
		fill       func(*Service)
		gid        int64
		uid        int64
		wantStatus pendingStartStatus
	}{
		{name: "below caps", fill: func(*Service) {}, gid: -100, uid: 1, wantStatus: pendingStarted},
		{name: "per-group cap", fill: func(v *Service) {
			for i := range pendingPerGroupCap {
				v.pend[pkey{-100, int64(i + 1)}] = &pending{}
			}
		}, gid: -100, uid: pendingPerGroupCap + 1, wantStatus: pendingBlockedCapacity},
		{name: "global cap", fill: func(v *Service) {
			for i := range pendingGlobalCap {
				gid := -int64(i/pendingPerGroupCap + 1)
				v.pend[pkey{gid, int64(i + 1)}] = &pending{}
			}
		}, gid: -999, uid: 1, wantStatus: pendingBlockedCapacity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := newTestService(&config.Config{TimeoutSeconds: 3600})
			tt.fill(v)
			p := &pending{nonce: "new"}
			_, status := v.startPending(&fakeVerifyBot{}, tt.gid, tt.uid, p)
			if status != tt.wantStatus {
				t.Fatalf("startPending status = %v, want %v", status, tt.wantStatus)
			}
			if status == pendingStarted {
				if p.timer == nil {
					t.Fatal("accepted pending must have an expiry timer")
				}
				p.timer.Stop()
				return
			}
			if p.timer != nil {
				t.Error("rejected pending must not arm an expiry timer")
			}
			if _, exists := v.pend[pkey{tt.gid, tt.uid}]; exists {
				t.Error("rejected pending must not enter the queue")
			}
		})
	}
}

func TestPendingCapAlertThrottled(t *testing.T) {
	tests := []struct {
		name       string
		adminLogID int64
		groupID    int64
		wantChatID int64
	}{
		{name: "configured admin log", adminLogID: -200, groupID: -100, wantChatID: -200},
		{name: "affected group fallback", groupID: -100, wantChatID: -100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := newTestService(&config.Config{AdminLogChatID: tt.adminLogID})
			fb := &fakeVerifyBot{}
			v.alertPendingCap(context.Background(), fb, tt.groupID)
			v.alertPendingCap(context.Background(), fb, -300)
			if fb.sends != 1 {
				t.Fatalf("two over-cap joins inside the cooldown sent %d alerts, want 1", fb.sends)
			}
			if fb.lastSendChat != tt.wantChatID {
				t.Errorf("alert chat = %d, want %d", fb.lastSendChat, tt.wantChatID)
			}
			v.mu.Lock()
			v.pendingCapAlertAt = time.Now().Add(-pendingCapAlertCooldown)
			v.mu.Unlock()
			v.alertPendingCap(context.Background(), fb, tt.groupID)
			if fb.sends != 2 {
				t.Errorf("an alert after the cooldown brought sends to %d, want 2", fb.sends)
			}
		})
	}
}

func TestRemoveGroupCancelsAllPendingStateWithoutStrikes(t *testing.T) {
	const removedGroup, otherGroup = int64(-100), int64(-200)
	v := newTestService(&config.Config{
		Groups:   []config.GroupConfig{{ID: removedGroup}, {ID: otherGroup}},
		GroupIDs: []int64{removedGroup, otherGroup},
	})
	liveKey := pkey{removedGroup, 1}
	claimedKey := pkey{removedGroup, 2}
	otherKey := pkey{otherGroup, 3}
	live := livePending(41)
	live.timer = time.AfterFunc(time.Hour, func() {})
	claimed := livePending(42)
	claimed.done = true
	claimed.timer = time.AfterFunc(time.Hour, func() {})
	other := livePending(43)
	other.timer = time.AfterFunc(time.Hour, func() {})
	t.Cleanup(func() { other.timer.Stop() })
	v.pend[liveKey] = live
	v.pend[claimedKey] = claimed
	v.pend[otherKey] = other
	v.terminal[claimedKey] = claimed

	remover, ok := any(v).(interface{ RemoveGroup(int64) })
	if !ok {
		t.Fatal("Service has no explicit group-removal transition")
	}
	remover.RemoveGroup(removedGroup)

	for _, key := range []pkey{liveKey, claimedKey} {
		if _, exists := v.pend[key]; exists {
			t.Errorf("removed group pending %v remains live", key)
		}
		if _, exists := v.terminal[key]; exists {
			t.Errorf("removed group terminal %v remains live", key)
		}
		if _, struck := v.vfail[key]; struck {
			t.Errorf("removing pending %v recorded a verification strike", key)
		}
	}
	if v.pend[otherKey] != other {
		t.Error("group removal changed another group's pending")
	}
	if live.timer.Stop() || claimed.timer.Stop() {
		t.Error("group removal left an expiry timer armed")
	}
}

func TestShutdownSnapshotKeepsBlockedExpirySettlement(t *testing.T) {
	const gid, uid = int64(-100), int64(5)
	v := newTestService(&config.Config{TimeoutSeconds: 3600, VerifyMaxFails: 3})
	v.statePath = t.TempDir() + "/pending.json"
	key := pkey{gid, uid}
	p := &pending{
		mode:               config.ModeKernel,
		lang:               i18n.LangEN,
		qText:              "question",
		nonce:              "blocked",
		name:               "Applicant",
		deadline:           time.Now().Add(time.Hour),
		challengeDelivered: true,
	}
	v.pend[key] = p
	bot := newBlockingTerminalBot()
	done := make(chan struct{})
	go func() {
		v.onExpiry(context.Background(), bot, gid, uid, p.nonce, p.epoch, "timeout")
		close(done)
	}()
	select {
	case <-bot.declineStarted:
	case <-time.After(time.Second):
		t.Fatal("expiry settlement did not reach the blocking decline")
	}
	defer func() {
		select {
		case bot.release <- struct{}{}:
		default:
		}
	}()

	v.Shutdown()
	var during []pendingRec
	if err := store.Load(v.statePath, &during); err != nil {
		t.Fatal(err)
	}
	if len(during) != 1 || during[0].GroupID != gid || during[0].UserID != uid {
		t.Fatalf("shutdown snapshot during settlement = %+v, want the claimed pending", during)
	}

	bot.release <- struct{}{}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("expiry settlement did not finish after release")
	}
	var after []pendingRec
	if err := store.Load(v.statePath, &after); err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("snapshot after confirmed settlement = %+v, want empty", after)
	}
}

func TestLateBothDeliveryDeletesAbandonedMessagesWithoutPromptingReplacement(t *testing.T) {
	const gid, uid = int64(-100), int64(701)
	v := newTestService(&config.Config{
		Groups:         []config.GroupConfig{{ID: gid}},
		GroupIDs:       []int64{gid},
		Lang:           "en",
		VerifyMode:     config.ModeKernel,
		TimeoutSeconds: 30,
		DeliveryMode:   config.DeliveryBoth,
	})
	update := telego.Update{ChatJoinRequest: &telego.ChatJoinRequest{
		Chat: telego.Chat{ID: gid},
		From: telego.User{ID: uid, FirstName: "Applicant", LanguageCode: "en"},
	}}
	release := make(chan struct{}, 1)
	first := newFakeVerifyBot()
	first.sendMessageID = 101
	first.sendStarted = make(chan struct{}, 1)
	first.releaseSend = release
	first.blockSendN = 2
	firstDone := make(chan struct{})
	go func() {
		runFakeHandler(t, newAPITestBot(t, first), v.OnJoinRequest, update)
		close(firstDone)
	}()
	select {
	case <-first.sendStarted:
	case <-time.After(time.Second):
		t.Fatal("first private challenge did not block in delivery")
	}
	defer func() {
		select {
		case release <- struct{}{}:
		default:
		}
		v.Shutdown()
	}()

	second := newFakeVerifyBot()
	second.sendErrAt = map[int]error{2: errors.New("connection reset after request write")}
	runFakeHandler(t, newAPITestBot(t, second), v.OnJoinRequest, update)
	key := pkey{gid, uid}
	v.mu.Lock()
	replacement := v.pend[key]
	v.mu.Unlock()
	if replacement == nil || replacement.prompted {
		t.Fatalf("replacement before old delivery completion = %+v, want unprompted", replacement)
	}

	release <- struct{}{}
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first private delivery did not finish after release")
	}
	v.mu.Lock()
	current := v.pend[key]
	v.mu.Unlock()
	if current != replacement || current.prompted {
		t.Fatalf("late old delivery changed replacement = %+v, want same unprompted pending", current)
	}
	if first.deletes != 2 || !reflect.DeepEqual(first.deletedChats, []int64{uid, gid}) ||
		!reflect.DeepEqual(first.deletedMessageIDs, []int{101, 101}) {
		t.Fatalf("late challenge cleanup = chats %v messages %v, want abandoned private and group messages",
			first.deletedChats, first.deletedMessageIDs)
	}
}

func TestPrivateDeliveryCompletedAfterExpiryIsDeleted(t *testing.T) {
	const gid, uid = int64(-100), int64(702)
	v := newTestService(&config.Config{
		Groups:         []config.GroupConfig{{ID: gid}},
		GroupIDs:       []int64{gid},
		Lang:           "en",
		VerifyMode:     config.ModeKernel,
		TimeoutSeconds: 30,
		DeliveryMode:   config.DeliveryDM,
	})
	update := telego.Update{ChatJoinRequest: &telego.ChatJoinRequest{
		Chat: telego.Chat{ID: gid},
		From: telego.User{ID: uid, FirstName: "Applicant", LanguageCode: "en"},
	}}
	release := make(chan struct{}, 1)
	caller := newFakeVerifyBot()
	caller.sendMessageID = 102
	caller.sendStarted = make(chan struct{}, 1)
	caller.releaseSend = release
	handlerDone := make(chan struct{})
	go func() {
		runFakeHandler(t, newAPITestBot(t, caller), v.OnJoinRequest, update)
		close(handlerDone)
	}()
	select {
	case <-caller.sendStarted:
	case <-time.After(time.Second):
		t.Fatal("private challenge did not block in delivery")
	}
	defer func() {
		select {
		case release <- struct{}{}:
		default:
		}
		v.Shutdown()
	}()

	key := pkey{gid, uid}
	v.mu.Lock()
	p := v.pend[key]
	if p == nil {
		v.mu.Unlock()
		t.Fatal("pending disappeared before manual expiry")
	}
	if p.timer != nil {
		p.timer.Stop()
	}
	nonce, epoch := p.nonce, p.epoch
	v.mu.Unlock()
	v.onExpiry(context.Background(), newFakeVerifyBot(), gid, uid, nonce, epoch, "challenge-post-failed")

	release <- struct{}{}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("private delivery did not finish after release")
	}
	if caller.deletes != 1 || caller.deletedChats[0] != uid || caller.deletedMessageIDs[0] != 102 {
		t.Fatalf("expired private message cleanup = chats %v messages %v, want [%d]/[102]",
			caller.deletedChats, caller.deletedMessageIDs, uid)
	}
}
