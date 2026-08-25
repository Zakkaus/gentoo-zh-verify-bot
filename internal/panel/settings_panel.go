package panel

import (
	"context"
	"errors"
	"log"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
)

type panelButton struct {
	text  string
	field string
	value string
	group int64
}

type eligibleGroup struct {
	id    int64
	title string
}

// OnSettings creates a user-bound session and replies with its private-chat deep link.
func (v *Panel) OnSettings(ctx *th.Context, update telego.Update) error {
	message := update.Message
	if message == nil || message.From == nil || v.settings == nil || !v.settings.IsGroup(message.Chat.ID) {
		return nil
	}
	requestCtx := ctx.Context()
	language := v.groupLanguage(message.Chat.ID)
	admin, err := v.telegram.FreshAdmin(requestCtx, message.Chat.ID, message.From.ID)
	if err != nil {
		v.notify(requestCtx, ctx.Bot(), message.Chat.ID, i18n.Messages.Panel.Settings.Error.AuthorizationCheckFailed.For(language))
		return nil
	}
	if !admin {
		v.notify(requestCtx, ctx.Bot(), message.Chat.ID, i18n.Messages.Panel.Error.AdminOnly.For(language))
		return nil
	}
	_, username, err := v.botIdentity(requestCtx, ctx.Bot())
	if err != nil || username == "" {
		v.notify(requestCtx, ctx.Bot(), message.Chat.ID, i18n.Messages.Panel.Settings.Error.PanelUnavailable.For(language))
		return nil
	}
	session, err := v.newSettingsSession(message.From.ID, message.Chat.ID, v.requesterLanguage(message))
	if err != nil {
		v.notify(requestCtx, ctx.Bot(), message.Chat.ID, i18n.Messages.Panel.Settings.Error.PanelUnavailable.For(language))
		return nil
	}
	url := "https://t.me/" + username + "?start=panel_" + session.token
	params := tu.Message(tu.ID(message.Chat.ID), i18n.Messages.Panel.Settings.Launch.Sent.For(language)).
		WithReplyParameters(&telego.ReplyParameters{MessageID: message.MessageID}).
		WithReplyMarkup(&telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{{{
			Text: i18n.Messages.Panel.Settings.Launch.Open.For(language), URL: url,
		}}}})
	if _, err := ctx.Bot().SendMessage(requestCtx, params); err != nil {
		v.removeSession(session)
		log.Printf("settings launcher in group %d failed: %v", message.Chat.ID, err)
	}
	return nil
}

func panelStartToken(text string) string {
	fields := strings.Fields(text)
	if len(fields) != 2 {
		return ""
	}
	command := fields[0]
	if index := strings.IndexByte(command, '@'); index >= 0 {
		command = command[:index]
	}
	if command != "/start" || !strings.HasPrefix(fields[1], "panel_") {
		return ""
	}
	return strings.TrimPrefix(fields[1], "panel_")
}

func (v *Panel) openSettingsStart(ctx *th.Context, message *telego.Message, token string) bool {
	if token == "" || message == nil || message.From == nil || message.Chat.Type != "private" {
		return false
	}
	requestCtx := ctx.Context()
	language := i18n.FromTelegram(message.From.LanguageCode)
	session := v.sessionByToken(token)
	if session == nil || session.ownerID != message.From.ID {
		_, _ = ctx.Bot().SendMessage(requestCtx, tu.Message(tu.ID(message.Chat.ID), i18n.Messages.Panel.Settings.Error.Expired.For(language)))
		return true
	}
	admin, err := v.telegram.FreshAdmin(requestCtx, session.anchorGroupID, message.From.ID)
	if err != nil {
		_, _ = ctx.Bot().SendMessage(requestCtx, tu.Message(tu.ID(message.Chat.ID), i18n.Messages.Panel.Settings.Error.AuthorizationCheckFailed.For(language)))
		return true
	}
	if !admin {
		v.removeSession(session)
		_, _ = ctx.Bot().SendMessage(requestCtx, tu.Message(tu.ID(message.Chat.ID), i18n.Messages.Panel.Settings.Error.AuthorizationLost.For(language)))
		return true
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if v.sessionByToken(token) != session {
		_, _ = ctx.Bot().SendMessage(requestCtx, tu.Message(tu.ID(message.Chat.ID), i18n.Messages.Panel.Settings.Error.Expired.For(language)))
		return true
	}
	session.chatID = message.Chat.ID
	session.language = i18n.FromRequester(message.From.LanguageCode, session.language)
	session.screen = "gl"
	session.page = 0
	if err := v.renderSession(requestCtx, ctx.Bot(), session, session.anchorGroupID); err != nil {
		v.removeSession(session)
		_, _ = ctx.Bot().SendMessage(requestCtx, tu.Message(tu.ID(message.Chat.ID), i18n.Messages.Panel.Settings.Error.PanelUnavailable.For(session.language)))
	}
	return true
}

type postCommitRenderError struct{ err error }

func (e *postCommitRenderError) Error() string { return e.err.Error() }
func (e *postCommitRenderError) Unwrap() error { return e.err }

func (v *Panel) renderAfterCommit(ctx context.Context, bot *telego.Bot, session *panelSession) error {
	if err := v.renderSession(ctx, bot, session, session.groupID); err != nil {
		return &postCommitRenderError{err: err}
	}
	return nil
}

func (v *Panel) handlePostCommitRenderError(ctx context.Context, bot *telego.Bot, session *panelSession, err error) (string, bool) {
	var postCommit *postCommitRenderError
	if !errors.As(err, &postCommit) {
		return "", false
	}
	log.Printf("settings change for group %d committed but panel render failed: %v", session.groupID, postCommit)
	text := i18n.Messages.Panel.Settings.Error.SavedRenderFailed.For(session.language)
	v.finishSession(ctx, bot, session, text)
	return text, true
}

// OnSettingsCallback authorizes and applies one versioned panel callback.
func (v *Panel) OnSettingsCallback(ctx *th.Context, update telego.Update) error {
	query := update.CallbackQuery
	if query == nil {
		return nil
	}
	requestCtx := ctx.Context()
	bot := ctx.Bot()
	data, err := parseCallback(query.Data)
	if err != nil || v.settings == nil || !v.settings.IsGroup(data.group) {
		v.answerCallback(requestCtx, bot, query.ID, "", false)
		return nil
	}
	language := i18n.FromTelegram(query.From.LanguageCode)
	admin, authErr := v.telegram.FreshAdmin(requestCtx, data.group, query.From.ID)
	if authErr != nil {
		v.answerCallback(requestCtx, bot, query.ID, i18n.Messages.Panel.Settings.Error.AuthorizationCheckFailed.For(language), true)
		return nil
	}
	if !admin {
		v.finishUserSession(requestCtx, bot, query.From.ID, query.Message, i18n.Messages.Panel.Settings.Error.AuthorizationLost.For(language))
		v.answerCallback(requestCtx, bot, query.ID, i18n.Messages.Panel.Settings.Error.AuthorizationLost.For(language), true)
		return nil
	}
	session := v.sessionByToken(data.token)
	if session == nil {
		v.expireCallbackMessage(requestCtx, bot, query.Message, i18n.Messages.Panel.Settings.Error.Expired.For(language))
		v.answerCallback(requestCtx, bot, query.ID, i18n.Messages.Panel.Settings.Error.Expired.For(language), true)
		return nil
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if v.sessionByToken(data.token) != session {
		v.answerCallback(requestCtx, bot, query.ID, i18n.Messages.Panel.Settings.Error.Expired.For(language), true)
		return nil
	}
	if session.ownerID != query.From.ID || session.screen != data.screen || query.Message == nil ||
		query.Message.GetChat().Type != "private" || query.Message.GetChat().ID != session.chatID ||
		query.Message.GetMessageID() != session.messageID {
		v.answerCallback(requestCtx, bot, query.ID, i18n.Messages.Panel.Settings.Error.Expired.For(language), true)
		return nil
	}
	if data.screen == "gl" {
		if data.field != "go" && data.group != session.groupID {
			v.answerCallback(requestCtx, bot, query.ID, i18n.Messages.Panel.Settings.Error.Expired.For(language), true)
			return nil
		}
	} else if data.group != session.groupID {
		v.answerCallback(requestCtx, bot, query.ID, i18n.Messages.Panel.Settings.Error.Expired.For(language), true)
		return nil
	}
	if err := v.dispatchCallback(requestCtx, bot, session, data); err != nil {
		if text, handled := v.handlePostCommitRenderError(requestCtx, bot, session, err); handled {
			v.answerCallback(requestCtx, bot, query.ID, text, true)
			return nil
		}
		var notice *panelNoticeError
		if errors.As(err, &notice) {
			v.answerCallback(requestCtx, bot, query.ID, notice.text, true)
			return nil
		}
		if errors.Is(err, store.ErrSettingsConflict) {
			v.finishSession(requestCtx, bot, session, i18n.Messages.Panel.Settings.Error.ConcurrentChange.For(session.language))
			v.answerCallback(requestCtx, bot, query.ID, i18n.Messages.Panel.Settings.Error.ConcurrentChange.For(session.language), true)
			return nil
		}
		log.Printf("settings callback for group %d failed: %v", data.group, err)
		v.finishSession(requestCtx, bot, session, i18n.Messages.Panel.Settings.Error.SaveFailed.For(session.language))
		v.answerCallback(requestCtx, bot, query.ID, i18n.Messages.Panel.Settings.Error.SaveFailed.For(session.language), true)
		return nil
	}
	v.answerCallback(requestCtx, bot, query.ID, "", false)
	return nil
}

func (v *Panel) dispatchCallback(ctx context.Context, bot *telego.Bot, session *panelSession, data callbackData) error {
	if data.field == "cl" {
		v.finishSession(ctx, bot, session, i18n.Messages.Panel.Settings.Common.Close.For(session.language))
		return nil
	}
	if data.screen == "gl" {
		return v.dispatchGroupList(ctx, bot, session, data)
	}
	group, ok := v.settings.Group(session.groupID)
	if !ok {
		return store.ErrUnknownGroup
	}
	stale := group.Revision() != session.revision
	if stale && revisionSensitive(data) {
		return &store.ConflictError{GroupID: session.groupID, Expected: session.revision, Actual: group.Revision()}
	}
	if stale {
		session.revision = group.Revision()
	}
	switch data.screen {
	case "gh":
		return v.dispatchGroupHome(ctx, bot, session, data)
	case "rt":
		return v.dispatchRuntime(ctx, bot, session, group, data)
	case "ls":
		return v.dispatchLists(ctx, bot, session, data)
	case "li":
		return v.dispatchList(ctx, bot, session, group, data)
	case "vp":
		return v.dispatchVerificationParameters(ctx, bot, session, data)
	case "ct":
		return v.navigate(ctx, bot, session, data.value)
	case "qb":
		return v.dispatchQuizBank(ctx, bot, session, group, data)
	case "qd":
		return v.dispatchQuizDraft(ctx, bot, session, group, data)
	case "fb":
		return v.dispatchFallbackBank(ctx, bot, session, group, data)
	case "fd":
		return v.dispatchFallbackDraft(ctx, bot, session, group, data)
	case "ch":
		return v.dispatchChannel(ctx, bot, session, group, data)
	case "cf":
		return v.dispatchConfirmation(ctx, bot, session, group, data)
	case "in":
		return v.cancelInput(ctx, bot, session, data)
	default:
		return errors.New("unknown panel screen")
	}
}

func revisionSensitive(data callbackData) bool {
	if data.screen == "gh" && (data.field == "go" || data.field == "rf") {
		return false
	}
	if data.field == "go" || data.field == "pg" || data.field == "rf" || data.field == "cn" {
		return false
	}
	return true
}

func (v *Panel) dispatchGroupList(ctx context.Context, bot *telego.Bot, session *panelSession, data callbackData) error {
	switch data.field {
	case "go":
		group, ok := v.settings.Group(data.group)
		if !ok {
			return store.ErrUnknownGroup
		}
		botID, _, err := v.botIdentity(ctx, bot)
		if err != nil {
			return err
		}
		member, err := bot.GetChatMember(ctx, &telego.GetChatMemberParams{ChatID: tu.ID(data.group), UserID: botID})
		if err != nil || member == nil || member.MemberStatus() == telego.MemberStatusLeft ||
			member.MemberStatus() == telego.MemberStatusBanned {
			return &panelNoticeError{text: i18n.Messages.Panel.Settings.Error.InvalidChat.For(session.language)}
		}
		session.groupID = data.group
		session.revision = group.Revision()
		session.globalRevision = v.settings.Global().Revision()
		session.page = 0
		session.screen = "gh"
		return v.renderSession(ctx, bot, session, data.group)
	case "pg":
		page, _ := decodeUnsigned(data.value)
		session.page = int(page)
		return v.renderSession(ctx, bot, session, data.group)
	case "rf":
		return v.renderSession(ctx, bot, session, data.group)
	default:
		return errors.New("invalid group-list action")
	}
}

func (v *Panel) dispatchGroupHome(ctx context.Context, bot *telego.Bot, session *panelSession, data callbackData) error {
	switch data.field {
	case "go":
		if data.value == "gl" {
			session.page = 0
		}
		return v.navigate(ctx, bot, session, data.value)
	case "rf":
		return v.renderSession(ctx, bot, session, session.groupID)
	default:
		return errors.New("invalid group-home action")
	}
}

func (v *Panel) dispatchRuntime(ctx context.Context, bot *telego.Bot, session *panelSession, group store.GroupView, data callbackData) error {
	next := group.Overrides()
	switch data.field {
	case "go":
		return v.navigate(ctx, bot, session, data.value)
	case "en":
		value := !group.Enabled().Value
		next.Enabled = &value
	case "df":
		value := !group.DMFirst().Value
		next.DMFirst = &value
	case "vm":
		value := map[string]string{"k": config.ModeKernel, "q": config.ModeQuiz, "m": config.ModeMixed}[data.value]
		next.VerifyMode = &value
	case "ns":
		value := !group.NameSpoiler().Value
		next.NameSpoiler = &value
	case "ld":
		value := !group.LookupAutoDeleteEnabled().Value
		next.LookupAutoDeleteEnabled = &value
	case "bd":
		return v.armTextInput(ctx, bot, session, inputBanDuration, "rt")
	case "lt":
		return v.armTextInput(ctx, bot, session, inputLookupTTL, "rt")
	case "lg":
		value := map[string]string{"z": "zh", "h": "zh-Hant", "e": "en"}[data.value]
		next.Lang = &value
	default:
		return errors.New("invalid runtime action")
	}
	result, err := v.settings.CommitGroup(session.groupID, session.revision, next)
	if err != nil {
		return err
	}
	session.revision = result.Revision
	if data.field == "lg" {
		session.language = i18n.FromStored(*next.Lang)
	}
	return v.renderAfterCommit(ctx, bot, session)
}

func (v *Panel) dispatchLists(ctx context.Context, bot *telego.Bot, session *panelSession, data callbackData) error {
	if data.field == "go" {
		return v.navigate(ctx, bot, session, data.value)
	}
	session.listKind = inputKind(data.field)
	session.page = 0
	session.screen = "li"
	return v.renderSession(ctx, bot, session, session.groupID)
}

func (v *Panel) dispatchList(ctx context.Context, bot *telego.Bot, session *panelSession, group store.GroupView, data callbackData) error {
	switch data.field {
	case "go":
		return v.navigate(ctx, bot, session, data.value)
	case "pg":
		page, _ := decodeUnsigned(data.value)
		session.page = int(page)
		return v.renderSession(ctx, bot, session, session.groupID)
	case "ca":
		if inputKind(data.value) != session.listKind {
			return errors.New("list kind does not match current screen")
		}
		return v.armChatInput(ctx, bot, session, session.listKind, "li")
	case "cw", "tg", "kc":
		if inputKind(data.field) != session.listKind {
			return errors.New("list kind does not match current screen")
		}
		encoded, _ := decodeUnsigned(data.value)
		id := int64(encoded>>1) ^ -int64(encoded&1)
		next := group.Overrides()
		values := v.listValues(group, session.listKind)
		found := false
		kept := make([]int64, 0, len(values))
		for _, value := range values {
			if value == id && !found {
				found = true
				continue
			}
			kept = append(kept, value)
		}
		if !found {
			return &store.ConflictError{GroupID: session.groupID, Expected: session.revision, Actual: group.Revision()}
		}
		if session.listKind == inputChannelWhitelist {
			if err := v.updateChannelWhitelist(ctx, bot, session, id, false); err != nil {
				return err
			}
			return v.renderAfterCommit(ctx, bot, session)
		}
		setListOverride(&next, session.listKind, kept)
		result, err := v.settings.CommitGroup(session.groupID, session.revision, next)
		if err != nil {
			return err
		}
		session.revision = result.Revision
		return v.renderAfterCommit(ctx, bot, session)
	default:
		return errors.New("invalid list action")
	}
}

func (v *Panel) dispatchVerificationParameters(ctx context.Context, bot *telego.Bot, session *panelSession, data callbackData) error {
	if data.field == "go" {
		return v.navigate(ctx, bot, session, data.value)
	}
	kind := map[string]inputKind{"to": inputTimeout, "mf": inputMaxFails, "rc": inputRetryCooldown, "pr": inputPrivateRate}[data.field]
	if kind == inputPrivateRate && session.groupID != v.settings.ControlGroupID() {
		return &panelNoticeError{text: i18n.Messages.Panel.Settings.Error.ControlGroupOnly.For(session.language)}
	}
	return v.armTextInput(ctx, bot, session, kind, "vp")
}

func (v *Panel) navigate(ctx context.Context, bot *telego.Bot, session *panelSession, screen string) error {
	session.screen = screen
	session.page = 0
	if screen != "qd" {
		session.quiz = nil
	}
	if screen != "fd" {
		session.fallback = nil
	}
	return v.renderSession(ctx, bot, session, session.groupID)
}

func (v *Panel) renderSession(ctx context.Context, bot *telego.Bot, session *panelSession, authorizedGroup int64) error {
	token, err := newPanelToken()
	if err != nil {
		return err
	}
	text, keyboard, err := v.buildScreen(ctx, bot, session, authorizedGroup, token)
	if err != nil {
		return err
	}
	if session.messageID == 0 {
		message, err := bot.SendMessage(ctx, tu.Message(tu.ID(session.chatID), text).WithReplyMarkup(keyboard))
		if err != nil {
			return err
		}
		if message == nil {
			return errors.New("telegram returned no panel message")
		}
		session.messageID = message.MessageID
	} else {
		_, err = bot.EditMessageText(ctx, &telego.EditMessageTextParams{
			ChatID: tu.ID(session.chatID), MessageID: session.messageID, Text: text, ReplyMarkup: keyboard,
		})
		if err != nil {
			return err
		}
	}
	if !v.rotateSessionToken(session, token) {
		return errors.New("panel session expired while rendering")
	}
	return nil
}

func (v *Panel) buildScreen(ctx context.Context, bot *telego.Bot, session *panelSession, authorizedGroup int64, token string) (string, *telego.InlineKeyboardMarkup, error) {
	switch session.screen {
	case "gl":
		return v.buildGroupList(ctx, bot, session, authorizedGroup, token)
	case "gh":
		return v.buildGroupHome(ctx, bot, session, token)
	case "rt":
		return v.buildRuntime(session, token)
	case "ls":
		return v.buildLists(session, token)
	case "li":
		return v.buildList(session, token)
	case "vp":
		return v.buildVerificationParameters(session, token)
	case "ct":
		return v.buildContent(session, token)
	case "qb":
		return v.buildQuizBank(session, token)
	case "qd":
		return v.buildQuizDetail(session, token)
	case "fb":
		return v.buildFallbackBank(session, token)
	case "fd":
		return v.buildFallbackDetail(session, token)
	case "ch":
		return v.buildChannel(session, token)
	case "cf":
		return v.buildConfirmation(session, token)
	case "in":
		return v.buildInput(session, token)
	default:
		return "", nil, errors.New("unknown panel screen")
	}
}

func (v *Panel) buildGroupList(ctx context.Context, bot *telego.Bot, session *panelSession, authorizedGroup int64, token string) (string, *telego.InlineKeyboardMarkup, error) {
	groups, err := v.eligibleGroups(ctx, bot, session.ownerID, authorizedGroup)
	if err != nil {
		return "", nil, err
	}
	rows := make([][]telego.InlineKeyboardButton, 0, panelPageSize+2)
	if len(groups) == 0 {
		rows, err = v.appendButtonRow(rows, token, session.screen, session.groupID,
			panelButton{text: i18n.Messages.Panel.Settings.Common.Refresh.For(session.language), field: "rf", value: "_"},
			panelButton{text: i18n.Messages.Panel.Settings.Common.Close.For(session.language), field: "cl", value: "_"})
		return i18n.Messages.Panel.Settings.Screen.NoGroups.For(session.language), &telego.InlineKeyboardMarkup{InlineKeyboard: rows}, err
	}
	maxPage := (len(groups) - 1) / panelPageSize
	if session.page > maxPage {
		session.page = maxPage
	}
	start := session.page * panelPageSize
	end := min(start+panelPageSize, len(groups))
	var itemLines []string
	for _, group := range groups[start:end] {
		label := i18n.Messages.Panel.Settings.Value.GroupButton.Render(session.language, group.title, group.id)
		rows, err = v.appendButtonRow(rows, token, session.screen, group.id, panelButton{text: label, field: "go", value: "gh"})
		if err != nil {
			return "", nil, err
		}
		itemLines = append(itemLines, label)
	}
	var pageButtons []panelButton
	if session.page > 0 {
		pageButtons = append(pageButtons, panelButton{text: i18n.Messages.Panel.Settings.Common.Prev.For(session.language), field: "pg", value: encodeUnsigned(uint64(session.page - 1))})
	}
	if session.page < maxPage {
		pageButtons = append(pageButtons, panelButton{text: i18n.Messages.Panel.Settings.Common.Next.For(session.language), field: "pg", value: encodeUnsigned(uint64(session.page + 1))})
	}
	if len(pageButtons) > 0 {
		rows, err = v.appendButtonRow(rows, token, session.screen, session.groupID, pageButtons...)
		if err != nil {
			return "", nil, err
		}
	}
	rows, err = v.appendButtonRow(rows, token, session.screen, session.groupID,
		panelButton{text: i18n.Messages.Panel.Settings.Common.Refresh.For(session.language), field: "rf", value: "_"},
		panelButton{text: i18n.Messages.Panel.Settings.Common.Close.For(session.language), field: "cl", value: "_"})
	text := i18n.Messages.Panel.Settings.Screen.Groups.Render(session.language, session.page+1, strings.Join(itemLines, "\n"))
	return text, &telego.InlineKeyboardMarkup{InlineKeyboard: rows}, err
}

func (v *Panel) buildGroupHome(ctx context.Context, bot *telego.Bot, session *panelSession, token string) (string, *telego.InlineKeyboardMarkup, error) {
	group, ok := v.settings.Group(session.groupID)
	if !ok {
		return "", nil, store.ErrUnknownGroup
	}
	title := strconv.FormatInt(session.groupID, 10)
	if chat, err := bot.GetChat(ctx, &telego.GetChatParams{ChatID: tu.ID(session.groupID)}); err == nil && chat != nil && chat.Title != "" {
		title = chat.Title
	}
	fallbackCount := len(group.FallbackQuestions().Value)
	channel := v.channelDisplayValue(group)
	if v.requiredChannelID(group) == 0 {
		channel = i18n.Messages.Panel.Settings.Value.RequiredDisabled.For(session.language)
	}
	text := i18n.Messages.Panel.Settings.Screen.GroupHome.Render(session.language, title, session.groupID,
		group.Revision(), v.persistenceText(session.language), v.sourcedBool(session.language, group.Enabled()),
		v.sourcedMode(session.language, group.VerifyMode()), channel, len(group.Questions().Value), fallbackCount,
		v.sourcedLanguage(session.language, group.Lang()))
	buttons := []panelButton{
		{text: i18n.Messages.Panel.Settings.Field.Runtime.For(session.language), field: "go", value: "rt"},
		{text: i18n.Messages.Panel.Settings.Field.Lists.For(session.language), field: "go", value: "ls"},
		{text: i18n.Messages.Panel.Settings.Field.VerificationParameters.For(session.language), field: "go", value: "vp"},
		{text: i18n.Messages.Panel.Settings.Field.Content.For(session.language), field: "go", value: "ct"},
	}
	rows := make([][]telego.InlineKeyboardButton, 0, 5)
	var err error
	for _, button := range buttons {
		rows, err = v.appendButtonRow(rows, token, session.screen, session.groupID, button)
		if err != nil {
			return "", nil, err
		}
	}
	rows, err = v.appendButtonRow(rows, token, session.screen, session.groupID,
		panelButton{text: i18n.Messages.Panel.Settings.Field.ChangeGroup.For(session.language), field: "go", value: "gl"},
		panelButton{text: i18n.Messages.Panel.Settings.Common.Refresh.For(session.language), field: "rf", value: "_"},
		panelButton{text: i18n.Messages.Panel.Settings.Common.Close.For(session.language), field: "cl", value: "_"})
	return text, &telego.InlineKeyboardMarkup{InlineKeyboard: rows}, err
}

func (v *Panel) buildRuntime(session *panelSession, token string) (string, *telego.InlineKeyboardMarkup, error) {
	group, ok := v.settings.Group(session.groupID)
	if !ok {
		return "", nil, store.ErrUnknownGroup
	}
	text := i18n.Messages.Panel.Settings.Screen.Runtime.Render(session.language, session.groupID,
		v.sourcedBool(session.language, group.Enabled()), v.sourcedMode(session.language, group.VerifyMode()),
		v.sourcedBool(session.language, group.DMFirst()), v.sourcedBool(session.language, group.NameSpoiler()),
		v.sourcedSeconds(session.language, group.BanSeconds(), true),
		v.sourcedBool(session.language, group.LookupAutoDeleteEnabled()), v.sourcedSeconds(session.language, group.LookupTTLSeconds(), false),
		v.sourcedLanguage(session.language, group.Lang()))
	buttons := []panelButton{
		{text: i18n.Messages.Panel.Settings.Field.Verification.For(session.language), field: "en", value: "_"},
		{text: i18n.Messages.Panel.Settings.Field.DMFirst.For(session.language), field: "df", value: "_"},
		{text: i18n.Messages.Panel.Settings.Field.ModeKernel.For(session.language), field: "vm", value: "k"},
		{text: i18n.Messages.Panel.Settings.Field.ModeQuiz.For(session.language), field: "vm", value: "q"},
		{text: i18n.Messages.Panel.Settings.Field.ModeMixed.For(session.language), field: "vm", value: "m"},
		{text: i18n.Messages.Panel.Settings.Field.NameSpoiler.For(session.language), field: "ns", value: "_"},
		{text: i18n.Messages.Panel.Settings.Field.BanDuration.For(session.language), field: "bd", value: "_"},
		{text: i18n.Messages.Panel.Settings.Field.LookupDelete.For(session.language), field: "ld", value: "_"},
		{text: i18n.Messages.Panel.Settings.Field.LookupTTL.For(session.language), field: "lt", value: "_"},
		{text: i18n.Messages.Panel.Settings.Field.LanguageZH.For(session.language), field: "lg", value: "z"},
		{text: i18n.Messages.Panel.Settings.Field.LanguageZHHant.For(session.language), field: "lg", value: "h"},
		{text: i18n.Messages.Panel.Settings.Field.LanguageEN.For(session.language), field: "lg", value: "e"},
		{text: i18n.Messages.Panel.Settings.Common.Back.For(session.language), field: "go", value: "gh"},
	}
	return v.screenWithSingleButtons(text, token, session, buttons)
}

func (v *Panel) buildLists(session *panelSession, token string) (string, *telego.InlineKeyboardMarkup, error) {
	group, ok := v.settings.Group(session.groupID)
	if !ok {
		return "", nil, store.ErrUnknownGroup
	}
	text := i18n.Messages.Panel.Settings.Screen.Lists.Render(session.language, session.groupID,
		len(group.ChannelWhitelist().Value), len(group.TrustedMemberGroupIDs().Value), len(group.KnownChatIDs().Value))
	buttons := []panelButton{
		{text: i18n.Messages.Panel.Settings.Field.ChannelWhitelist.For(session.language), field: "cw", value: "_"},
		{text: i18n.Messages.Panel.Settings.Field.TrustedGroups.For(session.language), field: "tg", value: "_"},
		{text: i18n.Messages.Panel.Settings.Field.KnownChats.For(session.language), field: "kc", value: "_"},
		{text: i18n.Messages.Panel.Settings.Common.Back.For(session.language), field: "go", value: "gh"},
	}
	return v.screenWithSingleButtons(text, token, session, buttons)
}

func (v *Panel) buildList(session *panelSession, token string) (string, *telego.InlineKeyboardMarkup, error) {
	group, ok := v.settings.Group(session.groupID)
	if !ok {
		return "", nil, store.ErrUnknownGroup
	}
	values := v.listValues(group, session.listKind)
	name := v.listName(session.language, session.listKind)
	maxPage := 0
	if len(values) > 0 {
		maxPage = (len(values) - 1) / panelPageSize
	}
	if session.page > maxPage {
		session.page = maxPage
	}
	start := session.page * panelPageSize
	end := min(start+panelPageSize, len(values))
	rows := make([][]telego.InlineKeyboardButton, 0, panelPageSize+3)
	var lines []string
	for _, id := range values[start:end] {
		label := i18n.Messages.Panel.Settings.Value.IDItem.Render(session.language, id)
		lines = append(lines, label)
		var err error
		rows, err = v.appendButtonRow(rows, token, session.screen, session.groupID, panelButton{
			text:  i18n.Messages.Panel.Settings.Common.Remove.For(session.language) + " " + label,
			field: string(session.listKind), value: encodeSigned(id),
		})
		if err != nil {
			return "", nil, err
		}
	}
	var err error
	rows, err = v.appendButtonRow(rows, token, session.screen, session.groupID,
		panelButton{text: i18n.Messages.Panel.Settings.Common.Add.For(session.language), field: "ca", value: string(session.listKind)})
	if err != nil {
		return "", nil, err
	}
	var pages []panelButton
	if session.page > 0 {
		pages = append(pages, panelButton{text: i18n.Messages.Panel.Settings.Common.Prev.For(session.language), field: "pg", value: encodeUnsigned(uint64(session.page - 1))})
	}
	if session.page < maxPage {
		pages = append(pages, panelButton{text: i18n.Messages.Panel.Settings.Common.Next.For(session.language), field: "pg", value: encodeUnsigned(uint64(session.page + 1))})
	}
	if len(pages) > 0 {
		rows, err = v.appendButtonRow(rows, token, session.screen, session.groupID, pages...)
		if err != nil {
			return "", nil, err
		}
	}
	rows, err = v.appendButtonRow(rows, token, session.screen, session.groupID,
		panelButton{text: i18n.Messages.Panel.Settings.Common.Back.For(session.language), field: "go", value: "ls"})
	text := i18n.Messages.Panel.Settings.Screen.List.Render(session.language, name, session.groupID, len(values), strings.Join(lines, "\n"))
	return text, &telego.InlineKeyboardMarkup{InlineKeyboard: rows}, err
}

func (v *Panel) buildVerificationParameters(session *panelSession, token string) (string, *telego.InlineKeyboardMarkup, error) {
	group, ok := v.settings.Group(session.groupID)
	if !ok {
		return "", nil, store.ErrUnknownGroup
	}
	global := v.settings.Global()
	session.globalRevision = global.Revision()
	text := i18n.Messages.Panel.Settings.Screen.Verification.Render(session.language, session.groupID,
		v.sourcedSeconds(session.language, group.TimeoutSeconds(), false), v.sourcedLimit(session.language, group.VerifyMaxFails()),
		v.sourcedLimit(session.language, group.VerifyRetrySeconds()),
		i18n.Messages.Panel.Settings.Value.Sourced.Render(session.language, strconv.Itoa(global.PrivateQueryPerMin().Value), v.sourceText(session.language, global.PrivateQueryPerMin().Source)))
	buttons := []panelButton{
		{text: i18n.Messages.Panel.Settings.Field.Timeout.For(session.language), field: "to", value: "_"},
		{text: i18n.Messages.Panel.Settings.Field.MaxFails.For(session.language), field: "mf", value: "_"},
		{text: i18n.Messages.Panel.Settings.Field.RetryCooldown.For(session.language), field: "rc", value: "_"},
		{text: i18n.Messages.Panel.Settings.Field.PrivateRate.For(session.language), field: "pr", value: "_"},
		{text: i18n.Messages.Panel.Settings.Common.Back.For(session.language), field: "go", value: "gh"},
	}
	return v.screenWithSingleButtons(text, token, session, buttons)
}

func (v *Panel) buildContent(session *panelSession, token string) (string, *telego.InlineKeyboardMarkup, error) {
	group, ok := v.settings.Group(session.groupID)
	if !ok {
		return "", nil, store.ErrUnknownGroup
	}
	fallback := i18n.Messages.Panel.Settings.Value.Custom.For(session.language)
	if group.FallbackBuiltin().Value {
		fallback = i18n.Messages.Panel.Settings.Value.Builtins.For(session.language)
	}
	channel := v.channelDisplayValue(group)
	if v.requiredChannelID(group) == 0 {
		channel = i18n.Messages.Panel.Settings.Value.RequiredDisabled.For(session.language)
	}
	invite := group.ChannelInviteURL().Value
	if invite == "" {
		invite = i18n.Messages.Panel.Settings.Value.InviteMissing.For(session.language)
	}
	text := i18n.Messages.Panel.Settings.Screen.Content.Render(session.language, session.groupID, len(group.Questions().Value), fallback, channel, invite)
	buttons := []panelButton{
		{text: i18n.Messages.Panel.Settings.Field.QuizBank.For(session.language), field: "go", value: "qb"},
		{text: i18n.Messages.Panel.Settings.Field.FallbackBank.For(session.language), field: "go", value: "fb"},
		{text: i18n.Messages.Panel.Settings.Field.RequiredChannel.For(session.language), field: "go", value: "ch"},
		{text: i18n.Messages.Panel.Settings.Common.Back.For(session.language), field: "go", value: "gh"},
	}
	return v.screenWithSingleButtons(text, token, session, buttons)
}

func (v *Panel) buildQuizBank(session *panelSession, token string) (string, *telego.InlineKeyboardMarkup, error) {
	group, ok := v.settings.Group(session.groupID)
	if !ok {
		return "", nil, store.ErrUnknownGroup
	}
	questions := group.Questions().Value
	return v.buildQuestionBank(session, token, questions, false)
}

func (v *Panel) buildFallbackBank(session *panelSession, token string) (string, *telego.InlineKeyboardMarkup, error) {
	group, ok := v.settings.Group(session.groupID)
	if !ok {
		return "", nil, store.ErrUnknownGroup
	}
	questions := group.FallbackQuestions().Value
	rows := make([][]telego.InlineKeyboardButton, 0, panelPageSize+4)
	maxPage := 0
	if len(questions) > 0 {
		maxPage = (len(questions) - 1) / panelPageSize
	}
	if session.page > maxPage {
		session.page = maxPage
	}
	start := session.page * panelPageSize
	end := min(start+panelPageSize, len(questions))
	var lines []string
	for index := start; index < end; index++ {
		label := i18n.Messages.Panel.Settings.Value.QuestionItem.Render(session.language, index+1, summarize(questions[index].Q))
		lines = append(lines, label)
		var err error
		rows, err = v.appendButtonRow(rows, token, session.screen, session.groupID, panelButton{text: label, field: "fq", value: encodeUnsigned(uint64(index))})
		if err != nil {
			return "", nil, err
		}
	}
	var err error
	rows, err = v.appendButtonRow(rows, token, session.screen, session.groupID,
		panelButton{text: i18n.Messages.Panel.Settings.Common.Add.For(session.language), field: "ca", value: "_"},
		panelButton{text: i18n.Messages.Panel.Settings.Field.ResetBuiltin.For(session.language), field: "rb", value: "_"})
	if err != nil {
		return "", nil, err
	}
	rows, err = v.appendPageAndBack(rows, token, session, maxPage, "ct")
	bank := i18n.Messages.Panel.Settings.Value.Custom.For(session.language)
	if group.FallbackBuiltin().Value {
		bank = i18n.Messages.Panel.Settings.Value.Builtins.For(session.language)
	}
	text := i18n.Messages.Panel.Settings.Screen.FallbackBank.Render(session.language, session.groupID, bank, len(questions), strings.Join(lines, "\n"))
	return text, &telego.InlineKeyboardMarkup{InlineKeyboard: rows}, err
}

func (v *Panel) buildQuestionBank(session *panelSession, token string, questions []config.Question, _ bool) (string, *telego.InlineKeyboardMarkup, error) {
	rows := make([][]telego.InlineKeyboardButton, 0, panelPageSize+3)
	maxPage := 0
	if len(questions) > 0 {
		maxPage = (len(questions) - 1) / panelPageSize
	}
	if session.page > maxPage {
		session.page = maxPage
	}
	start := session.page * panelPageSize
	end := min(start+panelPageSize, len(questions))
	var lines []string
	for index := start; index < end; index++ {
		label := i18n.Messages.Panel.Settings.Value.QuestionItem.Render(session.language, index+1, summarize(questions[index].Q))
		lines = append(lines, label)
		var err error
		rows, err = v.appendButtonRow(rows, token, session.screen, session.groupID, panelButton{text: label, field: "qq", value: encodeUnsigned(uint64(index))})
		if err != nil {
			return "", nil, err
		}
	}
	var err error
	rows, err = v.appendButtonRow(rows, token, session.screen, session.groupID,
		panelButton{text: i18n.Messages.Panel.Settings.Common.Add.For(session.language), field: "ca", value: "_"})
	if err != nil {
		return "", nil, err
	}
	rows, err = v.appendPageAndBack(rows, token, session, maxPage, "ct")
	text := i18n.Messages.Panel.Settings.Screen.QuizBank.Render(session.language, session.groupID, len(questions), strings.Join(lines, "\n"))
	return text, &telego.InlineKeyboardMarkup{InlineKeyboard: rows}, err
}

func (v *Panel) buildQuizDetail(session *panelSession, token string) (string, *telego.InlineKeyboardMarkup, error) {
	if session.quiz == nil {
		return "", nil, errors.New("missing quiz draft")
	}
	draft := session.quiz.question
	var lines []string
	for index, option := range draft.Options {
		lines = append(lines, i18n.Messages.Panel.Settings.Value.OptionItem.Render(session.language, index+1, option))
	}
	correct := i18n.Messages.Panel.Settings.Common.None.For(session.language)
	if draft.Answer >= 0 && draft.Answer < len(draft.Options) {
		correct = i18n.Messages.Panel.Settings.Value.OptionItem.Render(session.language, draft.Answer+1, draft.Options[draft.Answer])
	}
	text := i18n.Messages.Panel.Settings.Screen.QuizDetail.Render(session.language, draft.Q, strings.Join(lines, "\n"), correct)
	rows := make([][]telego.InlineKeyboardButton, 0, len(draft.Options)*2+4)
	var err error
	rows, err = v.appendButtonRow(rows, token, session.screen, session.groupID,
		panelButton{text: i18n.Messages.Panel.Settings.Field.EditQuestion.For(session.language), field: "qq", value: "_"},
		panelButton{text: i18n.Messages.Panel.Settings.Field.AddOption.For(session.language), field: "qo", value: "_"})
	if err != nil {
		return "", nil, err
	}
	for index, option := range draft.Options {
		rows, err = v.appendButtonRow(rows, token, session.screen, session.groupID,
			panelButton{text: i18n.Messages.Panel.Settings.Field.CorrectOption.For(session.language) + " " + summarize(option), field: "ok", value: encodeUnsigned(uint64(index))},
			panelButton{text: i18n.Messages.Panel.Settings.Common.Remove.For(session.language) + " " + strconv.Itoa(index+1), field: "dl", value: encodeUnsigned(uint64(index))})
		if err != nil {
			return "", nil, err
		}
	}
	rows, err = v.appendButtonRow(rows, token, session.screen, session.groupID,
		panelButton{text: i18n.Messages.Panel.Settings.Common.Save.For(session.language), field: "sv", value: "_"},
		panelButton{text: i18n.Messages.Panel.Settings.Common.Cancel.For(session.language), field: "cn", value: "_"})
	if err == nil && session.quiz.existing {
		rows, err = v.appendButtonRow(rows, token, session.screen, session.groupID,
			panelButton{text: i18n.Messages.Panel.Settings.Common.Delete.For(session.language), field: "rm", value: "_"})
	}
	return text, &telego.InlineKeyboardMarkup{InlineKeyboard: rows}, err
}

func (v *Panel) buildFallbackDetail(session *panelSession, token string) (string, *telego.InlineKeyboardMarkup, error) {
	if session.fallback == nil {
		return "", nil, errors.New("missing fallback draft")
	}
	draft := session.fallback.question
	var lines []string
	for index, answer := range draft.Answers {
		lines = append(lines, i18n.Messages.Panel.Settings.Value.AnswerItem.Render(session.language, index+1, answer))
	}
	text := i18n.Messages.Panel.Settings.Screen.FallbackDetail.Render(session.language, draft.Q, strings.Join(lines, "\n"))
	rows := make([][]telego.InlineKeyboardButton, 0, len(draft.Answers)+4)
	var err error
	rows, err = v.appendButtonRow(rows, token, session.screen, session.groupID,
		panelButton{text: i18n.Messages.Panel.Settings.Field.EditQuestion.For(session.language), field: "fq", value: "_"},
		panelButton{text: i18n.Messages.Panel.Settings.Field.AddAnswer.For(session.language), field: "fa", value: "_"})
	if err != nil {
		return "", nil, err
	}
	for index := range draft.Answers {
		rows, err = v.appendButtonRow(rows, token, session.screen, session.groupID,
			panelButton{text: i18n.Messages.Panel.Settings.Common.Remove.For(session.language) + " " + strconv.Itoa(index+1), field: "dl", value: encodeUnsigned(uint64(index))})
		if err != nil {
			return "", nil, err
		}
	}
	rows, err = v.appendButtonRow(rows, token, session.screen, session.groupID,
		panelButton{text: i18n.Messages.Panel.Settings.Common.Save.For(session.language), field: "sv", value: "_"},
		panelButton{text: i18n.Messages.Panel.Settings.Common.Cancel.For(session.language), field: "cn", value: "_"})
	if err == nil && session.fallback.existing {
		rows, err = v.appendButtonRow(rows, token, session.screen, session.groupID,
			panelButton{text: i18n.Messages.Panel.Settings.Common.Delete.For(session.language), field: "rm", value: "_"})
	}
	return text, &telego.InlineKeyboardMarkup{InlineKeyboard: rows}, err
}

func (v *Panel) buildChannel(session *panelSession, token string) (string, *telego.InlineKeyboardMarkup, error) {
	group, ok := v.settings.Group(session.groupID)
	if !ok {
		return "", nil, store.ErrUnknownGroup
	}
	display := v.channelDisplayValue(group)
	if display == "" {
		display = i18n.Messages.Panel.Settings.Common.None.For(session.language)
	}
	invite := group.ChannelInviteURL().Value
	if invite == "" {
		invite = i18n.Messages.Panel.Settings.Value.InviteMissing.For(session.language)
	}
	text := i18n.Messages.Panel.Settings.Screen.Channel.Render(session.language, session.groupID, v.requiredChannelID(group), display, invite)
	buttons := []panelButton{
		{text: i18n.Messages.Panel.Settings.Field.SetChannel.For(session.language), field: "ci", value: "_"},
		{text: i18n.Messages.Panel.Settings.Field.SetInvite.For(session.language), field: "iu", value: "_"},
		{text: i18n.Messages.Panel.Settings.Field.ClearInvite.For(session.language), field: "dl", value: "_"},
		{text: i18n.Messages.Panel.Settings.Common.Disable.For(session.language), field: "ds", value: "_"},
		{text: i18n.Messages.Panel.Settings.Common.Back.For(session.language), field: "go", value: "ct"},
	}
	return v.screenWithSingleButtons(text, token, session, buttons)
}

func (v *Panel) buildConfirmation(session *panelSession, token string) (string, *telego.InlineKeyboardMarkup, error) {
	if session.confirm == nil {
		return "", nil, errors.New("missing confirmation")
	}
	object := i18n.Messages.Panel.Settings.Common.Delete.For(session.language)
	switch session.confirm.kind {
	case "channel":
		object = i18n.Messages.Panel.Settings.Field.RequiredChannel.For(session.language)
	case "fallback_builtin":
		object = i18n.Messages.Panel.Settings.Field.ResetBuiltin.For(session.language)
	}
	text := i18n.Messages.Panel.Settings.Screen.Confirm.Render(session.language, object)
	rows, err := v.appendButtonRow(nil, token, session.screen, session.groupID,
		panelButton{text: i18n.Messages.Panel.Settings.Common.Confirm.For(session.language), field: "ok", value: "_"},
		panelButton{text: i18n.Messages.Panel.Settings.Common.Cancel.For(session.language), field: "cn", value: "_"})
	return text, &telego.InlineKeyboardMarkup{InlineKeyboard: rows}, err
}

func (v *Panel) buildInput(session *panelSession, token string) (string, *telego.InlineKeyboardMarkup, error) {
	if session.pending == nil {
		return "", nil, errors.New("missing panel input")
	}
	prompt := v.inputPrompt(session.language, session.pending.kind)
	text := i18n.Messages.Panel.Settings.Screen.Input.Render(session.language, prompt)
	rows, err := v.appendButtonRow(nil, token, session.screen, session.groupID,
		panelButton{text: i18n.Messages.Panel.Settings.Common.Cancel.For(session.language), field: "cn", value: "_"})
	return text, &telego.InlineKeyboardMarkup{InlineKeyboard: rows}, err
}

func (v *Panel) screenWithSingleButtons(text, token string, session *panelSession, buttons []panelButton) (string, *telego.InlineKeyboardMarkup, error) {
	rows := make([][]telego.InlineKeyboardButton, 0, len(buttons))
	var err error
	for _, button := range buttons {
		rows, err = v.appendButtonRow(rows, token, session.screen, session.groupID, button)
		if err != nil {
			return "", nil, err
		}
	}
	return text, &telego.InlineKeyboardMarkup{InlineKeyboard: rows}, nil
}

func (v *Panel) appendButtonRow(rows [][]telego.InlineKeyboardButton, token, screen string, defaultGroup int64, buttons ...panelButton) ([][]telego.InlineKeyboardButton, error) {
	row := make([]telego.InlineKeyboardButton, 0, len(buttons))
	for _, button := range buttons {
		group := button.group
		if group == 0 {
			group = defaultGroup
		}
		data, err := encodeCallback(callbackData{token: token, screen: screen, group: group, field: button.field, value: button.value})
		if err != nil {
			return nil, err
		}
		row = append(row, telego.InlineKeyboardButton{Text: button.text, CallbackData: data})
	}
	return append(rows, row), nil
}

func (v *Panel) appendPageAndBack(rows [][]telego.InlineKeyboardButton, token string, session *panelSession, maxPage int, back string) ([][]telego.InlineKeyboardButton, error) {
	var pageButtons []panelButton
	if session.page > 0 {
		pageButtons = append(pageButtons, panelButton{text: i18n.Messages.Panel.Settings.Common.Prev.For(session.language), field: "pg", value: encodeUnsigned(uint64(session.page - 1))})
	}
	if session.page < maxPage {
		pageButtons = append(pageButtons, panelButton{text: i18n.Messages.Panel.Settings.Common.Next.For(session.language), field: "pg", value: encodeUnsigned(uint64(session.page + 1))})
	}
	var err error
	if len(pageButtons) > 0 {
		rows, err = v.appendButtonRow(rows, token, session.screen, session.groupID, pageButtons...)
		if err != nil {
			return nil, err
		}
	}
	return v.appendButtonRow(rows, token, session.screen, session.groupID,
		panelButton{text: i18n.Messages.Panel.Settings.Common.Back.For(session.language), field: "go", value: back})
}

func (v *Panel) eligibleGroups(ctx context.Context, bot *telego.Bot, userID, authorizedGroup int64) ([]eligibleGroup, error) {
	botID, _, err := v.botIdentity(ctx, bot)
	if err != nil {
		return nil, err
	}
	var groups []eligibleGroup
	for _, groupID := range v.settings.GroupIDs() {
		member, err := bot.GetChatMember(ctx, &telego.GetChatMemberParams{ChatID: tu.ID(groupID), UserID: botID})
		if err != nil || member == nil || member.MemberStatus() == telego.MemberStatusLeft || member.MemberStatus() == telego.MemberStatusBanned {
			continue
		}
		if groupID != authorizedGroup {
			admin, err := v.telegram.FreshAdmin(ctx, groupID, userID)
			if err != nil || !admin {
				continue
			}
		}
		title := strconv.FormatInt(groupID, 10)
		if chat, err := bot.GetChat(ctx, &telego.GetChatParams{ChatID: tu.ID(groupID)}); err == nil && chat != nil && chat.Title != "" {
			title = chat.Title
		}
		groups = append(groups, eligibleGroup{id: groupID, title: title})
	}
	return groups, nil
}

func (v *Panel) botIdentity(ctx context.Context, bot *telego.Bot) (int64, string, error) {
	state := v.panelState
	state.mu.Lock()
	if state.botID != 0 {
		id, name := state.botID, state.botName
		state.mu.Unlock()
		return id, name, nil
	}
	state.mu.Unlock()
	me, err := bot.GetMe(ctx)
	if err != nil || me == nil {
		return 0, "", err
	}
	state.mu.Lock()
	state.botID, state.botName = me.ID, me.Username
	state.mu.Unlock()
	return me.ID, me.Username, nil
}

func (v *Panel) answerCallback(ctx context.Context, bot *telego.Bot, id, text string, alert bool) {
	params := &telego.AnswerCallbackQueryParams{CallbackQueryID: id, Text: text, ShowAlert: alert}
	_ = bot.AnswerCallbackQuery(ctx, params)
}

func (v *Panel) finishUserSession(ctx context.Context, bot *telego.Bot, userID int64, message telego.MaybeInaccessibleMessage, text string) {
	if session := v.sessionByUser(userID); session != nil {
		session.mu.Lock()
		v.finishSession(ctx, bot, session, text)
		session.mu.Unlock()
		return
	}
	v.expireCallbackMessage(ctx, bot, message, text)
}

func (v *Panel) finishSession(ctx context.Context, bot *telego.Bot, session *panelSession, text string) {
	if session.pending != nil {
		v.rememberCanceledPrompt(session, session.pending)
		session.pending = nil
	}
	v.removeSession(session)
	if session.messageID != 0 {
		_, _ = bot.EditMessageText(ctx, &telego.EditMessageTextParams{ChatID: tu.ID(session.chatID), MessageID: session.messageID, Text: text})
	}
}

func (v *Panel) expireCallbackMessage(ctx context.Context, bot *telego.Bot, message telego.MaybeInaccessibleMessage, text string) {
	if message == nil || message.GetChat().Type != "private" {
		return
	}
	_, _ = bot.EditMessageText(ctx, &telego.EditMessageTextParams{ChatID: tu.ID(message.GetChat().ID), MessageID: message.GetMessageID(), Text: text})
}

func (v *Panel) persistenceText(language i18n.Lang) string {
	status := v.settings.Persistence()
	switch {
	case status.Durable && status.Writable:
		return i18n.Messages.Panel.Settings.Value.Durable.For(language)
	case !status.Configured:
		return i18n.Messages.Panel.Settings.Value.RuntimeOnly.For(language)
	default:
		return i18n.Messages.Panel.Settings.Value.Unavailable.For(language)
	}
}

func (v *Panel) sourceText(language i18n.Lang, source store.Source) string {
	switch source {
	case store.SourceRuntime:
		return i18n.Messages.Panel.Settings.Source.Runtime.For(language)
	case store.SourceConfig:
		return i18n.Messages.Panel.Settings.Source.Config.For(language)
	default:
		return i18n.Messages.Panel.Settings.Source.Default.For(language)
	}
}

func (v *Panel) sourcedBool(language i18n.Lang, setting store.Setting[bool]) string {
	value := i18n.Messages.Panel.Settings.Common.Off.For(language)
	if setting.Value {
		value = i18n.Messages.Panel.Settings.Common.On.For(language)
	}
	return i18n.Messages.Panel.Settings.Value.Sourced.Render(language, value, v.sourceText(language, setting.Source))
}

func (v *Panel) sourcedMode(language i18n.Lang, setting store.Setting[string]) string {
	return i18n.Messages.Panel.Settings.Value.Sourced.Render(language, v.modeText(language, setting.Value), v.sourceText(language, setting.Source))
}

func (v *Panel) sourcedLanguage(language i18n.Lang, setting store.Setting[string]) string {
	value := map[string]string{
		"zh":      i18n.Messages.Panel.Settings.Field.LanguageZH.For(language),
		"zh-Hant": i18n.Messages.Panel.Settings.Field.LanguageZHHant.For(language),
		"en":      i18n.Messages.Panel.Settings.Field.LanguageEN.For(language),
	}[setting.Value]
	return i18n.Messages.Panel.Settings.Value.Sourced.Render(language, value, v.sourceText(language, setting.Source))
}

func (v *Panel) sourcedSeconds(language i18n.Lang, setting store.Setting[int], permanent bool) string {
	value := i18n.Messages.Panel.Settings.Value.Seconds.Render(language, setting.Value)
	if permanent && setting.Value <= 0 {
		value = i18n.Messages.Panel.Settings.Value.Permanent.For(language)
	}
	return i18n.Messages.Panel.Settings.Value.Sourced.Render(language, value, v.sourceText(language, setting.Source))
}

func (v *Panel) sourcedLimit(language i18n.Lang, setting store.Setting[int]) string {
	value := i18n.Messages.Panel.Settings.Common.Off.For(language)
	if setting.Value > 0 {
		value = strconv.Itoa(setting.Value)
	}
	return i18n.Messages.Panel.Settings.Value.Sourced.Render(language, value, v.sourceText(language, setting.Source))
}

func (v *Panel) modeText(language i18n.Lang, mode string) string {
	switch mode {
	case config.ModeQuiz:
		return i18n.Messages.Panel.Settings.Mode.Quiz.For(language)
	case config.ModeMixed:
		return i18n.Messages.Panel.Settings.Mode.Mixed.For(language)
	default:
		return i18n.Messages.Panel.Settings.Mode.Kernel.For(language)
	}
}

func (v *Panel) requiredChannelID(group store.GroupView) int64 {
	if value := group.Overrides().RequiredChannelID; value != nil {
		return *value
	}
	return group.Baseline().RequiredChannelID.Value
}

func (v *Panel) channelDisplayValue(group store.GroupView) string {
	return group.ChannelDisplay().Value
}

func (v *Panel) listValues(group store.GroupView, kind inputKind) []int64 {
	switch kind {
	case inputChannelWhitelist:
		return group.ChannelWhitelist().Value
	case inputTrustedGroup:
		return group.TrustedMemberGroupIDs().Value
	case inputKnownChat:
		return group.KnownChatIDs().Value
	default:
		return nil
	}
}

func setListOverride(overrides *store.GroupOverrides, kind inputKind, values []int64) {
	values = append([]int64(nil), values...)
	switch kind {
	case inputTrustedGroup:
		overrides.TrustedMemberGroupIDs = &values
	case inputKnownChat:
		overrides.KnownChatIDs = &values
	}
}

func (v *Panel) listName(language i18n.Lang, kind inputKind) string {
	switch kind {
	case inputChannelWhitelist:
		return i18n.Messages.Panel.Settings.Field.ChannelWhitelist.For(language)
	case inputTrustedGroup:
		return i18n.Messages.Panel.Settings.Field.TrustedGroups.For(language)
	default:
		return i18n.Messages.Panel.Settings.Field.KnownChats.For(language)
	}
}

func summarize(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if utf8.RuneCountInString(value) <= 48 {
		return value
	}
	runes := []rune(value)
	return string(runes[:47]) + "…"
}

func (v *Panel) inputPrompt(language i18n.Lang, kind inputKind) string {
	prompts := &i18n.Messages.Panel.Settings.Prompt
	switch kind {
	case inputBanDuration:
		return prompts.BanDuration.For(language)
	case inputLookupTTL:
		return prompts.LookupTTL.For(language)
	case inputTimeout:
		return prompts.Timeout.For(language)
	case inputMaxFails:
		return prompts.MaxFails.For(language)
	case inputRetryCooldown:
		return prompts.RetryCooldown.For(language)
	case inputPrivateRate:
		return prompts.PrivateRate.For(language)
	case inputQuizQuestion:
		return prompts.QuizQuestion.For(language)
	case inputQuizOption:
		return prompts.QuizOption.For(language)
	case inputFallbackQuestion:
		return prompts.FallbackQuestion.For(language)
	case inputFallbackAnswer:
		return prompts.FallbackAnswer.For(language)
	case inputInviteURL:
		return prompts.InviteURL.For(language)
	case inputChannelWhitelist:
		return prompts.ChannelWhitelist.For(language)
	case inputTrustedGroup:
		return prompts.TrustedGroup.For(language)
	case inputKnownChat:
		return prompts.KnownChat.For(language)
	default:
		return prompts.RequiredChannel.For(language)
	}
}

func requestID() int {
	return int(time.Now().UnixNano() & 0x7fffffff)
}
