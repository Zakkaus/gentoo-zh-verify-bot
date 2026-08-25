package panel

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
)

type panelNoticeError struct{ text string }

func (e *panelNoticeError) Error() string { return e.text }

func (v *Panel) dispatchQuizBank(ctx context.Context, bot *telego.Bot, session *panelSession, group store.GroupView, data callbackData) error {
	switch data.field {
	case "go":
		return v.navigate(ctx, bot, session, data.value)
	case "pg":
		page, _ := decodeUnsigned(data.value)
		session.page = int(page)
		return v.renderSession(ctx, bot, session, session.groupID)
	case "ca":
		return v.armTextInput(ctx, bot, session, inputQuizQuestion, "qb")
	case "qq":
		indexValue, _ := decodeUnsigned(data.value)
		index := int(indexValue)
		questions := group.Questions().Value
		if index < 0 || index >= len(questions) {
			return &store.ConflictError{GroupID: session.groupID, Expected: session.revision, Actual: group.Revision()}
		}
		question := cloneQuestion(questions[index])
		session.quiz = &quizDraft{index: index, existing: true, question: question, revision: session.revision}
		session.screen = "qd"
		return v.renderSession(ctx, bot, session, session.groupID)
	default:
		return errors.New("invalid quiz-bank action")
	}
}

func (v *Panel) dispatchQuizDraft(ctx context.Context, bot *telego.Bot, session *panelSession, group store.GroupView, data callbackData) error {
	if session.quiz == nil || session.quiz.revision != session.revision {
		return &store.ConflictError{GroupID: session.groupID, Expected: session.revision, Actual: group.Revision()}
	}
	draft := session.quiz
	switch data.field {
	case "qq":
		return v.armTextInput(ctx, bot, session, inputQuizQuestion, "qd")
	case "qo":
		return v.armTextInput(ctx, bot, session, inputQuizOption, "qd")
	case "ok":
		indexValue, _ := decodeUnsigned(data.value)
		index := int(indexValue)
		if index < 0 || index >= len(draft.question.Options) {
			return &store.ConflictError{GroupID: session.groupID, Expected: session.revision, Actual: group.Revision()}
		}
		draft.question.Answer = index
		return v.renderSession(ctx, bot, session, session.groupID)
	case "dl":
		indexValue, _ := decodeUnsigned(data.value)
		index := int(indexValue)
		if index < 0 || index >= len(draft.question.Options) {
			return &store.ConflictError{GroupID: session.groupID, Expected: session.revision, Actual: group.Revision()}
		}
		draft.question.Options = append(draft.question.Options[:index], draft.question.Options[index+1:]...)
		switch {
		case draft.question.Answer == index:
			draft.question.Answer = -1
		case draft.question.Answer > index:
			draft.question.Answer--
		}
		return v.renderSession(ctx, bot, session, session.groupID)
	case "sv":
		if strings.TrimSpace(draft.question.Q) == "" || len(draft.question.Options) < 2 ||
			draft.question.Answer < 0 || draft.question.Answer >= len(draft.question.Options) {
			return &panelNoticeError{text: i18n.Messages.Panel.Settings.Error.QuestionNeedsOptions.For(session.language)}
		}
		questions := group.Questions().Value
		if draft.existing {
			if draft.index < 0 || draft.index >= len(questions) {
				return &store.ConflictError{GroupID: session.groupID, Expected: session.revision, Actual: group.Revision()}
			}
			questions[draft.index] = cloneQuestion(draft.question)
		} else {
			questions = append(questions, cloneQuestion(draft.question))
		}
		next := group.Overrides()
		next.Questions = &questions
		result, err := v.settings.CommitGroup(session.groupID, session.revision, next)
		if err != nil {
			return err
		}
		session.revision = result.Revision
		session.quiz = nil
		session.screen = "qb"
		return v.renderSession(ctx, bot, session, session.groupID)
	case "rm":
		if !draft.existing {
			return errors.New("new quiz question cannot be deleted")
		}
		session.confirm = &confirmation{kind: "quiz", index: draft.index, revision: draft.revision}
		session.screen = "cf"
		return v.renderSession(ctx, bot, session, session.groupID)
	case "cn":
		session.quiz = nil
		session.screen = "qb"
		return v.renderSession(ctx, bot, session, session.groupID)
	default:
		return errors.New("invalid quiz-draft action")
	}
}

func (v *Panel) dispatchFallbackBank(ctx context.Context, bot *telego.Bot, session *panelSession, group store.GroupView, data callbackData) error {
	switch data.field {
	case "go":
		return v.navigate(ctx, bot, session, data.value)
	case "pg":
		page, _ := decodeUnsigned(data.value)
		session.page = int(page)
		return v.renderSession(ctx, bot, session, session.groupID)
	case "ca":
		return v.armTextInput(ctx, bot, session, inputFallbackQuestion, "fb")
	case "fq":
		indexValue, _ := decodeUnsigned(data.value)
		index := int(indexValue)
		questions := group.FallbackQuestions().Value
		if group.FallbackBuiltin().Value || index < 0 || index >= len(questions) {
			return &store.ConflictError{GroupID: session.groupID, Expected: session.revision, Actual: group.Revision()}
		}
		session.fallback = &fallbackDraft{index: index, existing: true, question: cloneShortQuestion(questions[index]), revision: session.revision}
		session.screen = "fd"
		return v.renderSession(ctx, bot, session, session.groupID)
	case "rb":
		session.confirm = &confirmation{kind: "fallback_builtin", revision: session.revision}
		session.screen = "cf"
		return v.renderSession(ctx, bot, session, session.groupID)
	default:
		return errors.New("invalid fallback-bank action")
	}
}

func (v *Panel) dispatchFallbackDraft(ctx context.Context, bot *telego.Bot, session *panelSession, group store.GroupView, data callbackData) error {
	if session.fallback == nil || session.fallback.revision != session.revision {
		return &store.ConflictError{GroupID: session.groupID, Expected: session.revision, Actual: group.Revision()}
	}
	draft := session.fallback
	switch data.field {
	case "fq":
		return v.armTextInput(ctx, bot, session, inputFallbackQuestion, "fd")
	case "fa":
		return v.armTextInput(ctx, bot, session, inputFallbackAnswer, "fd")
	case "dl":
		indexValue, _ := decodeUnsigned(data.value)
		index := int(indexValue)
		if index < 0 || index >= len(draft.question.Answers) {
			return &store.ConflictError{GroupID: session.groupID, Expected: session.revision, Actual: group.Revision()}
		}
		draft.question.Answers = append(draft.question.Answers[:index], draft.question.Answers[index+1:]...)
		return v.renderSession(ctx, bot, session, session.groupID)
	case "sv":
		if strings.TrimSpace(draft.question.Q) == "" || len(draft.question.Answers) == 0 {
			return &panelNoticeError{text: i18n.Messages.Panel.Settings.Error.FallbackNeedsAnswer.For(session.language)}
		}
		questions := group.FallbackQuestions().Value
		if group.FallbackBuiltin().Value {
			questions = nil
		}
		if draft.existing {
			if draft.index < 0 || draft.index >= len(questions) {
				return &store.ConflictError{GroupID: session.groupID, Expected: session.revision, Actual: group.Revision()}
			}
			questions[draft.index] = cloneShortQuestion(draft.question)
		} else {
			questions = append(questions, cloneShortQuestion(draft.question))
		}
		builtin := false
		next := group.Overrides()
		next.FallbackBuiltin = &builtin
		next.FallbackQuestions = &questions
		result, err := v.settings.CommitGroup(session.groupID, session.revision, next)
		if err != nil {
			return err
		}
		session.revision = result.Revision
		session.fallback = nil
		session.screen = "fb"
		return v.renderSession(ctx, bot, session, session.groupID)
	case "rm":
		if !draft.existing {
			return errors.New("new fallback question cannot be deleted")
		}
		session.confirm = &confirmation{kind: "fallback", index: draft.index, revision: draft.revision}
		session.screen = "cf"
		return v.renderSession(ctx, bot, session, session.groupID)
	case "cn":
		session.fallback = nil
		session.screen = "fb"
		return v.renderSession(ctx, bot, session, session.groupID)
	default:
		return errors.New("invalid fallback-draft action")
	}
}

func (v *Panel) dispatchChannel(ctx context.Context, bot *telego.Bot, session *panelSession, group store.GroupView, data callbackData) error {
	switch data.field {
	case "go":
		return v.navigate(ctx, bot, session, data.value)
	case "ci":
		return v.armChatInput(ctx, bot, session, inputRequiredChannel, "ch")
	case "iu":
		if v.requiredChannelID(group) == 0 {
			return &panelNoticeError{text: i18n.Messages.Panel.Settings.Error.InvalidChat.For(session.language)}
		}
		return v.armTextInput(ctx, bot, session, inputInviteURL, "ch")
	case "dl":
		if !strings.HasPrefix(group.ChannelDisplay().Value, "@") {
			return &panelNoticeError{text: i18n.Messages.Panel.Settings.Error.InvalidURL.For(session.language)}
		}
		invite := ""
		next := group.Overrides()
		next.ChannelInviteURL = &invite
		result, err := v.settings.CommitGroup(session.groupID, session.revision, next)
		if err != nil {
			return err
		}
		session.revision = result.Revision
		return v.renderSession(ctx, bot, session, session.groupID)
	case "ds":
		session.confirm = &confirmation{kind: "channel", revision: session.revision}
		session.screen = "cf"
		return v.renderSession(ctx, bot, session, session.groupID)
	default:
		return errors.New("invalid channel action")
	}
}

func (v *Panel) dispatchConfirmation(ctx context.Context, bot *telego.Bot, session *panelSession, group store.GroupView, data callbackData) error {
	confirmation := session.confirm
	if confirmation == nil || confirmation.revision != session.revision {
		return &store.ConflictError{GroupID: session.groupID, Expected: session.revision, Actual: group.Revision()}
	}
	if data.field == "cn" {
		session.screen = confirmationParent(confirmation.kind)
		session.confirm = nil
		return v.renderSession(ctx, bot, session, session.groupID)
	}
	if data.field != "ok" {
		return errors.New("invalid confirmation action")
	}
	next := group.Overrides()
	switch confirmation.kind {
	case "quiz":
		questions := group.Questions().Value
		if confirmation.index < 0 || confirmation.index >= len(questions) {
			return &store.ConflictError{GroupID: session.groupID, Expected: session.revision, Actual: group.Revision()}
		}
		questions = append(questions[:confirmation.index], questions[confirmation.index+1:]...)
		next.Questions = &questions
	case "fallback":
		questions := group.FallbackQuestions().Value
		if group.FallbackBuiltin().Value || confirmation.index < 0 || confirmation.index >= len(questions) {
			return &store.ConflictError{GroupID: session.groupID, Expected: session.revision, Actual: group.Revision()}
		}
		questions = append(questions[:confirmation.index], questions[confirmation.index+1:]...)
		if len(questions) == 0 {
			builtin := true
			next.FallbackBuiltin = &builtin
			next.FallbackQuestions = nil
		} else {
			next.FallbackQuestions = &questions
		}
	case "fallback_builtin":
		builtin := true
		next.FallbackBuiltin = &builtin
		next.FallbackQuestions = nil
	case "channel":
		id := int64(0)
		display, invite := "", ""
		next.RequiredChannelID = &id
		next.ChannelDisplay = &display
		next.ChannelInviteURL = &invite
	default:
		return errors.New("unknown confirmation")
	}
	result, err := v.settings.CommitGroup(session.groupID, session.revision, next)
	if err != nil {
		return err
	}
	session.revision = result.Revision
	parent := confirmationParent(confirmation.kind)
	session.confirm = nil
	session.quiz = nil
	session.fallback = nil
	session.screen = parent
	return v.renderSession(ctx, bot, session, session.groupID)
}

func confirmationParent(kind string) string {
	switch kind {
	case "quiz":
		return "qb"
	case "fallback", "fallback_builtin":
		return "fb"
	default:
		return "ch"
	}
}

func (v *Panel) armTextInput(ctx context.Context, bot *telego.Bot, session *panelSession, kind inputKind, parent string) error {
	if v.kernelPending(session.ownerID) {
		return &panelNoticeError{text: i18n.Messages.Panel.Settings.Error.InputBlockedVerification.For(session.language)}
	}
	pending := &pendingInput{kind: kind, parent: parent, expectedRevision: session.revision}
	session.pending = pending
	session.screen = "in"
	if err := v.renderSession(ctx, bot, session, session.groupID); err != nil {
		session.pending = nil
		return err
	}
	message, err := bot.SendMessage(ctx, tu.Message(tu.ID(session.chatID), v.inputPrompt(session.language, kind)).
		WithReplyMarkup(&telego.ForceReply{ForceReply: true, Selective: true}))
	if err != nil || message == nil {
		session.pending = nil
		if err == nil {
			err = errors.New("telegram returned no input prompt")
		}
		return err
	}
	pending.promptMessageID = message.MessageID
	return nil
}

func (v *Panel) armChatInput(ctx context.Context, bot *telego.Bot, session *panelSession, kind inputKind, parent string) error {
	if v.kernelPending(session.ownerID) {
		return &panelNoticeError{text: i18n.Messages.Panel.Settings.Error.InputBlockedVerification.For(session.language)}
	}
	primary := requestID()
	pending := &pendingInput{kind: kind, parent: parent, requestID: primary, expectedRevision: session.revision}
	session.pending = pending
	session.screen = "in"
	if err := v.renderSession(ctx, bot, session, session.groupID); err != nil {
		session.pending = nil
		return err
	}
	isChannel := kind == inputChannelWhitelist || kind == inputRequiredChannel
	buttonText := i18n.Messages.Panel.Settings.Field.ChatGroup.For(session.language)
	if isChannel {
		buttonText = i18n.Messages.Panel.Settings.Field.ChatChannel.For(session.language)
	}
	buttons := []telego.KeyboardButton{{
		Text: buttonText,
		RequestChat: (&telego.KeyboardButtonRequestChat{RequestID: int32(primary), ChatIsChannel: isChannel}).
			WithRequestTitle(true).WithRequestUsername(true).WithBotIsMember(true),
	}}
	if kind == inputKnownChat {
		alternative := primary + 1
		pending.requestAltID = alternative
		buttons = append(buttons, telego.KeyboardButton{
			Text: i18n.Messages.Panel.Settings.Field.ChatChannel.For(session.language),
			RequestChat: (&telego.KeyboardButtonRequestChat{RequestID: int32(alternative), ChatIsChannel: true}).
				WithRequestTitle(true).WithRequestUsername(true).WithBotIsMember(true),
		})
	}
	message, err := bot.SendMessage(ctx, tu.Message(tu.ID(session.chatID), v.inputPrompt(session.language, kind)).
		WithReplyMarkup((&telego.ReplyKeyboardMarkup{Keyboard: [][]telego.KeyboardButton{buttons}}).
			WithResizeKeyboard().WithOneTimeKeyboard().WithSelective()))
	if err != nil || message == nil {
		session.pending = nil
		if err == nil {
			err = errors.New("telegram returned no chat-picker prompt")
		}
		return err
	}
	pending.promptMessageID = message.MessageID
	return nil
}

func (v *Panel) cancelInput(ctx context.Context, bot *telego.Bot, session *panelSession, data callbackData) error {
	if data.field != "cn" {
		return errors.New("invalid input cancellation")
	}
	if session.pending == nil {
		v.finishSession(ctx, bot, session, i18n.Messages.Panel.Settings.Error.InputCanceledVerification.For(session.language))
		return nil
	}
	pending := session.pending
	v.rememberCanceledPrompt(session, pending)
	session.pending = nil
	session.screen = pending.parent
	v.removeReplyKeyboard(ctx, bot, session.chatID, session.language)
	if pending.promptMessageID != 0 {
		_ = bot.DeleteMessage(ctx, &telego.DeleteMessageParams{ChatID: tu.ID(session.chatID), MessageID: pending.promptMessageID})
	}
	return v.renderSession(ctx, bot, session, session.groupID)
}

// OnPanelInput applies one exact ForceReply-bound text submission.
func (v *Panel) OnPanelInput(ctx *th.Context, update telego.Update) error {
	message := update.Message
	if message == nil || message.From == nil || message.ReplyToMessage == nil {
		return nil
	}
	key := promptKey{userID: message.From.ID, messageID: message.ReplyToMessage.MessageID}
	if tombstone, ok := v.consumeTombstone(key); ok {
		_, _ = ctx.Bot().SendMessage(ctx.Context(), tu.Message(tu.ID(message.Chat.ID), i18n.Messages.Panel.Settings.Error.InputCanceledVerification.For(tombstone.language)))
		return nil
	}
	session := v.sessionByUser(message.From.ID)
	if session == nil {
		return nil
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	pending := session.pending
	if pending == nil || pending.promptMessageID != key.messageID {
		return nil
	}
	if !v.authorizeInput(ctx.Context(), ctx.Bot(), session) {
		return nil
	}
	group, ok := v.settings.Group(session.groupID)
	if !ok || group.Revision() != pending.expectedRevision {
		v.finishSession(ctx.Context(), ctx.Bot(), session, i18n.Messages.Panel.Settings.Error.ConcurrentChange.For(session.language))
		return nil
	}
	text := strings.TrimSpace(message.Text)
	if text == "" {
		v.sendInputError(ctx.Context(), ctx.Bot(), session, i18n.Messages.Panel.Settings.Error.InvalidInput.For(session.language))
		return nil
	}
	session.pending = nil
	if err := v.applyTextInput(ctx.Context(), ctx.Bot(), session, group, pending, text); err != nil {
		var notice *panelNoticeError
		if errors.As(err, &notice) {
			session.pending = pending
			v.sendInputError(ctx.Context(), ctx.Bot(), session, notice.text)
			return nil
		}
		if errors.Is(err, store.ErrSettingsConflict) {
			v.finishSession(ctx.Context(), ctx.Bot(), session, i18n.Messages.Panel.Settings.Error.ConcurrentChange.For(session.language))
			return nil
		}
		v.finishSession(ctx.Context(), ctx.Bot(), session, i18n.Messages.Panel.Settings.Error.SaveFailed.For(session.language))
		return nil
	}
	_ = ctx.Bot().DeleteMessage(ctx.Context(), &telego.DeleteMessageParams{ChatID: tu.ID(session.chatID), MessageID: pending.promptMessageID})
	_ = ctx.Bot().DeleteMessage(ctx.Context(), &telego.DeleteMessageParams{ChatID: tu.ID(session.chatID), MessageID: message.MessageID})
	v.removeReplyKeyboard(ctx.Context(), ctx.Bot(), session.chatID, session.language)
	return nil
}

// OnPanelChatShared validates and applies one exact Telegram chat-picker result.
func (v *Panel) OnPanelChatShared(ctx *th.Context, update telego.Update) error {
	message := update.Message
	if message == nil || message.From == nil || message.ChatShared == nil {
		return nil
	}
	session := v.sessionByUser(message.From.ID)
	if session == nil {
		return nil
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	pending := session.pending
	request := message.ChatShared.RequestID
	if pending == nil || (pending.requestID != request && pending.requestAltID != request) {
		return nil
	}
	if !v.authorizeInput(ctx.Context(), ctx.Bot(), session) {
		return nil
	}
	if v.kernelPending(session.ownerID) {
		session.pending = nil
		v.removeReplyKeyboard(ctx.Context(), ctx.Bot(), session.chatID, session.language)
		_, _ = ctx.Bot().SendMessage(ctx.Context(), tu.Message(tu.ID(session.chatID),
			i18n.Messages.Panel.Settings.Error.InputCanceledVerification.For(session.language)))
		return nil
	}
	group, ok := v.settings.Group(session.groupID)
	if !ok || group.Revision() != pending.expectedRevision {
		v.finishSession(ctx.Context(), ctx.Bot(), session, i18n.Messages.Panel.Settings.Error.ConcurrentChange.For(session.language))
		return nil
	}
	sharedID := message.ChatShared.ChatID
	chat, valid := v.validateSharedChat(ctx.Context(), ctx.Bot(), session, pending.kind, sharedID)
	if !valid {
		v.sendInputError(ctx.Context(), ctx.Bot(), session, i18n.Messages.Panel.Settings.Error.InvalidChat.For(session.language))
		return nil
	}
	session.pending = nil
	if err := v.applySharedChat(ctx.Context(), ctx.Bot(), session, group, pending, chat); err != nil {
		var notice *panelNoticeError
		if errors.As(err, &notice) {
			session.pending = pending
			v.sendInputError(ctx.Context(), ctx.Bot(), session, notice.text)
			return nil
		}
		if errors.Is(err, store.ErrSettingsConflict) {
			v.finishSession(ctx.Context(), ctx.Bot(), session, i18n.Messages.Panel.Settings.Error.ConcurrentChange.For(session.language))
		} else {
			v.finishSession(ctx.Context(), ctx.Bot(), session, i18n.Messages.Panel.Settings.Error.SaveFailed.For(session.language))
		}
		return nil
	}
	_ = ctx.Bot().DeleteMessage(ctx.Context(), &telego.DeleteMessageParams{ChatID: tu.ID(session.chatID), MessageID: pending.promptMessageID})
	_ = ctx.Bot().DeleteMessage(ctx.Context(), &telego.DeleteMessageParams{ChatID: tu.ID(session.chatID), MessageID: message.MessageID})
	v.removeReplyKeyboard(ctx.Context(), ctx.Bot(), session.chatID, session.language)
	return nil
}

func (v *Panel) authorizeInput(ctx context.Context, bot *telego.Bot, session *panelSession) bool {
	admin, err := v.telegram.FreshAdmin(ctx, session.groupID, session.ownerID)
	if err != nil {
		_, _ = bot.SendMessage(ctx, tu.Message(tu.ID(session.chatID), i18n.Messages.Panel.Settings.Error.AuthorizationCheckFailed.For(session.language)))
		return false
	}
	if !admin {
		v.finishSession(ctx, bot, session, i18n.Messages.Panel.Settings.Error.AuthorizationLost.For(session.language))
		return false
	}
	return true
}

func (v *Panel) applyTextInput(ctx context.Context, bot *telego.Bot, session *panelSession, group store.GroupView, pending *pendingInput, text string) error {
	next := group.Overrides()
	commit := true
	switch pending.kind {
	case inputBanDuration:
		value, ok := parsePanelBanDuration(text)
		if !ok {
			return &panelNoticeError{text: i18n.Messages.Panel.Settings.Error.InvalidDuration.For(session.language)}
		}
		next.BanSeconds = &value
	case inputLookupTTL:
		minutes, ok := parseBoundedPositive(text, 1, 1440)
		if !ok {
			return &panelNoticeError{text: i18n.Messages.Panel.Settings.Error.InvalidNumber.For(session.language)}
		}
		seconds := minutes * 60
		next.LookupTTLSeconds = &seconds
	case inputTimeout:
		value, ok := parseBoundedPositive(text, 30, 1800)
		if !ok {
			return &panelNoticeError{text: i18n.Messages.Panel.Settings.Error.InvalidNumber.For(session.language)}
		}
		next.TimeoutSeconds = &value
	case inputMaxFails:
		value, ok := parsePositiveOrOff(text)
		if !ok {
			return &panelNoticeError{text: i18n.Messages.Panel.Settings.Error.InvalidNumber.For(session.language)}
		}
		next.VerifyMaxFails = &value
	case inputRetryCooldown:
		value, ok := parsePositiveOrOff(text)
		if !ok {
			return &panelNoticeError{text: i18n.Messages.Panel.Settings.Error.InvalidNumber.For(session.language)}
		}
		next.VerifyRetrySeconds = &value
	case inputPrivateRate:
		if session.groupID != v.settings.ControlGroupID() {
			return &panelNoticeError{text: i18n.Messages.Panel.Settings.Error.ControlGroupOnly.For(session.language)}
		}
		value, ok := parseBoundedPositive(text, 1, 1<<30)
		if !ok {
			return &panelNoticeError{text: i18n.Messages.Panel.Settings.Error.InvalidNumber.For(session.language)}
		}
		global := v.settings.Global()
		if global.Revision() != session.globalRevision {
			return store.ErrSettingsConflict
		}
		overrides := global.Overrides()
		overrides.PrivateQueryPerMin = &value
		result, err := v.settings.CommitGlobal(session.globalRevision, overrides)
		if err != nil {
			return err
		}
		session.globalRevision = result.Revision
		commit = false
	case inputQuizQuestion:
		commit = false
		if session.quiz == nil {
			session.quiz = &quizDraft{index: -1, question: config.Question{Q: text, Answer: -1}, revision: session.revision}
		} else {
			session.quiz.question.Q = text
		}
		session.screen = "qd"
	case inputQuizOption:
		commit = false
		if session.quiz == nil {
			return errors.New("missing quiz draft")
		}
		session.quiz.question.Options = append(session.quiz.question.Options, text)
		session.screen = "qd"
	case inputFallbackQuestion:
		commit = false
		if session.fallback == nil {
			session.fallback = &fallbackDraft{index: -1, question: config.ShortQuestion{Q: text}, revision: session.revision}
		} else {
			session.fallback.question.Q = text
		}
		session.screen = "fd"
	case inputFallbackAnswer:
		commit = false
		if session.fallback == nil {
			return errors.New("missing fallback draft")
		}
		session.fallback.question.Answers = append(session.fallback.question.Answers, text)
		session.screen = "fd"
	case inputInviteURL:
		if !validTelegramURL(text) {
			return &panelNoticeError{text: i18n.Messages.Panel.Settings.Error.InvalidURL.For(session.language)}
		}
		invite := text
		next.ChannelInviteURL = &invite
		if session.channel != nil {
			next.RequiredChannelID = &session.channel.id
			next.ChannelDisplay = &session.channel.display
			session.channel = nil
		}
	default:
		return errors.New("unknown panel input")
	}
	if commit {
		result, err := v.settings.CommitGroup(session.groupID, session.revision, next)
		if err != nil {
			return err
		}
		session.revision = result.Revision
	}
	if session.screen == "in" {
		session.screen = pending.parent
	}
	return v.renderSession(ctx, bot, session, session.groupID)
}

func (v *Panel) validateSharedChat(ctx context.Context, bot *telego.Bot, session *panelSession, kind inputKind, chatID int64) (*telego.ChatFullInfo, bool) {
	chat, err := bot.GetChat(ctx, &telego.GetChatParams{ChatID: tu.ID(chatID)})
	if err != nil || chat == nil {
		return nil, false
	}
	member, err := bot.GetChatMember(ctx, &telego.GetChatMemberParams{ChatID: tu.ID(chatID), UserID: session.ownerID})
	if err != nil || member == nil || member.MemberStatus() == telego.MemberStatusLeft || member.MemberStatus() == telego.MemberStatusBanned {
		return nil, false
	}
	if kind == inputRequiredChannel {
		botID, _, err := v.botIdentity(ctx, bot)
		if err != nil {
			return nil, false
		}
		botMember, err := bot.GetChatMember(ctx, &telego.GetChatMemberParams{ChatID: tu.ID(chatID), UserID: botID})
		if err != nil || botMember == nil || botMember.MemberStatus() == telego.MemberStatusLeft || botMember.MemberStatus() == telego.MemberStatusBanned {
			return nil, false
		}
	}
	return chat, true
}

func (v *Panel) applySharedChat(ctx context.Context, bot *telego.Bot, session *panelSession, group store.GroupView, pending *pendingInput, chat *telego.ChatFullInfo) error {
	sharedID := chat.ID
	if pending.kind == inputTrustedGroup && sharedID == session.groupID {
		return &panelNoticeError{text: i18n.Messages.Panel.Settings.Error.InvalidChat.For(session.language)}
	}
	if pending.kind == inputRequiredChannel {
		display := chat.Title
		if chat.Username != "" {
			display = "@" + chat.Username
		}
		if chat.Username == "" {
			session.channel = &channelDraft{id: sharedID, display: display}
			return v.armTextInput(ctx, bot, session, inputInviteURL, "ch")
		}
		next := group.Overrides()
		next.RequiredChannelID = &sharedID
		next.ChannelDisplay = &display
		result, err := v.settings.CommitGroup(session.groupID, session.revision, next)
		if err != nil {
			return err
		}
		session.revision = result.Revision
		session.screen = "ch"
		return v.renderSession(ctx, bot, session, session.groupID)
	}
	if pending.kind == inputChannelWhitelist {
		if err := v.updateChannelWhitelist(ctx, bot, session, sharedID, true); err != nil {
			return err
		}
		session.screen = pending.parent
		return v.renderSession(ctx, bot, session, session.groupID)
	}
	values := v.listValues(group, pending.kind)
	for _, existing := range values {
		if existing == sharedID {
			session.screen = pending.parent
			return v.renderSession(ctx, bot, session, session.groupID)
		}
	}
	values = append(values, sharedID)
	next := group.Overrides()
	setListOverride(&next, pending.kind, values)
	result, err := v.settings.CommitGroup(session.groupID, session.revision, next)
	if err != nil {
		return err
	}
	session.revision = result.Revision

	session.screen = pending.parent
	return v.renderSession(ctx, bot, session, session.groupID)
}

func (v *Panel) updateChannelWhitelist(ctx context.Context, bot *telego.Bot, session *panelSession, senderID int64, allow bool) error {
	unbanErr, err := v.moderation.UpdateChannelWhitelist(ctx, session.groupID, senderID, allow)
	if err != nil {
		return err
	}
	group, ok := v.settings.Group(session.groupID)
	if !ok {
		return store.ErrUnknownGroup
	}
	session.revision = group.Revision()
	if unbanErr != nil {
		v.sendInputError(ctx, bot, session, i18n.Messages.Panel.Settings.Error.WhitelistUnbanFailed.For(session.language))
	}
	return nil
}

func (v *Panel) sendInputError(ctx context.Context, bot *telego.Bot, session *panelSession, text string) {
	_, _ = bot.SendMessage(ctx, tu.Message(tu.ID(session.chatID), text))
}

func (v *Panel) removeReplyKeyboard(ctx context.Context, bot *telego.Bot, chatID int64, language i18n.Lang) {
	message, err := bot.SendMessage(ctx, tu.Message(tu.ID(chatID), i18n.Messages.Panel.Settings.Common.Close.For(language)).
		WithReplyMarkup(&telego.ReplyKeyboardRemove{RemoveKeyboard: true}))
	if err == nil && message != nil {
		_ = bot.DeleteMessage(ctx, &telego.DeleteMessageParams{ChatID: tu.ID(chatID), MessageID: message.MessageID})
	}
}

func parsePanelBanDuration(value string) (int, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "0" {
		return 0, true
	}
	if value == "" {
		return 0, false
	}
	multiplier := 1
	switch value[len(value)-1] {
	case 's':
		value = value[:len(value)-1]
	case 'm':
		multiplier, value = 60, value[:len(value)-1]
	case 'h':
		multiplier, value = 3600, value[:len(value)-1]
	case 'd':
		multiplier, value = 86400, value[:len(value)-1]
	}
	number, err := strconv.Atoi(value)
	if err != nil || number < 0 || number > 1<<31 {
		return 0, false
	}
	return config.ClampBanSeconds(number * multiplier), true
}

func parseBoundedPositive(value string, minimum, maximum int) (int, bool) {
	number, err := strconv.Atoi(strings.TrimSpace(value))
	return number, err == nil && number >= minimum && number <= maximum
}

func parsePositiveOrOff(value string) (int, bool) {
	if strings.EqualFold(strings.TrimSpace(value), "off") {
		return -1, true
	}
	return parseBoundedPositive(value, 1, 1<<30)
}

func validTelegramURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() != "t.me" || strings.Trim(parsed.Path, "/") == "" {
		return false
	}
	return parsed.RawQuery == "" && parsed.Fragment == ""
}

func cloneQuestion(value config.Question) config.Question {
	value.Options = append([]string(nil), value.Options...)
	return value
}

func cloneShortQuestion(value config.ShortQuestion) config.ShortQuestion {
	value.Answers = append([]string(nil), value.Answers...)
	return value
}
