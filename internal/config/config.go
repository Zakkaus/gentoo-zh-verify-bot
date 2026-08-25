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

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
)

const (
	// ModeQuiz presents shuffled inline-button questions.
	ModeQuiz = "quiz"
	// ModeKernel requires applicants to type a kernel version.
	ModeKernel = "kernel"
	// ModeMixed chooses quiz or kernel verification per applicant.
	ModeMixed = "mixed"
)

const (
	// DeliveryGroup posts the challenge only in the guarded group.
	DeliveryGroup = "group"
	// DeliveryDM attempts a private challenge and posts in the group only after a definite rejection.
	DeliveryDM = "dm"
	// DeliveryBoth posts in the group before attempting the private challenge.
	DeliveryBoth = "both"
)

const defaultDeliveryMode = DeliveryBoth

// ValidDeliveryMode reports whether mode names a supported challenge delivery mode.
func ValidDeliveryMode(mode string) bool {
	switch mode {
	case DeliveryGroup, DeliveryDM, DeliveryBoth:
		return true
	}
	return false
}

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

const (
	maxDurationSeconds           = int64((1<<63 - 1) / int64(time.Second))
	maxFeedIntervalSeconds       = 24 * 60 * 60
	maxOwnerClaimLifetimeSeconds = 24 * 60 * 60
	maxMessageTTLSeconds         = 24 * 60 * 60
	maxVerifyRetrySeconds        = telegramBanMax
)

// SecondsToDuration converts seconds without overflowing time.Duration.
func SecondsToDuration(seconds int) (time.Duration, bool) {
	value := int64(seconds)
	if value < -maxDurationSeconds || value > maxDurationSeconds {
		return 0, false
	}
	return time.Duration(value) * time.Second, true
}

func checkedConfigDuration(key string, seconds int, minimum, operationalMaximum int64) (time.Duration, error) {
	maximum := min(operationalMaximum, maxDurationSeconds)
	value := int64(seconds)
	if value < minimum || value > maximum {
		return 0, fmt.Errorf("%s=%d is outside the accepted range %d..%d seconds", key, seconds, minimum, maximum)
	}
	duration, ok := SecondsToDuration(seconds)
	if !ok {
		return 0, fmt.Errorf("%s=%d is outside the accepted range %d..%d seconds", key, seconds, minimum, maximum)
	}
	return duration, nil
}

func validatePositiveConfigDuration(key string, seconds int, operationalMaximum int64) error {
	if seconds <= 0 {
		return nil
	}
	_, err := checkedConfigDuration(key, seconds, 1, operationalMaximum)
	return err
}

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
	// DeliveryMode is group, dm, both, or empty to inherit.
	DeliveryMode string `json:"delivery_mode"`
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
	// IntervalSeconds is the polling interval with a 300-second default and one-day maximum.
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
	seconds := f.IntervalSeconds
	switch {
	case seconds <= 0:
		seconds = 5 * 60
	case seconds < 60:
		seconds = 60
	case seconds > maxFeedIntervalSeconds:
		seconds = maxFeedIntervalSeconds
	}
	duration, _ := SecondsToDuration(seconds)
	return duration
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
	// OwnerClaimLifetimeSeconds limits the one-use journal claim to at most one day.
	OwnerClaimLifetimeSeconds int `json:"owner_claim_lifetime_seconds"`
	// OwnerClaimUserID optionally restricts the first owner claim to one Telegram user.
	OwnerClaimUserID int64 `json:"owner_claim_user_id"`
	// ChannelInviteURL links a private required channel without a public handle.
	ChannelInviteURL string `json:"channel_invite_url"`
	// TimeoutSeconds is the verification deadline.
	TimeoutSeconds int `json:"timeout_seconds"`
	// AdminLogChatID receives moderation and failed-action notices.
	AdminLogChatID int64 `json:"admin_log_chat_id"`
	// NotifyTTLSeconds controls notice deletion, defaults to 60, and has a one-day maximum.
	NotifyTTLSeconds int `json:"notify_ttl_seconds"`
	// LookupTTLSeconds controls lookup deletion, defaults to 180, and has a one-day maximum.
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
	// VerifyRetrySeconds is the cooldown, capped at 366 days; a negative value disables it.
	VerifyRetrySeconds int `json:"verify_retry_seconds"`
	// VerifyMaxFails is the automatic-ban threshold and a negative value disables it.
	VerifyMaxFails int `json:"verify_max_fails"`
	// VerifyMode selects kernel, quiz, or mixed verification.
	VerifyMode string `json:"verify_mode"`
	// DeliveryMode selects group-only, private-with-fallback, or group-and-private challenge delivery.
	DeliveryMode string `json:"delivery_mode"`
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
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read config: %w", err)
		}
		data = []byte("{}")
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
	if c.DeliveryMode != "" && !ValidDeliveryMode(c.DeliveryMode) {
		return nil, fmt.Errorf("delivery_mode %q is not one of %q, %q, %q", c.DeliveryMode, DeliveryGroup, DeliveryDM, DeliveryBoth)
	}
	for i := range c.Groups {
		g := &c.Groups[i]
		if err := validateQuestions(g.Questions, fmt.Sprintf("group %d", g.ID)); err != nil {
			return nil, err
		}
		if g.VerifyMode != "" && !ValidMode(g.VerifyMode) {
			return nil, fmt.Errorf("group %d: verify_mode %q is not one of %q, %q, %q", g.ID, g.VerifyMode, ModeKernel, ModeQuiz, ModeMixed)
		}
		if g.DeliveryMode != "" && !ValidDeliveryMode(g.DeliveryMode) {
			return nil, fmt.Errorf("group %d: delivery_mode %q is not one of %q, %q, %q", g.ID, g.DeliveryMode, DeliveryGroup, DeliveryDM, DeliveryBoth)
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
	if len(c.Groups) == 0 {
		if c.VerifyMode != "" && c.VerifyMode != ModeKernel && len(c.Questions) == 0 {
			return nil, fmt.Errorf("default runtime group: no questions (add global questions or set verify_mode to %q)", ModeKernel)
		}
		if c.RequiredChannelID != 0 && c.ChannelInviteURL == "" && !strings.HasPrefix(c.ChannelDisplay, "@") {
			return nil, fmt.Errorf("default runtime group: required_channel_id is set but the channel has no reachable link (set channel_display to an @handle, or channel_invite_url for a private channel)")
		}
	}
	if err := validatePositiveConfigDuration("timeout_seconds", c.TimeoutSeconds, maxDurationSeconds); err != nil {
		return nil, err
	}
	if err := validatePositiveConfigDuration("notify_ttl_seconds", c.NotifyTTLSeconds, maxMessageTTLSeconds); err != nil {
		return nil, err
	}
	if c.LookupTTLSeconds != nil {
		if err := validatePositiveConfigDuration("lookup_ttl_seconds", *c.LookupTTLSeconds, maxMessageTTLSeconds); err != nil {
			return nil, err
		}
	}
	if err := validatePositiveConfigDuration("ban_seconds", c.BanSeconds, maxDurationSeconds); err != nil {
		return nil, err
	}
	if err := validatePositiveConfigDuration("mute_seconds", c.MuteSeconds, maxDurationSeconds); err != nil {
		return nil, err
	}
	if err := validatePositiveConfigDuration("verify_retry_seconds", c.VerifyRetrySeconds, maxVerifyRetrySeconds); err != nil {
		return nil, err
	}
	if err := validatePositiveConfigDuration("owner_claim_lifetime_seconds", c.OwnerClaimLifetimeSeconds, maxOwnerClaimLifetimeSeconds); err != nil {
		return nil, err
	}
	if c.OwnerClaimLifetimeSeconds < 0 {
		return nil, fmt.Errorf("owner_claim_lifetime_seconds must not be negative")
	}
	if c.OwnerClaimUserID < 0 {
		return nil, fmt.Errorf("owner_claim_user_id must not be negative")
	}
	if c.OwnerClaimLifetimeSeconds == 0 {
		c.OwnerClaimLifetimeSeconds = 10 * 60
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
	if c.Feed != nil { // accept singular "feed" as one entry in "feeds"
		c.Feeds = append(c.Feeds, *c.Feed)
	}
	for i := range c.Feeds {
		if err := validatePositiveConfigDuration(
			fmt.Sprintf("feeds[%d].interval_seconds", i), c.Feeds[i].IntervalSeconds, maxFeedIntervalSeconds,
		); err != nil {
			return nil, err
		}
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

// OwnerClaimLifetime returns the configured first-owner claim lifetime.
func (c *Config) OwnerClaimLifetime() time.Duration {
	seconds := c.OwnerClaimLifetimeSeconds
	if seconds <= 0 || seconds > maxOwnerClaimLifetimeSeconds {
		seconds = 10 * 60
	}
	duration, _ := SecondsToDuration(seconds)
	return duration
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
	l := i18n.FromStored(c.LangForGroup(chatID))
	return false, i18n.Messages.Feed.Config.ControlGroupOnly.Render(l, c.ControlGroupID)
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

// DeliveryModeFor returns the effective challenge delivery mode for a group.
func (c *Config) DeliveryModeFor(id int64) string {
	if g := c.group(id); g != nil && ValidDeliveryMode(g.DeliveryMode) {
		return g.DeliveryMode
	}
	if ValidDeliveryMode(c.DeliveryMode) {
		return c.DeliveryMode
	}
	return defaultDeliveryMode
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
