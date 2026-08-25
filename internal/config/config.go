// Package config loads and resolves the bot's JSON configuration.
package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"
)

const (
	// ModeQuiz presents shuffled inline-button questions.
	ModeQuiz = "quiz"
	// ModeKernel requires applicants to type a kernel version.
	ModeKernel = "kernel"
	// ModeMixed chooses quiz or kernel verification per applicant.
	ModeMixed = "mixed"
)

const defaultVerifyMode = ModeKernel

// ValidMode reports whether mode names a supported verification mode.
func ValidMode(mode string) bool {
	switch mode {
	case ModeQuiz, ModeKernel, ModeMixed:
		return true
	}
	return false
}

// ValidLanguage reports whether lang is empty or a supported canonical language tag.
func ValidLanguage(lang string) bool {
	switch lang {
	case "", "zh", "zh-Hant", "en":
		return true
	default:
		return false
	}
}

// Telegram treats until_date below 30 seconds or above 366 days as permanent.
const telegramBanMax = 366 * 86400

// ClampBanSeconds maps a ban duration into Telegram's enforced range.
func ClampBanSeconds(seconds int) int {
	switch {
	case seconds <= 0:
		return 0
	case seconds < 30:
		return 30
	case seconds > telegramBanMax:
		return 0
	default:
		return seconds
	}
}

// clampMuteSecs maps a finite mute duration into Telegram's enforced range.
func clampMuteSecs(secs int) int {
	switch {
	case secs < 30:
		return 30
	case secs > telegramBanMax:
		return telegramBanMax
	default:
		return secs
	}
}

// defaultPrivateReply handles plain DMs not routed to a command.
const defaultPrivateReply = "👋 这是 Gentoo 中文社区的入群验证 + Gentoo/Linux 助手机器人。\n\n" +
	"• 想入群:回到群里发起加入申请,再点群消息中的「✅ 点此完成验证」链接来这里完成验证。\n" +
	"• 查询命令(/pkg /use /bug /news /wiki /bbs /pkgs /arm /armpkgs)私聊也能直接用(每分钟有限次,防滥用;群里不限次)。\n" +
	"• 审核/管理命令仅在群里有效。"

// Question is one verification quiz item with a zero-based answer index.
type Question struct {
	// Q is the question prompt.
	Q string `json:"q"`
	// Options lists the possible answers.
	Options []string `json:"options"`
	// Answer is the zero-based index of the correct option.
	Answer int `json:"answer"`
}

// ShortQuestion is an answer-hidden verification question.
type ShortQuestion struct {
	// Q is the question prompt.
	Q string `json:"q"`
	// Answers lists normalized whole replies accepted as correct.
	Answers []string `json:"answers"`
}

// OverlayCfg identifies a GitHub overlay searched by /pkg.
type OverlayCfg struct {
	// Name is the overlay's display and cache name.
	Name string `json:"name"`
	// Repo is the GitHub repository in owner/name form.
	Repo string `json:"repo"`
	// Branch is the repository branch and defaults to master when empty.
	Branch string `json:"branch"`
}

// GroupConfig overrides top-level defaults for one guarded group.
type GroupConfig struct {
	// ID is the guarded Telegram group ID.
	ID int64 `json:"id"`
	// RequiredChannelID overrides the global channel requirement when non-nil.
	RequiredChannelID *int64 `json:"required_channel_id"`
	// ChannelDisplay overrides the global channel display name when non-empty.
	ChannelDisplay string `json:"channel_display"`
	// ChannelInviteURL overrides the global private-channel invite when non-empty.
	ChannelInviteURL string `json:"channel_invite_url"`
	// Questions overrides the global quiz pool when non-empty.
	Questions []Question `json:"questions"`
	// VerifyMode is kernel, quiz, mixed, or empty to inherit.
	VerifyMode string `json:"verify_mode"`
	// TrustedMemberGroupIDs overrides, disables, or inherits the global bypass list.
	TrustedMemberGroupIDs []int64 `json:"trusted_member_group_ids"`
	// Lang overrides the global language when non-empty.
	Lang string `json:"lang"`
}

// FeedConfig configures one optional Bugzilla and news destination.
type FeedConfig struct {
	// ChatID is the channel or group receiving feed posts.
	ChatID int64 `json:"chat_id"`
	// Lang selects zh, zh-Hant, or en and defaults to zh.
	Lang string `json:"lang"`
	// IntervalSeconds is the polling interval with a 300-second default and 60-second minimum.
	IntervalSeconds int `json:"interval_seconds"`
	// Bugs enables Bugzilla posts and defaults to true.
	Bugs *bool `json:"bugs"`
	// News enables news posts and defaults to true.
	News *bool `json:"news"`
	// BugProduct filters bugs by product when non-empty.
	BugProduct string `json:"bug_product"`
	// BugComponent filters bugs by component when non-empty.
	BugComponent string `json:"bug_component"`
	// SilentBugs makes every bug post silent when true.
	SilentBugs *bool `json:"silent_bugs"`
}

// BugsOn reports whether this feed posts Bugzilla bugs.
func (f *FeedConfig) BugsOn() bool { return f.Bugs == nil || *f.Bugs }

// NewsOn reports whether this feed posts news items.
func (f *FeedConfig) NewsOn() bool { return f.News == nil || *f.News }

// Interval returns this feed's clamped polling interval.
func (f *FeedConfig) Interval() time.Duration {
	switch {
	case f.IntervalSeconds <= 0:
		return 5 * time.Minute // unset -> default
	case f.IntervalSeconds < 60:
		return 60 * time.Second // clamp a too-fast interval to the 60 s floor
	default:
		return time.Duration(f.IntervalSeconds) * time.Second
	}
}

// Config contains the validated JSON configuration.
type Config struct {
	// Groups is the canonical guarded-group list after legacy IDs are merged.
	Groups []GroupConfig `json:"groups"`
	// GroupIDs mirrors Groups and accepts the legacy group_ids key.
	GroupIDs []int64 `json:"group_ids"`
	// GroupID accepts the legacy singular group_id key.
	GroupID int64 `json:"group_id"`
	// ControlGroupID limits global commands and zero allows any guarded group.
	ControlGroupID int64 `json:"control_group_id"`
	// Lang is the default language for group-facing output and defaults to zh.
	Lang string `json:"lang"`
	// RequiredChannelID gates approval on channel membership and zero disables it.
	RequiredChannelID int64 `json:"required_channel_id"`
	// ChannelDisplay names the required channel for messages and public links.
	ChannelDisplay string `json:"channel_display"`
	// TrustedMemberGroupIDs is the global verification-bypass source list.
	TrustedMemberGroupIDs []int64 `json:"trusted_member_group_ids"`
	// KnownChatIDs prevents auto-leave without granting verification or bypass semantics.
	KnownChatIDs []int64 `json:"known_chat_ids"`
	// ChannelInviteURL links a private required channel without a public handle.
	ChannelInviteURL string `json:"channel_invite_url"`
	// TimeoutSeconds is the verification deadline.
	TimeoutSeconds int `json:"timeout_seconds"`
	// AdminLogChatID receives moderation and failed-action notices.
	AdminLogChatID int64 `json:"admin_log_chat_id"`
	// NotifyTTLSeconds controls notice deletion, defaults to 60, and is disabled when negative.
	NotifyTTLSeconds int `json:"notify_ttl_seconds"`
	// LookupTTLSeconds controls lookup deletion, defaults to 180, and is disabled when non-positive.
	LookupTTLSeconds *int `json:"lookup_ttl_seconds"`
	// WarnLimit is the strike count before an automatic kick and defaults to three.
	WarnLimit int `json:"warn_limit"`
	// PrivateQueryPerMin is the per-user DM query limit and defaults to three.
	PrivateQueryPerMin int `json:"private_query_per_min"`
	// RequiredChannelFailOpen controls admission when required-channel membership is unreadable.
	RequiredChannelFailOpen *bool `json:"required_channel_fail_open"`
	// BanSeconds is the default ban duration and zero means permanent.
	BanSeconds int `json:"ban_seconds"`
	// MuteSeconds is the finite default mute duration.
	MuteSeconds int `json:"mute_seconds"`
	// VerifyRetrySeconds is the cooldown and a negative value disables it.
	VerifyRetrySeconds int `json:"verify_retry_seconds"`
	// VerifyMaxFails is the automatic-ban threshold and a negative value disables it.
	VerifyMaxFails int `json:"verify_max_fails"`
	// VerifyMode selects kernel, quiz, or mixed verification.
	VerifyMode string `json:"verify_mode"`
	// FallbackQuestions is the answer-hidden path for applicants without Linux.
	FallbackQuestions []ShortQuestion `json:"fallback_questions"`
	// Overlays lists GitHub overlays searched by /pkg.
	Overlays []OverlayCfg `json:"overlays"`
	// NewsURL is the Gentoo news-items index used by /news.
	NewsURL string `json:"news_url"`
	// StatsTimezone is the IANA time zone for the daily /stats boundary.
	StatsTimezone string `json:"stats_timezone"`
	// RichMessages enables rich Bot API messages with an HTML fallback.
	RichMessages bool `json:"rich_messages"`
	// UserAgent overrides the outbound HTTP User-Agent when non-empty.
	UserAgent string `json:"user_agent"`
	// PrivateReply handles non-command DMs outside verification.
	PrivateReply string `json:"private_reply"`
	// BlockChannelSenders rejects sender-chat posts when privacy mode is disabled.
	BlockChannelSenders bool `json:"block_channel_senders"`
	// ChannelWhitelist lists sender chats allowed to post in guarded groups.
	ChannelWhitelist []int64 `json:"channel_whitelist"`
	// Feeds lists Bugzilla and news destinations.
	Feeds []FeedConfig `json:"feeds"`
	// Feed accepts the legacy singular feed form and is merged into Feeds.
	Feed *FeedConfig `json:"feed"`
	// Questions is the global verification quiz pool.
	Questions []Question `json:"questions"`
}

func warnUnknownJSONKeys(raw json.RawMessage, typ reflect.Type, where string) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return
	}
	known := make(map[string]struct{}, typ.NumField())
	for i := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			known[name] = struct{}{}
		}
	}
	var unknown []string
	for name := range object {
		if _, ok := known[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	for _, name := range unknown {
		log.Printf("WARNING: %s: unknown key %q", where, name)
	}
}

func warnUnknownJSONEntries(raw json.RawMessage, typ reflect.Type, where string) {
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return
	}
	for i, entry := range entries {
		warnUnknownJSONKeys(entry, typ, fmt.Sprintf("%s[%d]", where, i))
	}
}

func warnUnknownConfigKeys(data []byte) {
	warnUnknownJSONKeys(data, reflect.TypeOf(Config{}), "config")
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return
	}
	warnUnknownJSONEntries(top["groups"], reflect.TypeOf(GroupConfig{}), "config groups")
	warnUnknownJSONEntries(top["feeds"], reflect.TypeOf(FeedConfig{}), "config feeds")
	if raw, ok := top["feed"]; ok {
		warnUnknownJSONKeys(raw, reflect.TypeOf(FeedConfig{}), "config feed")
	}
}

// LoadConfig reads, validates, defaults, and normalizes a JSON configuration file.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	warnUnknownConfigKeys(data)
	// Merge legacy group IDs before building the canonical mirror.
	legacy := c.GroupIDs
	if c.GroupID != 0 {
		legacy = append(legacy, c.GroupID)
	}
	for _, id := range legacy {
		if c.group(id) == nil {
			c.Groups = append(c.Groups, GroupConfig{ID: id})
		}
	}
	c.GroupIDs = make([]int64, 0, len(c.Groups))
	for i := range c.Groups {
		c.GroupIDs = append(c.GroupIDs, c.Groups[i].ID)
	}
	if len(c.Groups) == 0 {
		return nil, fmt.Errorf("at least one group is required (groups, group_ids, or group_id)")
	}
	// Reject invalid or duplicate groups before handlers start.
	seenGroup := map[int64]bool{}
	for i := range c.Groups {
		id := c.Groups[i].ID
		if id == 0 {
			return nil, fmt.Errorf("group id 0 is invalid (a Telegram group/supergroup id is negative)")
		}
		if seenGroup[id] {
			return nil, fmt.Errorf("duplicate group id %d", id)
		}
		seenGroup[id] = true
	}
	// Invalid repos fail here; duplicate names would collide in the cache.
	seenOverlay := map[string]bool{}
	for i, o := range c.Overlays {
		if parts := strings.Split(o.Repo, "/"); o.Repo == "" || len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("overlay %d: repo must be \"owner/name\" (got %q)", i, o.Repo)
		}
		name := o.Name
		if name == "" {
			name = o.Repo
		}
		if seenOverlay[name] {
			return nil, fmt.Errorf("duplicate overlay name %q", name)
		}
		seenOverlay[name] = true
	}

	validateQuestions := func(qs []Question, where string) error {
		for i, q := range qs {
			if len(q.Options) < 2 {
				return fmt.Errorf("%s question %d: need at least 2 options", where, i)
			}
			if q.Answer < 0 || q.Answer >= len(q.Options) {
				return fmt.Errorf("%s question %d: answer index %d out of range", where, i, q.Answer)
			}
		}
		return nil
	}
	if err := validateQuestions(c.Questions, "global"); err != nil {
		return nil, err
	}
	for i, q := range c.FallbackQuestions {
		if strings.TrimSpace(q.Q) == "" || len(q.Answers) == 0 {
			return nil, fmt.Errorf("fallback_questions %d requires q and at least one answers entry", i)
		}
		for _, a := range q.Answers {
			if strings.TrimSpace(a) == "" {
				return nil, fmt.Errorf("fallback_questions %d: answers must not contain an empty string", i)
			}
		}
	}
	if !ValidLanguage(c.Lang) {
		return nil, fmt.Errorf("lang %q is not one of %q, %q, %q", c.Lang, "zh", "zh-Hant", "en")
	}
	if c.VerifyMode != "" && !ValidMode(c.VerifyMode) {
		return nil, fmt.Errorf("verify_mode %q is not one of %q, %q, %q", c.VerifyMode, ModeKernel, ModeQuiz, ModeMixed)
	}
	for i := range c.Groups {
		g := &c.Groups[i]
		if err := validateQuestions(g.Questions, fmt.Sprintf("group %d", g.ID)); err != nil {
			return nil, err
		}
		if g.VerifyMode != "" && !ValidMode(g.VerifyMode) {
			return nil, fmt.Errorf("group %d: verify_mode %q is not one of %q, %q, %q", g.ID, g.VerifyMode, ModeKernel, ModeQuiz, ModeMixed)
		}
		if !ValidLanguage(g.Lang) {
			return nil, fmt.Errorf("group %d: lang %q is not one of %q, %q, %q", g.ID, g.Lang, "zh", "zh-Hant", "en")
		}
		// Kernel-only groups need no quiz pool; runtime quiz mode falls back to kernel.
		if c.VerifyModeFor(g.ID) != ModeKernel && len(c.QuestionsFor(g.ID)) == 0 {
			return nil, fmt.Errorf("group %d: no questions (add global questions or this group's own questions, or set verify_mode to %q)", g.ID, ModeKernel)
		}
		if c.RequiredChannel(g.ID) != 0 && c.ChannelInvite(g.ID) == "" && !strings.HasPrefix(c.ChannelDisplayFor(g.ID), "@") {
			return nil, fmt.Errorf("group %d: required_channel_id is set but the channel has no reachable link (set channel_display to an @handle, or channel_invite_url for a private channel)", g.ID)
		}
	}
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = 240
	}
	if c.TimeoutSeconds < 30 {
		c.TimeoutSeconds = 30 // a too-short timeout makes the challenge unwinnable and strikes real users
	}
	if c.TimeoutSeconds > 1800 {
		c.TimeoutSeconds = 1800
	}
	if c.NotifyTTLSeconds == 0 {
		c.NotifyTTLSeconds = 60
	}
	if c.WarnLimit <= 0 {
		c.WarnLimit = 3
	}
	if c.PrivateQueryPerMin <= 0 {
		c.PrivateQueryPerMin = 3
	}
	if c.VerifyRetrySeconds == 0 {
		c.VerifyRetrySeconds = 180 // negative is honoured as "no cooldown"
	}
	if c.VerifyMaxFails == 0 {
		c.VerifyMaxFails = 3 // negative => never auto-ban
	}
	if c.MuteSeconds <= 0 {
		c.MuteSeconds = 3600 // mute is always timed; default 1h (no permanent mute)
	}
	// Keep reported config durations within Telegram's enforced window.
	c.BanSeconds = ClampBanSeconds(c.BanSeconds)
	c.MuteSeconds = clampMuteSecs(c.MuteSeconds)
	if c.PrivateReply == "" {
		c.PrivateReply = defaultPrivateReply
	}
	if c.Feed != nil { // accept singular "feed" as one entry in "feeds"
		c.Feeds = append(c.Feeds, *c.Feed)
	}
	for i := range c.Feeds {
		if !ValidLanguage(c.Feeds[i].Lang) {
			return nil, fmt.Errorf("feed %d: lang %q is not one of %q, %q, %q", i, c.Feeds[i].Lang, "zh", "zh-Hant", "en")
		}
	}
	// Duplicate chat IDs share one cursor and would silently drop each other's items.
	seenFeed := map[int64]bool{}
	deduped := c.Feeds[:0]
	for _, f := range c.Feeds {
		if f.ChatID != 0 && seenFeed[f.ChatID] {
			log.Printf("config: duplicate feed for chat_id %d ignored (feed state is per chat)", f.ChatID)
			continue
		}
		seenFeed[f.ChatID] = true
		deduped = append(deduped, f)
	}
	c.Feeds = deduped
	// An unguarded control group would lock out every global command.
	if c.ControlGroupID != 0 && !c.IsGroup(c.ControlGroupID) {
		return nil, fmt.Errorf("control_group_id %d is not one of the configured groups", c.ControlGroupID)
	}
	if c.ControlGroupID == 0 && len(c.Groups) > 1 {
		log.Printf("WARNING: control_group_id is unset; administrators of any of the %d guarded groups can change process-global settings", len(c.Groups))
	}
	return &c, nil
}

// IsGroup reports whether id is one of the guarded groups.
func (c *Config) IsGroup(id int64) bool {
	for _, g := range c.GroupIDs {
		if g == id {
			return true
		}
	}
	return false
}

// ControlGroupAllowed reports whether a chat may run process-wide commands.
func (c *Config) ControlGroupAllowed(chatID int64) (bool, string) {
	if c.ControlGroupID == 0 || chatID == c.ControlGroupID {
		return true, ""
	}
	return false, fmt.Sprintf("⛔ 该命令只能在控制群（ID %d）中使用。", c.ControlGroupID)
}

func (c *Config) group(id int64) *GroupConfig {
	for i := range c.Groups {
		if c.Groups[i].ID == id {
			return &c.Groups[i]
		}
	}
	return nil
}

// LangForGroup returns the group override, global language, or zh by default.
func (c *Config) LangForGroup(id int64) string {
	if g := c.group(id); g != nil && g.Lang != "" {
		return g.Lang
	}
	if c.Lang != "" {
		return c.Lang
	}
	return "zh"
}

// RequiredChannel returns the effective required-channel ID for a group.
func (c *Config) RequiredChannel(id int64) int64 {
	if g := c.group(id); g != nil && g.RequiredChannelID != nil {
		return *g.RequiredChannelID
	}
	return c.RequiredChannelID
}

// TrustedGroups returns the effective verification-bypass source list for a group.
func (c *Config) TrustedGroups(id int64) []int64 {
	if g := c.group(id); g != nil && g.TrustedMemberGroupIDs != nil {
		return g.TrustedMemberGroupIDs
	}
	return c.TrustedMemberGroupIDs
}

// FailOpenChannel reports whether unreadable required-channel membership admits users.
func (c *Config) FailOpenChannel() bool {
	return c.RequiredChannelFailOpen == nil || *c.RequiredChannelFailOpen
}

// ChannelDisplayFor returns the effective required-channel display name for a group.
func (c *Config) ChannelDisplayFor(id int64) string {
	if g := c.group(id); g != nil && g.ChannelDisplay != "" {
		return g.ChannelDisplay
	}
	return c.ChannelDisplay
}

// ChannelInvite returns the effective private-channel invite URL for a group.
func (c *Config) ChannelInvite(id int64) string {
	if g := c.group(id); g != nil && g.ChannelInviteURL != "" {
		return g.ChannelInviteURL
	}
	return c.ChannelInviteURL
}

// VerifyModeFor returns the effective verification mode for a group.
func (c *Config) VerifyModeFor(id int64) string {
	if g := c.group(id); g != nil && ValidMode(g.VerifyMode) {
		return g.VerifyMode
	}
	if ValidMode(c.VerifyMode) {
		return c.VerifyMode
	}
	return defaultVerifyMode
}

// QuestionsFor returns the effective verification quiz pool for a group.
func (c *Config) QuestionsFor(id int64) []Question {
	if g := c.group(id); g != nil && len(g.Questions) > 0 {
		return g.Questions
	}
	return c.Questions
}

// IsKnownChat is the auto-leave allowlist, including support-only chats.
func (c *Config) IsKnownChat(id int64) bool {
	if c.IsGroup(id) ||
		(c.RequiredChannelID != 0 && id == c.RequiredChannelID) ||
		(c.AdminLogChatID != 0 && id == c.AdminLogChatID) {
		return true
	}
	// Explicit support-only chats.
	for _, k := range c.KnownChatIDs {
		if k == id {
			return true
		}
	}
	// Trusted bypass sources must remain readable.
	for _, t := range c.TrustedMemberGroupIDs {
		if t == id {
			return true
		}
	}
	for i := range c.Groups {
		if c.Groups[i].RequiredChannelID != nil && *c.Groups[i].RequiredChannelID == id {
			return true
		}
		for _, t := range c.Groups[i].TrustedMemberGroupIDs {
			if t == id {
				return true
			}
		}
	}
	for i := range c.Feeds {
		if c.Feeds[i].ChatID == id {
			return true
		}
	}
	return false
}
