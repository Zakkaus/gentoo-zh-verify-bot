package panel

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/mymmrac/telego"
)

func panelTestGroup(t *testing.T, settings *store.Settings) store.GroupView {
	t.Helper()
	group, ok := settings.Group(panelTestGroupA)
	if !ok {
		t.Fatalf("missing test group %d", panelTestGroupA)
	}
	return group
}

func assertGroupOverrides(t *testing.T, settings *store.Settings, want store.GroupOverrides) store.GroupView {
	t.Helper()
	group := panelTestGroup(t, settings)
	if got := group.Overrides(); !reflect.DeepEqual(got, want) {
		t.Fatalf("group overrides = %+v, want only %+v", got, want)
	}
	return group
}

func assertGlobalOverrides(t *testing.T, settings *store.Settings, want store.GlobalOverrides) store.GlobalView {
	t.Helper()
	global := settings.Global()
	if got := global.Overrides(); !reflect.DeepEqual(got, want) {
		t.Fatalf("global overrides = %+v, want only %+v", got, want)
	}
	return global
}

func expectedRuntimeScreen(panel *Panel, group store.GroupView, language i18n.Lang) string {
	return i18n.Messages.Panel.Settings.Screen.Runtime.Render(language, group.ID(),
		panel.sourcedBool(language, group.Enabled()), panel.sourcedMode(language, group.VerifyMode()),
		panel.sourcedDeliveryMode(language, group.DeliveryMode()), panel.sourcedBool(language, group.NameSpoiler()),
		panel.sourcedSeconds(language, group.BanSeconds(), true),
		panel.sourcedBool(language, group.LookupAutoDeleteEnabled()), panel.sourcedSeconds(language, group.LookupTTLSeconds(), false),
		panel.sourcedLanguage(language, group.Lang()))
}

func expectedVerificationScreen(panel *Panel, settings *store.Settings, group store.GroupView, language i18n.Lang) string {
	global := settings.Global()
	return i18n.Messages.Panel.Settings.Screen.Verification.Render(language, group.ID(),
		panel.sourcedSeconds(language, group.TimeoutSeconds(), false), panel.sourcedLimit(language, group.VerifyMaxFails()),
		panel.sourcedLimit(language, group.VerifyRetrySeconds()),
		panel.sourcedBool(language, group.VerifyInvited()),
		i18n.Messages.Panel.Settings.Value.Sourced.Render(language, strconv.Itoa(global.PrivateQueryPerMin().Value),
			panel.sourceText(language, global.PrivateQueryPerMin().Source)))
}

func expectedModerationScreen(panel *Panel, settings *store.Settings, group store.GroupView, language i18n.Lang) string {
	global := settings.Global()
	return i18n.Messages.Panel.Settings.Screen.Moderation.Render(language, group.ID(),
		panel.sourcedBool(language, group.AntispamEnabled()),
		panel.sourcedSeconds(language, group.MuteSeconds(), false),
		panel.sourcedLimit(language, group.WarnLimit()),
		panel.sourcedBool(language, global.RichMessages()),
		i18n.Messages.Panel.Settings.Value.Sourced.Render(language,
			panel.alertChatText(language, global.AdminLogChatID().Value),
			panel.sourceText(language, global.AdminLogChatID().Source)))
}

func expectedListScreen(panel *Panel, group store.GroupView, language i18n.Lang, kind inputKind) string {
	values := panel.listValues(group, kind)
	lines := make([]string, 0, len(values))
	for _, id := range values {
		lines = append(lines, i18n.Messages.Panel.Settings.Value.IDItem.Render(language, id))
	}
	return i18n.Messages.Panel.Settings.Screen.List.Render(language, panel.listName(language, kind), group.ID(), len(values), strings.Join(lines, "\n"))
}

func expectedQuizBankScreen(group store.GroupView, language i18n.Lang) string {
	questions := group.Questions().Value
	lines := make([]string, 0, len(questions))
	for index, question := range questions {
		lines = append(lines, i18n.Messages.Panel.Settings.Value.QuestionItem.Render(
			language, index+1, summarize(question.Q)))
	}
	return i18n.Messages.Panel.Settings.Screen.QuizBank.Render(
		language, group.ID(), len(questions), strings.Join(lines, "\n"))
}

func expectedFallbackBankScreen(group store.GroupView, language i18n.Lang) string {
	questions := group.FallbackQuestions().Value
	lines := make([]string, 0, len(questions))
	for index, question := range questions {
		lines = append(lines, i18n.Messages.Panel.Settings.Value.QuestionItem.Render(
			language, index+1, summarize(question.Q)))
	}
	bank := i18n.Messages.Panel.Settings.Value.Custom.For(language)
	if group.FallbackBuiltin().Value {
		bank = i18n.Messages.Panel.Settings.Value.Builtins.For(language)
	}
	return i18n.Messages.Panel.Settings.Screen.FallbackBank.Render(
		language, group.ID(), bank, len(questions), strings.Join(lines, "\n"))
}

func expectedChannelScreen(panel *Panel, group store.GroupView, language i18n.Lang) string {
	display := panel.channelDisplayValue(group)
	if display == "" {
		display = i18n.Messages.Panel.Settings.Common.None.For(language)
	}
	invite := group.ChannelInviteURL().Value
	if invite == "" {
		invite = i18n.Messages.Panel.Settings.Value.InviteMissing.For(language)
	}
	return i18n.Messages.Panel.Settings.Screen.Channel.Render(language, group.ID(), panel.requiredChannelID(group), display, invite)
}

func bumpPanelGroupRevision(t *testing.T, settings *store.Settings) store.GroupOverrides {
	t.Helper()
	group := panelTestGroup(t, settings)
	next := group.Overrides()
	value := !group.Enabled().Value
	next.Enabled = &value
	if _, err := settings.CommitGroup(group.ID(), group.Revision(), next); err != nil {
		t.Fatal(err)
	}
	return next
}

func panelCallbackActions(t *testing.T, keyboard *telego.InlineKeyboardMarkup, token, screen string) []string {
	t.Helper()
	var actions []string
	for _, row := range keyboard.InlineKeyboard {
		for _, button := range row {
			data, err := parseCallback(button.CallbackData)
			if err != nil {
				t.Fatalf("parse callback %q: %v", button.CallbackData, err)
			}
			if data.token != token || data.screen != screen {
				t.Fatalf("callback %+v, want token %q screen %q", data, token, screen)
			}
			actions = append(actions, fmt.Sprintf("%d:%s:%s", data.group, data.field, data.value))
		}
	}
	return actions
}

func action(groupID int64, field, value string) string {
	return fmt.Sprintf("%d:%s:%s", groupID, field, value)
}

func testSettingsScreenContracts(t *testing.T) {
	const trustedID int64 = -1009000000711
	question := config.Question{Q: "Question", Options: []string{"A", "B"}, Answer: 0}
	fallbackQuestion := config.ShortQuestion{Q: "Fallback question", Answers: []string{"Answer"}}
	channelID := int64(-1009000000712)
	channelDisplay := "@public_channel"
	channelInvite := "https://t.me/public_channel"
	trusted := []int64{trustedID}
	questions := []config.Question{question}
	fallbackQuestions := []config.ShortQuestion{fallbackQuestion}
	customFallback := false

	panel, settings, _, bot := newSettingsPanelTest(t, "")
	group := panelTestGroup(t, settings)
	next := group.Overrides()
	next.TrustedMemberGroupIDs = &trusted
	next.Questions = &questions
	next.FallbackQuestions = &fallbackQuestions
	next.FallbackBuiltin = &customFallback
	next.RequiredChannelID = &channelID
	next.ChannelDisplay = &channelDisplay
	next.ChannelInviteURL = &channelInvite
	if _, err := settings.CommitGroup(group.ID(), group.Revision(), next); err != nil {
		t.Fatal(err)
	}
	group = panelTestGroup(t, settings)
	session := addPanelSession(t, panel, settings, panelTestGroupA, "gh")
	const token = "0123456789abcdef"
	language := i18n.LangEN

	groupALabel := i18n.Messages.Panel.Settings.Value.GroupButton.Render(language, fmt.Sprintf("Group %d", panelTestGroupA), panelTestGroupA)
	groupBLabel := i18n.Messages.Panel.Settings.Value.GroupButton.Render(language, fmt.Sprintf("Group %d", panelTestGroupB), panelTestGroupB)
	trustedLine := i18n.Messages.Panel.Settings.Value.IDItem.Render(language, trustedID)
	questionLine := i18n.Messages.Panel.Settings.Value.QuestionItem.Render(language, 1, summarize(question.Q))
	fallbackLine := i18n.Messages.Panel.Settings.Value.QuestionItem.Render(language, 1, summarize(fallbackQuestion.Q))
	optionLines := []string{
		i18n.Messages.Panel.Settings.Value.OptionItem.Render(language, 1, question.Options[0]),
		i18n.Messages.Panel.Settings.Value.OptionItem.Render(language, 2, question.Options[1]),
	}
	answerLine := i18n.Messages.Panel.Settings.Value.AnswerItem.Render(language, 1, fallbackQuestion.Answers[0])

	tests := []struct {
		screen   string
		prepare  func()
		wantText string
		actions  []string
	}{
		{
			screen:   "gl",
			wantText: i18n.Messages.Panel.Settings.Screen.Groups.Render(language, 1, groupALabel+"\n"+groupBLabel),
			actions: []string{
				action(panelTestGroupA, "go", "gh"), action(panelTestGroupB, "go", "gh"),
				action(panelTestGroupA, "rf", "_"), action(panelTestGroupA, "cl", "_"),
			},
		},
		{
			screen: "gh",
			wantText: i18n.Messages.Panel.Settings.Screen.GroupHome.Render(language,
				fmt.Sprintf("Group %d", panelTestGroupA), panelTestGroupA, group.Revision(), panel.persistenceText(language),
				panel.sourcedBool(language, group.Enabled()), panel.sourcedMode(language, group.VerifyMode()), channelDisplay,
				len(questions), len(fallbackQuestions), panel.sourcedLanguage(language, group.Lang())),
			actions: []string{
				action(panelTestGroupA, "go", "rt"), action(panelTestGroupA, "go", "ls"),
				action(panelTestGroupA, "go", "md"), action(panelTestGroupA, "go", "vp"),
				action(panelTestGroupA, "go", "ct"),
				action(panelTestGroupA, "go", "gl"), action(panelTestGroupA, "rf", "_"), action(panelTestGroupA, "cl", "_"),
			},
		},
		{
			screen: "rt", wantText: expectedRuntimeScreen(panel, group, language),
			actions: []string{
				action(panelTestGroupA, "en", "_"),
				action(panelTestGroupA, "df", "g"), action(panelTestGroupA, "df", "d"), action(panelTestGroupA, "df", "b"),
				action(panelTestGroupA, "vm", "k"), action(panelTestGroupA, "vm", "q"), action(panelTestGroupA, "vm", "m"),
				action(panelTestGroupA, "ns", "_"), action(panelTestGroupA, "bd", "_"), action(panelTestGroupA, "ld", "_"),
				action(panelTestGroupA, "lt", "_"), action(panelTestGroupA, "lg", "z"), action(panelTestGroupA, "lg", "h"),
				action(panelTestGroupA, "lg", "e"), action(panelTestGroupA, "go", "gh"),
			},
		},
		{
			screen: "ls",
			wantText: i18n.Messages.Panel.Settings.Screen.Lists.Render(language, panelTestGroupA,
				len(group.ChannelWhitelist().Value), len(trusted), len(group.KnownChatIDs().Value)),
			actions: []string{
				action(panelTestGroupA, "cw", "_"), action(panelTestGroupA, "tg", "_"),
				action(panelTestGroupA, "kc", "_"), action(panelTestGroupA, "go", "gh"),
			},
		},
		{
			screen: "li", prepare: func() { session.listKind = inputTrustedGroup },
			wantText: i18n.Messages.Panel.Settings.Screen.List.Render(language, panel.listName(language, inputTrustedGroup),
				panelTestGroupA, len(trusted), trustedLine),
			actions: []string{
				action(panelTestGroupA, "tg", encodeSigned(trustedID)), action(panelTestGroupA, "ca", "tg"),
				action(panelTestGroupA, "go", "ls"),
			},
		},
		{
			screen: "vp", wantText: expectedVerificationScreen(panel, settings, group, language),
			actions: []string{
				action(panelTestGroupA, "to", "_"), action(panelTestGroupA, "mf", "_"),
				action(panelTestGroupA, "rc", "_"), action(panelTestGroupA, "vi", "_"),
				action(panelTestGroupA, "pr", "_"), action(panelTestGroupA, "go", "gh"),
			},
		},
		{
			screen: "md", wantText: expectedModerationScreen(panel, settings, group, language),
			actions: []string{
				action(panelTestGroupA, "as", "_"), action(panelTestGroupA, "ms", "_"),
				action(panelTestGroupA, "wl", "_"), action(panelTestGroupA, "rx", "_"),
				action(panelTestGroupA, "al", "_"), action(panelTestGroupA, "ac", "_"),
				action(panelTestGroupA, "go", "gh"),
			},
		},
		{
			screen: "ct",
			wantText: i18n.Messages.Panel.Settings.Screen.Content.Render(language, panelTestGroupA, len(questions),
				i18n.Messages.Panel.Settings.Value.Custom.For(language), channelDisplay, channelInvite),
			actions: []string{
				action(panelTestGroupA, "go", "qb"), action(panelTestGroupA, "go", "fb"),
				action(panelTestGroupA, "go", "ch"), action(panelTestGroupA, "go", "gh"),
			},
		},
		{
			screen:   "qb",
			wantText: i18n.Messages.Panel.Settings.Screen.QuizBank.Render(language, panelTestGroupA, len(questions), questionLine),
			actions: []string{
				action(panelTestGroupA, "qq", encodeUnsigned(0)), action(panelTestGroupA, "ca", "_"), action(panelTestGroupA, "go", "ct"),
			},
		},
		{
			screen: "qd", prepare: func() {
				session.quiz = &quizDraft{question: cloneQuestion(question), revision: session.revision, existing: true}
			},
			wantText: i18n.Messages.Panel.Settings.Screen.QuizDetail.Render(language, question.Q,
				strings.Join(optionLines, "\n"), optionLines[0]),
			actions: []string{
				action(panelTestGroupA, "qq", "_"), action(panelTestGroupA, "qo", "_"),
				action(panelTestGroupA, "ok", encodeUnsigned(0)), action(panelTestGroupA, "dl", encodeUnsigned(0)),
				action(panelTestGroupA, "ok", encodeUnsigned(1)), action(panelTestGroupA, "dl", encodeUnsigned(1)),
				action(panelTestGroupA, "sv", "_"), action(panelTestGroupA, "cn", "_"), action(panelTestGroupA, "rm", "_"),
			},
		},
		{
			screen: "fb",
			wantText: i18n.Messages.Panel.Settings.Screen.FallbackBank.Render(language, panelTestGroupA,
				i18n.Messages.Panel.Settings.Value.Custom.For(language), len(fallbackQuestions), fallbackLine),
			actions: []string{
				action(panelTestGroupA, "fq", encodeUnsigned(0)), action(panelTestGroupA, "ca", "_"),
				action(panelTestGroupA, "rb", "_"), action(panelTestGroupA, "go", "ct"),
			},
		},
		{
			screen: "fd", prepare: func() {
				session.fallback = &fallbackDraft{question: cloneShortQuestion(fallbackQuestion), revision: session.revision, existing: true}
			},
			wantText: i18n.Messages.Panel.Settings.Screen.FallbackDetail.Render(language, fallbackQuestion.Q, answerLine),
			actions: []string{
				action(panelTestGroupA, "fq", "_"), action(panelTestGroupA, "fa", "_"), action(panelTestGroupA, "dl", encodeUnsigned(0)),
				action(panelTestGroupA, "sv", "_"), action(panelTestGroupA, "cn", "_"), action(panelTestGroupA, "rm", "_"),
			},
		},
		{
			screen: "ch", wantText: expectedChannelScreen(panel, group, language),
			actions: []string{
				action(panelTestGroupA, "ci", "_"), action(panelTestGroupA, "iu", "_"), action(panelTestGroupA, "dl", "_"),
				action(panelTestGroupA, "ds", "_"), action(panelTestGroupA, "go", "ct"),
			},
		},
		{
			screen: "cf", prepare: func() { session.confirm = &confirmation{kind: "channel", revision: session.revision} },
			wantText: i18n.Messages.Panel.Settings.Screen.Confirm.Render(language,
				i18n.Messages.Panel.Settings.Field.RequiredChannel.For(language)),
			actions: []string{action(panelTestGroupA, "ok", "_"), action(panelTestGroupA, "cn", "_")},
		},
		{
			screen: "in", prepare: func() {
				session.pending = &pendingInput{kind: inputTimeout, parent: "vp", expectedRevision: session.revision}
			},
			wantText: i18n.Messages.Panel.Settings.Screen.Input.Render(language, panel.inputPrompt(language, inputTimeout)),
			actions:  []string{action(panelTestGroupA, "cn", "_")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.screen, func(t *testing.T) {
			session.screen = tt.screen
			session.page = 0
			session.listKind = inputKnownChat
			session.quiz = nil
			session.fallback = nil
			session.confirm = nil
			session.pending = nil
			if tt.prepare != nil {
				tt.prepare()
			}
			text, keyboard, err := panel.buildScreen(context.Background(), bot, session, panelTestGroupA, token)
			if err != nil {
				t.Fatalf("build screen %s: %v", tt.screen, err)
			}
			if text != tt.wantText {
				t.Fatalf("screen %s text = %q, want catalogue rendering %q", tt.screen, text, tt.wantText)
			}
			if got := panelCallbackActions(t, keyboard, token, tt.screen); !reflect.DeepEqual(got, tt.actions) {
				t.Fatalf("screen %s callbacks = %v, want %v", tt.screen, got, tt.actions)
			}
		})
	}
}

func TestPanelRuntimeControlsMutateOnlyTargetRenderAndRejectStale(t *testing.T) {
	tests := []struct {
		name           string
		field, value   string
		seed           func(*testing.T, *store.Settings)
		setExpected    func(*store.GroupOverrides, store.GroupView)
		checkEffective func(*testing.T, store.GroupView)
		language       i18n.Lang
	}{
		{
			name: "group delivery", field: "df", value: "g", language: i18n.LangEN,
			setExpected: func(next *store.GroupOverrides, _ store.GroupView) {
				value := config.DeliveryGroup
				next.DeliveryMode = &value
			},
			checkEffective: func(t *testing.T, group store.GroupView) {
				if got := group.DeliveryMode(); got.Value != config.DeliveryGroup || got.Source != store.SourceRuntime {
					t.Fatalf("delivery mode = %+v", got)
				}
			},
		},
		{
			name: "DM delivery", field: "df", value: "d", language: i18n.LangEN,
			setExpected: func(next *store.GroupOverrides, _ store.GroupView) {
				value := config.DeliveryDM
				next.DeliveryMode = &value
			},
			checkEffective: func(t *testing.T, group store.GroupView) {
				if got := group.DeliveryMode(); got.Value != config.DeliveryDM || got.Source != store.SourceRuntime {
					t.Fatalf("delivery mode = %+v", got)
				}
			},
		},
		{
			name: "both delivery", field: "df", value: "b", language: i18n.LangEN,
			setExpected: func(next *store.GroupOverrides, _ store.GroupView) {
				next.DeliveryMode = nil
			},
			checkEffective: func(t *testing.T, group store.GroupView) {
				if got := group.DeliveryMode(); got.Value != config.DeliveryBoth || got.Source != store.SourceDefault {
					t.Fatalf("delivery mode = %+v", got)
				}
			},
		},
		{
			name: "kernel mode", field: "vm", value: "k", language: i18n.LangEN,
			seed: func(t *testing.T, settings *store.Settings) {
				group := panelTestGroup(t, settings)
				next := group.Overrides()
				value := config.ModeQuiz
				next.VerifyMode = &value
				if _, err := settings.CommitGroup(group.ID(), group.Revision(), next); err != nil {
					t.Fatal(err)
				}
			},
			setExpected: func(next *store.GroupOverrides, _ store.GroupView) {
				next.VerifyMode = nil
			},
			checkEffective: func(t *testing.T, group store.GroupView) {
				if got := group.VerifyMode().Value; got != config.ModeKernel {
					t.Fatalf("verify mode = %q", got)
				}
			},
		},
		{
			name: "quiz mode", field: "vm", value: "q", language: i18n.LangEN,
			setExpected: func(next *store.GroupOverrides, _ store.GroupView) {
				value := config.ModeQuiz
				next.VerifyMode = &value
			},
			checkEffective: func(t *testing.T, group store.GroupView) {
				if got := group.VerifyMode().Value; got != config.ModeQuiz {
					t.Fatalf("verify mode = %q", got)
				}
			},
		},
		{
			name: "mixed mode", field: "vm", value: "m", language: i18n.LangEN,
			setExpected: func(next *store.GroupOverrides, _ store.GroupView) {
				value := config.ModeMixed
				next.VerifyMode = &value
			},
			checkEffective: func(t *testing.T, group store.GroupView) {
				if got := group.VerifyMode().Value; got != config.ModeMixed {
					t.Fatalf("verify mode = %q", got)
				}
			},
		},
		{
			name: "name spoiler", field: "ns", value: "_", language: i18n.LangEN,
			setExpected: func(next *store.GroupOverrides, group store.GroupView) {
				value := !group.NameSpoiler().Value
				next.NameSpoiler = &value
			},
			checkEffective: func(t *testing.T, group store.GroupView) {
				if group.NameSpoiler().Source != store.SourceRuntime {
					t.Fatalf("name spoiler = %+v", group.NameSpoiler())
				}
			},
		},
		{
			name: "lookup auto-delete", field: "ld", value: "_", language: i18n.LangEN,
			setExpected: func(next *store.GroupOverrides, group store.GroupView) {
				value := !group.LookupAutoDeleteEnabled().Value
				next.LookupAutoDeleteEnabled = &value
			},
			checkEffective: func(t *testing.T, group store.GroupView) {
				if group.LookupAutoDeleteEnabled().Source != store.SourceRuntime {
					t.Fatalf("lookup auto-delete = %+v", group.LookupAutoDeleteEnabled())
				}
			},
		},
		{
			name: "Simplified Chinese language", field: "lg", value: "z", language: i18n.LangZH,
			seed: func(t *testing.T, settings *store.Settings) {
				group := panelTestGroup(t, settings)
				next := group.Overrides()
				value := "en"
				next.Lang = &value
				if _, err := settings.CommitGroup(group.ID(), group.Revision(), next); err != nil {
					t.Fatal(err)
				}
			},
			setExpected: func(next *store.GroupOverrides, _ store.GroupView) { next.Lang = nil },
			checkEffective: func(t *testing.T, group store.GroupView) {
				if got := group.Lang().Value; got != "zh" {
					t.Fatalf("language = %q", got)
				}
			},
		},
		{
			name: "Traditional Chinese language", field: "lg", value: "h", language: i18n.LangZHHant,
			setExpected: func(next *store.GroupOverrides, _ store.GroupView) { value := "zh-Hant"; next.Lang = &value },
			checkEffective: func(t *testing.T, group store.GroupView) {
				if got := group.Lang().Value; got != "zh-Hant" {
					t.Fatalf("language = %q", got)
				}
			},
		},
		{
			name: "English language", field: "lg", value: "e", language: i18n.LangEN,
			setExpected: func(next *store.GroupOverrides, _ store.GroupView) { value := "en"; next.Lang = &value },
			checkEffective: func(t *testing.T, group store.GroupView) {
				if got := group.Lang().Value; got != "en" {
					t.Fatalf("language = %q", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panel, settings, caller, bot := newSettingsPanelTest(t, "")
			if tt.seed != nil {
				tt.seed(t, settings)
			}
			group := panelTestGroup(t, settings)
			want := group.Overrides()
			tt.setExpected(&want, group)
			session := addPanelSession(t, panel, settings, panelTestGroupA, "rt")
			invokePanelCallback(t, panel, bot, session, panelTestGroupA, tt.field, tt.value)
			group = assertGroupOverrides(t, settings, want)
			tt.checkEffective(t, group)
			if got, wantScreen := caller.lastEditText, expectedRuntimeScreen(panel, group, tt.language); got != wantScreen {
				t.Fatalf("rendered runtime screen = %q, want catalogue rendering %q", got, wantScreen)
			}
		})

		t.Run(tt.name+" stale", func(t *testing.T) {
			panel, settings, caller, bot := newSettingsPanelTest(t, "")
			session := addPanelSession(t, panel, settings, panelTestGroupA, "rt")
			want := bumpPanelGroupRevision(t, settings)
			invokePanelCallback(t, panel, bot, session, panelTestGroupA, tt.field, tt.value)
			assertGroupOverrides(t, settings, want)
			conflict := i18n.Messages.Panel.Settings.Error.ConcurrentChange.For(i18n.LangEN)
			if caller.lastEditText != conflict {
				t.Fatalf("stale control rendered %q, want catalogue conflict %q", caller.lastEditText, conflict)
			}
		})
	}
}

func TestPanelNumericControlsMutateOnlyTargetRenderAndRejectStale(t *testing.T) {
	tests := []struct {
		name         string
		screen       string
		field, input string
		setGroup     func(*store.GroupOverrides)
		setGlobal    func(*store.GlobalOverrides)
		check        func(*testing.T, *store.Settings)
	}{
		{
			name: "ban duration", screen: "rt", field: "bd", input: "2h",
			setGroup: func(next *store.GroupOverrides) { value := 7200; next.BanSeconds = &value },
			check: func(t *testing.T, settings *store.Settings) {
				if got := panelTestGroup(t, settings).BanSeconds().Value; got != 7200 {
					t.Fatalf("ban seconds = %d", got)
				}
			},
		},
		{
			name: "lookup TTL", screen: "rt", field: "lt", input: "7",
			setGroup: func(next *store.GroupOverrides) { value := 420; next.LookupTTLSeconds = &value },
			check: func(t *testing.T, settings *store.Settings) {
				if got := panelTestGroup(t, settings).LookupTTLSeconds().Value; got != 420 {
					t.Fatalf("lookup TTL = %d", got)
				}
			},
		},
		{
			name: "mute duration", screen: "md", field: "ms", input: "2h",
			setGroup: func(next *store.GroupOverrides) { value := 7200; next.MuteSeconds = &value },
			check: func(t *testing.T, settings *store.Settings) {
				if got := panelTestGroup(t, settings).MuteSeconds().Value; got != 7200 {
					t.Fatalf("mute seconds = %d", got)
				}
			},
		},
		{
			name: "warning limit", screen: "md", field: "wl", input: "5",
			setGroup: func(next *store.GroupOverrides) { value := 5; next.WarnLimit = &value },
			check: func(t *testing.T, settings *store.Settings) {
				if got := panelTestGroup(t, settings).WarnLimit().Value; got != 5 {
					t.Fatalf("warn limit = %d", got)
				}
			},
		},
		{
			name: "verification timeout", screen: "vp", field: "to", input: "300",
			setGroup: func(next *store.GroupOverrides) { value := 300; next.TimeoutSeconds = &value },
			check: func(t *testing.T, settings *store.Settings) {
				if got := panelTestGroup(t, settings).TimeoutSeconds().Value; got != 300 {
					t.Fatalf("timeout = %d", got)
				}
			},
		},
		{
			name: "verification max fails", screen: "vp", field: "mf", input: "5",
			setGroup: func(next *store.GroupOverrides) { value := 5; next.VerifyMaxFails = &value },
			check: func(t *testing.T, settings *store.Settings) {
				if got := panelTestGroup(t, settings).VerifyMaxFails().Value; got != 5 {
					t.Fatalf("max fails = %d", got)
				}
			},
		},
		{
			name: "verification retry cooldown", screen: "vp", field: "rc", input: "90",
			setGroup: func(next *store.GroupOverrides) { value := 90; next.VerifyRetrySeconds = &value },
			check: func(t *testing.T, settings *store.Settings) {
				if got := panelTestGroup(t, settings).VerifyRetrySeconds().Value; got != 90 {
					t.Fatalf("retry seconds = %d", got)
				}
			},
		},
		{
			name: "private query rate", screen: "vp", field: "pr", input: "9",
			setGlobal: func(next *store.GlobalOverrides) { value := 9; next.PrivateQueryPerMin = &value },
			check: func(t *testing.T, settings *store.Settings) {
				if got := settings.Global().PrivateQueryPerMin().Value; got != 9 {
					t.Fatalf("private rate = %d", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panel, settings, caller, bot := newSettingsPanelTest(t, "")
			groupWant := panelTestGroup(t, settings).Overrides()
			globalWant := settings.Global().Overrides()
			if tt.setGroup != nil {
				tt.setGroup(&groupWant)
			}
			if tt.setGlobal != nil {
				tt.setGlobal(&globalWant)
			}
			session := addPanelSession(t, panel, settings, panelTestGroupA, tt.screen)
			invokePanelCallback(t, panel, bot, session, panelTestGroupA, tt.field, "_")
			submitPanelText(t, panel, bot, session, tt.input)
			group := assertGroupOverrides(t, settings, groupWant)
			assertGlobalOverrides(t, settings, globalWant)
			tt.check(t, settings)
			wantScreen := expectedRuntimeScreen(panel, group, i18n.LangEN)
			switch tt.screen {
			case "vp":
				wantScreen = expectedVerificationScreen(panel, settings, group, i18n.LangEN)
			case "md":
				wantScreen = expectedModerationScreen(panel, settings, group, i18n.LangEN)
			}
			if caller.lastEditText != wantScreen {
				t.Fatalf("rendered %s screen = %q, want catalogue rendering %q", tt.screen, caller.lastEditText, wantScreen)
			}
		})

		t.Run(tt.name+" stale", func(t *testing.T) {
			panel, settings, caller, bot := newSettingsPanelTest(t, "")
			session := addPanelSession(t, panel, settings, panelTestGroupA, tt.screen)
			invokePanelCallback(t, panel, bot, session, panelTestGroupA, tt.field, "_")
			var groupWant store.GroupOverrides
			globalWant := settings.Global().Overrides()
			if tt.setGlobal != nil {
				value := !settings.Global().RichMessages().Value
				globalWant.RichMessages = &value
				global := settings.Global()
				if _, err := settings.CommitGlobal(global.Revision(), globalWant); err != nil {
					t.Fatal(err)
				}
				groupWant = panelTestGroup(t, settings).Overrides()
			} else {
				groupWant = bumpPanelGroupRevision(t, settings)
			}
			submitPanelText(t, panel, bot, session, tt.input)
			assertGroupOverrides(t, settings, groupWant)
			assertGlobalOverrides(t, settings, globalWant)
			conflict := i18n.Messages.Panel.Settings.Error.ConcurrentChange.For(i18n.LangEN)
			if caller.lastEditText != conflict {
				t.Fatalf("stale numeric control rendered %q, want catalogue conflict %q", caller.lastEditText, conflict)
			}
		})
	}
}

func seedPanelChannel(t *testing.T, settings *store.Settings, channelID int64, display, invite string) store.GroupOverrides {
	t.Helper()
	group := panelTestGroup(t, settings)
	next := group.Overrides()
	next.RequiredChannelID = &channelID
	next.ChannelDisplay = &display
	next.ChannelInviteURL = &invite
	if _, err := settings.CommitGroup(group.ID(), group.Revision(), next); err != nil {
		t.Fatal(err)
	}
	return panelTestGroup(t, settings).Overrides()
}
func seedPanelBanks(t *testing.T, settings *store.Settings) store.GroupOverrides {
	t.Helper()
	group := panelTestGroup(t, settings)
	next := group.Overrides()
	questions := []config.Question{{Q: "Question", Options: []string{"A", "B"}, Answer: 0}}
	fallbackQuestions := []config.ShortQuestion{{Q: "Fallback question", Answers: []string{"Answer"}}}
	custom := false
	next.Questions = &questions
	next.FallbackQuestions = &fallbackQuestions
	next.FallbackBuiltin = &custom
	if _, err := settings.CommitGroup(group.ID(), group.Revision(), next); err != nil {
		t.Fatal(err)
	}
	return panelTestGroup(t, settings).Overrides()
}

func TestPanelTrustedGroupAndChannelControlsMutateOnlyTargetRenderAndRejectStale(t *testing.T) {
	const (
		trustedID = int64(-1009000000811)
		channelID = int64(-1009000000812)
	)

	t.Run("trusted group add", func(t *testing.T) {
		panel, settings, caller, bot := newSettingsPanelTest(t, "")
		session := addPanelSession(t, panel, settings, panelTestGroupA, "ls")
		invokePanelCallback(t, panel, bot, session, panelTestGroupA, "tg", "_")
		invokePanelCallback(t, panel, bot, session, panelTestGroupA, "ca", "tg")
		submitSharedChat(t, panel, bot, session, trustedID)
		want := store.GroupOverrides{}
		values := []int64{trustedID}
		want.TrustedMemberGroupIDs = &values
		group := assertGroupOverrides(t, settings, want)
		if caller.lastEditText != expectedListScreen(panel, group, i18n.LangEN, inputTrustedGroup) {
			t.Fatalf("trusted-group list did not render catalogue value: %q", caller.lastEditText)
		}
	})

	t.Run("trusted group add stale", func(t *testing.T) {
		panel, settings, caller, bot := newSettingsPanelTest(t, "")
		session := addPanelSession(t, panel, settings, panelTestGroupA, "ls")
		invokePanelCallback(t, panel, bot, session, panelTestGroupA, "tg", "_")
		invokePanelCallback(t, panel, bot, session, panelTestGroupA, "ca", "tg")
		want := bumpPanelGroupRevision(t, settings)
		submitSharedChat(t, panel, bot, session, trustedID)
		assertGroupOverrides(t, settings, want)
		if caller.lastEditText != i18n.Messages.Panel.Settings.Error.ConcurrentChange.For(i18n.LangEN) {
			t.Fatalf("stale trusted-group add rendered %q", caller.lastEditText)
		}
	})

	t.Run("trusted group remove and stale refusal", func(t *testing.T) {
		newSeeded := func(t *testing.T) (*Panel, *store.Settings, *panelAPICaller, *telego.Bot, *panelSession, store.GroupOverrides) {
			t.Helper()
			panel, settings, caller, bot := newSettingsPanelTest(t, "")
			group := panelTestGroup(t, settings)
			seeded := group.Overrides()
			values := []int64{trustedID}
			seeded.TrustedMemberGroupIDs = &values
			if _, err := settings.CommitGroup(group.ID(), group.Revision(), seeded); err != nil {
				t.Fatal(err)
			}
			session := addPanelSession(t, panel, settings, panelTestGroupA, "li")
			session.listKind = inputTrustedGroup
			return panel, settings, caller, bot, session, seeded
		}

		panel, settings, caller, bot, session, seeded := newSeeded(t)
		want := seeded
		want.TrustedMemberGroupIDs = nil
		invokePanelCallback(t, panel, bot, session, panelTestGroupA, "tg", encodeSigned(trustedID))
		group := assertGroupOverrides(t, settings, want)
		if caller.lastEditText != expectedListScreen(panel, group, i18n.LangEN, inputTrustedGroup) {
			t.Fatalf("trusted-group removal did not render empty catalogue list: %q", caller.lastEditText)
		}

		panel, settings, caller, bot, session, _ = newSeeded(t)
		staleWant := bumpPanelGroupRevision(t, settings)
		invokePanelCallback(t, panel, bot, session, panelTestGroupA, "tg", encodeSigned(trustedID))
		assertGroupOverrides(t, settings, staleWant)
		if caller.lastEditText != i18n.Messages.Panel.Settings.Error.ConcurrentChange.For(i18n.LangEN) {
			t.Fatalf("stale trusted-group removal rendered %q", caller.lastEditText)
		}
	})

	t.Run("public channel selection", func(t *testing.T) {
		panel, settings, caller, bot := newSettingsPanelTest(t, "")
		caller.chatUsername = "public_channel"
		session := addPanelSession(t, panel, settings, panelTestGroupA, "ch")
		invokePanelCallback(t, panel, bot, session, panelTestGroupA, "ci", "_")
		submitSharedChat(t, panel, bot, session, channelID)
		display := "@" + caller.chatUsername
		want := store.GroupOverrides{RequiredChannelID: func() *int64 { value := channelID; return &value }(), ChannelDisplay: &display}
		group := assertGroupOverrides(t, settings, want)
		if caller.lastEditText != expectedChannelScreen(panel, group, i18n.LangEN) {
			t.Fatalf("public-channel screen did not render catalogue values: %q", caller.lastEditText)
		}
	})

	t.Run("public channel selection stale", func(t *testing.T) {
		panel, settings, caller, bot := newSettingsPanelTest(t, "")
		caller.chatUsername = "public_channel"
		session := addPanelSession(t, panel, settings, panelTestGroupA, "ch")
		invokePanelCallback(t, panel, bot, session, panelTestGroupA, "ci", "_")
		want := bumpPanelGroupRevision(t, settings)
		submitSharedChat(t, panel, bot, session, channelID)
		assertGroupOverrides(t, settings, want)
		if caller.lastEditText != i18n.Messages.Panel.Settings.Error.ConcurrentChange.For(i18n.LangEN) {
			t.Fatalf("stale public-channel selection rendered %q", caller.lastEditText)
		}
	})

	t.Run("private channel selection and invite", func(t *testing.T) {
		panel, settings, caller, bot := newSettingsPanelTest(t, "")
		session := addPanelSession(t, panel, settings, panelTestGroupA, "ch")
		invokePanelCallback(t, panel, bot, session, panelTestGroupA, "ci", "_")
		submitSharedChat(t, panel, bot, session, channelID)
		if got := panelTestGroup(t, settings).Overrides(); !reflect.DeepEqual(got, store.GroupOverrides{}) {
			t.Fatalf("private channel committed before invite input: %+v", got)
		}
		const invite = "https://t.me/+privateinvite"
		submitPanelText(t, panel, bot, session, invite)
		display := fmt.Sprintf("Group %d", channelID)
		want := store.GroupOverrides{
			RequiredChannelID: func() *int64 { value := channelID; return &value }(),
			ChannelDisplay:    &display,
			ChannelInviteURL:  func() *string { value := invite; return &value }(),
		}
		group := assertGroupOverrides(t, settings, want)
		if caller.lastEditText != expectedChannelScreen(panel, group, i18n.LangEN) {
			t.Fatalf("private-channel screen did not render catalogue values: %q", caller.lastEditText)
		}
	})

	t.Run("private channel invite stale", func(t *testing.T) {
		panel, settings, caller, bot := newSettingsPanelTest(t, "")
		session := addPanelSession(t, panel, settings, panelTestGroupA, "ch")
		invokePanelCallback(t, panel, bot, session, panelTestGroupA, "ci", "_")
		submitSharedChat(t, panel, bot, session, channelID)
		want := bumpPanelGroupRevision(t, settings)
		submitPanelText(t, panel, bot, session, "https://t.me/+privateinvite")
		assertGroupOverrides(t, settings, want)
		if caller.lastEditText != i18n.Messages.Panel.Settings.Error.ConcurrentChange.For(i18n.LangEN) {
			t.Fatalf("stale private-channel invite rendered %q", caller.lastEditText)
		}
	})

	t.Run("public invite set clear destructive confirm and cancel", func(t *testing.T) {
		const (
			display = "@public_channel"
			invite  = "https://t.me/public_channel"
		)
		panel, settings, caller, bot := newSettingsPanelTest(t, "")
		seeded := seedPanelChannel(t, settings, channelID, display, "")
		session := addPanelSession(t, panel, settings, panelTestGroupA, "ch")

		invokePanelCallback(t, panel, bot, session, panelTestGroupA, "iu", "_")
		submitPanelText(t, panel, bot, session, invite)
		withInvite := seeded
		withInvite.ChannelInviteURL = func() *string { value := invite; return &value }()
		group := assertGroupOverrides(t, settings, withInvite)
		if caller.lastEditText != expectedChannelScreen(panel, group, i18n.LangEN) {
			t.Fatalf("public invite screen = %q", caller.lastEditText)
		}

		invokePanelCallback(t, panel, bot, session, panelTestGroupA, "dl", "_")
		clearedInvite := withInvite
		clearedInvite.ChannelInviteURL = nil
		group = assertGroupOverrides(t, settings, clearedInvite)
		if caller.lastEditText != expectedChannelScreen(panel, group, i18n.LangEN) {
			t.Fatalf("cleared public invite screen = %q", caller.lastEditText)
		}

		invokePanelCallback(t, panel, bot, session, panelTestGroupA, "ds", "_")
		assertGroupOverrides(t, settings, clearedInvite)
		wantConfirm := i18n.Messages.Panel.Settings.Screen.Confirm.Render(i18n.LangEN,
			i18n.Messages.Panel.Settings.Field.RequiredChannel.For(i18n.LangEN))
		if caller.lastEditText != wantConfirm {
			t.Fatalf("channel confirmation = %q, want catalogue screen %q", caller.lastEditText, wantConfirm)
		}
		invokePanelCallback(t, panel, bot, session, panelTestGroupA, "cn", "_")
		group = assertGroupOverrides(t, settings, clearedInvite)
		if caller.lastEditText != expectedChannelScreen(panel, group, i18n.LangEN) {
			t.Fatalf("channel cancel screen = %q", caller.lastEditText)
		}

		invokePanelCallback(t, panel, bot, session, panelTestGroupA, "ds", "_")
		invokePanelCallback(t, panel, bot, session, panelTestGroupA, "ok", "_")
		disabled := clearedInvite
		disabled.RequiredChannelID = nil
		disabled.ChannelDisplay = nil
		disabled.ChannelInviteURL = nil
		group = assertGroupOverrides(t, settings, disabled)
		if caller.lastEditText != expectedChannelScreen(panel, group, i18n.LangEN) {
			t.Fatalf("disabled channel screen = %q", caller.lastEditText)
		}
	})

	t.Run("public invite and destructive actions reject stale revisions", func(t *testing.T) {
		const (
			display = "@public_channel"
			invite  = "https://t.me/public_channel"
		)
		panel, settings, caller, bot := newSettingsPanelTest(t, "")
		seedPanelChannel(t, settings, channelID, display, "")
		session := addPanelSession(t, panel, settings, panelTestGroupA, "ch")
		invokePanelCallback(t, panel, bot, session, panelTestGroupA, "iu", "_")
		want := bumpPanelGroupRevision(t, settings)
		submitPanelText(t, panel, bot, session, invite)
		assertGroupOverrides(t, settings, want)
		if caller.lastEditText != i18n.Messages.Panel.Settings.Error.ConcurrentChange.For(i18n.LangEN) {
			t.Fatalf("stale public invite rendered %q", caller.lastEditText)
		}

		panel, settings, caller, bot = newSettingsPanelTest(t, "")
		seedPanelChannel(t, settings, channelID, display, invite)
		session = addPanelSession(t, panel, settings, panelTestGroupA, "ch")
		want = bumpPanelGroupRevision(t, settings)
		invokePanelCallback(t, panel, bot, session, panelTestGroupA, "dl", "_")
		assertGroupOverrides(t, settings, want)
		if caller.lastEditText != i18n.Messages.Panel.Settings.Error.ConcurrentChange.For(i18n.LangEN) {
			t.Fatalf("stale public invite clear rendered %q", caller.lastEditText)
		}

		panel, settings, caller, bot = newSettingsPanelTest(t, "")
		seedPanelChannel(t, settings, channelID, display, invite)
		session = addPanelSession(t, panel, settings, panelTestGroupA, "ch")
		invokePanelCallback(t, panel, bot, session, panelTestGroupA, "ds", "_")
		want = bumpPanelGroupRevision(t, settings)
		invokePanelCallback(t, panel, bot, session, panelTestGroupA, "ok", "_")
		assertGroupOverrides(t, settings, want)
		if caller.lastEditText != i18n.Messages.Panel.Settings.Error.ConcurrentChange.For(i18n.LangEN) {
			t.Fatalf("stale destructive action rendered %q", caller.lastEditText)
		}
	})

	t.Run("draft and input cancel controls preserve settings", func(t *testing.T) {
		for _, kind := range []string{"quiz", "fallback"} {
			t.Run(kind, func(t *testing.T) {
				panel, settings, caller, bot := newSettingsPanelTest(t, "")
				want := seedPanelBanks(t, settings)
				screen, field := "qb", "qq"
				if kind == "fallback" {
					screen, field = "fb", "fq"
				}
				session := addPanelSession(t, panel, settings, panelTestGroupA, screen)
				invokePanelCallback(t, panel, bot, session, panelTestGroupA, field, encodeUnsigned(0))
				invokePanelCallback(t, panel, bot, session, panelTestGroupA, "cn", "_")
				group := assertGroupOverrides(t, settings, want)
				wantScreen := expectedQuizBankScreen(group, i18n.LangEN)
				if kind == "fallback" {
					wantScreen = expectedFallbackBankScreen(group, i18n.LangEN)
				}
				if caller.lastEditText != wantScreen {
					t.Fatalf("%s cancel screen = %q, want catalogue rendering %q", kind, caller.lastEditText, wantScreen)
				}
			})
		}

		panel, settings, caller, bot := newSettingsPanelTest(t, "")
		session := addPanelSession(t, panel, settings, panelTestGroupA, "rt")
		invokePanelCallback(t, panel, bot, session, panelTestGroupA, "bd", "_")
		invokePanelCallback(t, panel, bot, session, panelTestGroupA, "cn", "_")
		group := assertGroupOverrides(t, settings, store.GroupOverrides{})
		if session.pending != nil || caller.lastEditText != expectedRuntimeScreen(panel, group, i18n.LangEN) {
			t.Fatalf("input cancel = pending %+v screen %q", session.pending, caller.lastEditText)
		}
	})

	t.Run("question-bank destructive controls commit only target and reject stale", func(t *testing.T) {
		type destructiveCase struct {
			name           string
			prepare        func(*testing.T, *Panel, *telego.Bot, *panelSession)
			updateExpected func(*store.GroupOverrides)
			wantScreen     func(store.GroupView) string
		}
		tests := []destructiveCase{
			{
				name: "quiz delete",
				prepare: func(t *testing.T, panel *Panel, bot *telego.Bot, session *panelSession) {
					invokePanelCallback(t, panel, bot, session, panelTestGroupA, "qq", encodeUnsigned(0))
					invokePanelCallback(t, panel, bot, session, panelTestGroupA, "rm", "_")
				},
				updateExpected: func(next *store.GroupOverrides) { next.Questions = nil },
				wantScreen: func(group store.GroupView) string {
					return expectedQuizBankScreen(group, i18n.LangEN)
				},
			},
			{
				name: "fallback delete",
				prepare: func(t *testing.T, panel *Panel, bot *telego.Bot, session *panelSession) {
					invokePanelCallback(t, panel, bot, session, panelTestGroupA, "fq", encodeUnsigned(0))
					invokePanelCallback(t, panel, bot, session, panelTestGroupA, "rm", "_")
				},
				updateExpected: func(next *store.GroupOverrides) {
					next.FallbackQuestions = nil
					next.FallbackBuiltin = nil
				},
				wantScreen: func(group store.GroupView) string {
					return expectedFallbackBankScreen(group, i18n.LangEN)
				},
			},
			{
				name: "fallback reset",
				prepare: func(t *testing.T, panel *Panel, bot *telego.Bot, session *panelSession) {
					invokePanelCallback(t, panel, bot, session, panelTestGroupA, "rb", "_")
				},
				updateExpected: func(next *store.GroupOverrides) {
					next.FallbackQuestions = nil
					next.FallbackBuiltin = nil
				},
				wantScreen: func(group store.GroupView) string {
					return expectedFallbackBankScreen(group, i18n.LangEN)
				},
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				panel, settings, caller, bot := newSettingsPanelTest(t, "")
				want := seedPanelBanks(t, settings)
				screen := "fb"
				if tt.name == "quiz delete" {
					screen = "qb"
				}
				session := addPanelSession(t, panel, settings, panelTestGroupA, screen)
				tt.prepare(t, panel, bot, session)
				tt.updateExpected(&want)
				invokePanelCallback(t, panel, bot, session, panelTestGroupA, "ok", "_")
				group := assertGroupOverrides(t, settings, want)
				if wantScreen := tt.wantScreen(group); caller.lastEditText != wantScreen {
					t.Fatalf("destructive screen = %q, want catalogue rendering %q", caller.lastEditText, wantScreen)
				}
			})

			t.Run(tt.name+" stale", func(t *testing.T) {
				panel, settings, caller, bot := newSettingsPanelTest(t, "")
				seedPanelBanks(t, settings)
				screen := "fb"
				if tt.name == "quiz delete" {
					screen = "qb"
				}
				session := addPanelSession(t, panel, settings, panelTestGroupA, screen)
				tt.prepare(t, panel, bot, session)
				want := bumpPanelGroupRevision(t, settings)
				invokePanelCallback(t, panel, bot, session, panelTestGroupA, "ok", "_")
				assertGroupOverrides(t, settings, want)
				if caller.lastEditText != i18n.Messages.Panel.Settings.Error.ConcurrentChange.For(i18n.LangEN) {
					t.Fatalf("stale destructive control rendered %q", caller.lastEditText)
				}
			})
		}
	})
}
