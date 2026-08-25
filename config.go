package main

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

// Question is one verification quiz item. Answer is the 0-based index into Options.
type Question struct {
	Q       string   `json:"q"`
	Options []string `json:"options"`
	Answer  int      `json:"answer"`
}

// ShortQuestion is typed; accepted answers never appear in its prompt.
// Answers contains normalized whole replies, not substrings in prose.
type ShortQuestion struct {
	Q       string   `json:"q"`
	Answers []string `json:"answers"`
}

// OverlayCfg is a GitHub overlay searched by /pkg.
type OverlayCfg struct {
	Name   string `json:"name"`
	Repo   string `json:"repo"`   // owner/name
	Branch string `json:"branch"` // default "master" if empty
}

// GroupConfig overrides top-level defaults for one guarded group.
type GroupConfig struct {
	ID                int64      `json:"id"`
	RequiredChannelID *int64     `json:"required_channel_id"` // nil => global required_channel_id
	ChannelDisplay    string     `json:"channel_display"`     // "" => global channel_display
	ChannelInviteURL  string     `json:"channel_invite_url"`  // "" => global channel_invite_url
	Questions         []Question `json:"questions"`           // empty => global questions
	// VerifyMode is "kernel", "quiz", "mixed", or "" to inherit.
	VerifyMode string `json:"verify_mode"`
	// TrustedMemberGroupIDs bypasses verification for confirmed members of other chats.
	// nil inherits the global list; [] disables it; non-empty replaces it.
	// Failed membership checks fall back to verification, never approval.
	TrustedMemberGroupIDs []int64 `json:"trusted_member_group_ids"`
}

// FeedConfig configures one optional Bugzilla/news destination.
type FeedConfig struct {
	ChatID          int64  `json:"chat_id"`          // channel/group to post to (bot must be admin there)
	Lang            string `json:"lang"`             // bug field labels: "zh" (default) or "en"
	IntervalSeconds int    `json:"interval_seconds"` // poll interval; default 300, min 60
	Bugs            *bool  `json:"bugs"`             // post new Bugzilla bugs (default true)
	News            *bool  `json:"news"`             // post new news items (default true)
	BugProduct      string `json:"bug_product"`      // only bugs in this Bugzilla product (empty = all)
	BugComponent    string `json:"bug_component"`    // only bugs in this component (empty = all)
	SilentBugs      *bool  `json:"silent_bugs"`      // true => all bugs silent; unset => only UNCONFIRMED silent (see bugSilent)
}

func (f *FeedConfig) bugsOn() bool { return f.Bugs == nil || *f.Bugs }
func (f *FeedConfig) newsOn() bool { return f.News == nil || *f.News }

func (f *FeedConfig) interval() time.Duration {
	switch {
	case f.IntervalSeconds <= 0:
		return 5 * time.Minute // unset -> default
	case f.IntervalSeconds < 60:
		return 60 * time.Second // clamp a too-fast interval to the 60 s floor
	default:
		return time.Duration(f.IntervalSeconds) * time.Second
	}
}

// Config is loaded from JSON; BOT_TOKEN remains environment-only.
type Config struct {
	// Groups is canonical after legacy group_ids/group_id are merged; GroupIDs mirrors it.
	Groups   []GroupConfig `json:"groups"`
	GroupIDs []int64       `json:"group_ids"`
	GroupID  int64         `json:"group_id"`
	// ControlGroupID limits global commands; zero allows any guarded group's admins.
	ControlGroupID int64 `json:"control_group_id"`
	// RequiredChannelID gates approval on channel membership; zero disables it.
	RequiredChannelID int64  `json:"required_channel_id"`
	ChannelDisplay    string `json:"channel_display"`
	// TrustedMemberGroupIDs is the global bypass source list.
	// Unreadable membership falls back to verification.
	TrustedMemberGroupIDs []int64 `json:"trusted_member_group_ids"`
	// KnownChatIDs prevents auto-leave without granting verification or bypass semantics.
	KnownChatIDs []int64 `json:"known_chat_ids"`
	// ChannelInviteURL is required for private channels without a public handle.
	ChannelInviteURL string `json:"channel_invite_url"`
	// TimeoutSeconds is the verification deadline.
	TimeoutSeconds int `json:"timeout_seconds"`
	// AdminLogChatID receives moderation and failed-action notices.
	AdminLogChatID int64 `json:"admin_log_chat_id"`
	// NotifyTTLSeconds: 0 defaults to 60; negative disables deletion.
	NotifyTTLSeconds int `json:"notify_ttl_seconds"`
	// LookupTTLSeconds deletes lookup commands and answers together.
	// nil defaults to 180; non-positive disables it.
	LookupTTLSeconds *int `json:"lookup_ttl_seconds"`
	// WarnLimit defaults to three strikes before an automatic kick.
	WarnLimit int `json:"warn_limit"`
	// PrivateQueryPerMin limits DMs only and defaults to three.
	PrivateQueryPerMin int `json:"private_query_per_min"`
	// RequiredChannelFailOpen controls unreadable membership: nil/true admits verified users;
	// false blocks them. Admins are alerted either way.
	RequiredChannelFailOpen *bool `json:"required_channel_fail_open"`
	// BanSeconds: 0 is permanent; /bantime may override it at runtime.
	BanSeconds int `json:"ban_seconds"`
	// MuteSeconds is always finite; it defaults to one hour and may be overridden per command.
	MuteSeconds int `json:"mute_seconds"`
	// VerifyRetrySeconds defaults to 180; negative disables the cooldown.
	VerifyRetrySeconds int `json:"verify_retry_seconds"`
	// VerifyMaxFails defaults to three; negative disables automatic bans.
	VerifyMaxFails int `json:"verify_max_fails"`
	// VerifyMode is "kernel", "quiz", or "mixed"; typing prevents blind button clicks.
	// Per-group config and /vmode may override it.
	VerifyMode string `json:"verify_mode"`
	// FallbackQuestions is the answer-hidden path for applicants without Linux.
	FallbackQuestions []ShortQuestion `json:"fallback_questions"`
	// Overlays searched by /pkg (defaults to gentoo-zh + guru when empty).
	Overlays []OverlayCfg `json:"overlays"`
	// NewsURL: the Gentoo news-items index for /news (defaults to gentoo.org when empty).
	NewsURL string `json:"news_url"`
	// StatsTimezone: IANA tz for the daily /stats reset boundary (defaults to UTC+8 when empty/invalid).
	StatsTimezone string `json:"stats_timezone"`
	// RichMessages falls back to HTML when Bot API 10.1 rejects the request.
	RichMessages bool `json:"rich_messages"`
	// UserAgent (optional): overrides the outbound HTTP User-Agent for /pkg /use /news /bug.
	UserAgent string `json:"user_agent"`
	// PrivateReply handles non-command DMs outside verification.
	PrivateReply string `json:"private_reply"`
	// BlockChannelSenders requires BotFather privacy mode to be off.
	BlockChannelSenders bool `json:"block_channel_senders"`
	// ChannelWhitelist: channel sender chats allowed to post in the groups (never blocked).
	ChannelWhitelist []int64 `json:"channel_whitelist"`
	// Feed is the legacy singular form and is merged into Feeds.
	Feeds     []FeedConfig `json:"feeds"`
	Feed      *FeedConfig  `json:"feed"`
	Questions []Question   `json:"questions"`
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
			return nil, fmt.Errorf("fallback_questions %d:需要 q 和至少一个 answers 条目", i)
		}
		for _, a := range q.Answers {
			if strings.TrimSpace(a) == "" {
				return nil, fmt.Errorf("fallback_questions %d: answers 不能包含空字符串", i)
			}
		}
	}
	if c.VerifyMode != "" && !validMode(c.VerifyMode) {
		return nil, fmt.Errorf("verify_mode %q is not one of %q, %q, %q", c.VerifyMode, modeKernel, modeQuiz, modeMixed)
	}
	for i := range c.Groups {
		g := &c.Groups[i]
		if err := validateQuestions(g.Questions, fmt.Sprintf("group %d", g.ID)); err != nil {
			return nil, err
		}
		if g.VerifyMode != "" && !validMode(g.VerifyMode) {
			return nil, fmt.Errorf("group %d: verify_mode %q is not one of %q, %q, %q", g.ID, g.VerifyMode, modeKernel, modeQuiz, modeMixed)
		}
		// Kernel-only groups need no quiz pool; runtime quiz mode falls back to kernel.
		if c.verifyMode(g.ID) != modeKernel && len(c.questions(g.ID)) == 0 {
			return nil, fmt.Errorf("group %d: no questions (add global questions or this group's own questions, or set verify_mode to %q)", g.ID, modeKernel)
		}
		if c.requiredChannel(g.ID) != 0 && c.channelInvite(g.ID) == "" && !strings.HasPrefix(c.channelDisplay(g.ID), "@") {
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
	c.BanSeconds = clampBanSecs(c.BanSeconds)
	c.MuteSeconds = clampMuteSecs(c.MuteSeconds)
	if c.PrivateReply == "" {
		c.PrivateReply = defaultPrivateReply
	}
	if c.Feed != nil { // accept singular "feed" as one entry in "feeds"
		c.Feeds = append(c.Feeds, *c.Feed)
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

func (c *Config) group(id int64) *GroupConfig {
	for i := range c.Groups {
		if c.Groups[i].ID == id {
			return &c.Groups[i]
		}
	}
	return nil
}

// Per-group values override top-level defaults.
func (c *Config) requiredChannel(id int64) int64 {
	if g := c.group(id); g != nil && g.RequiredChannelID != nil {
		return *g.RequiredChannelID
	}
	return c.RequiredChannelID
}

// A present per-group list, including [], overrides the global bypass list.
func (c *Config) trustedGroups(id int64) []int64 {
	if g := c.group(id); g != nil && g.TrustedMemberGroupIDs != nil {
		return g.TrustedMemberGroupIDs
	}
	return c.TrustedMemberGroupIDs
}

// Unreadable required-channel membership defaults to fail-open.
func (c *Config) failOpenChannel() bool {
	return c.RequiredChannelFailOpen == nil || *c.RequiredChannelFailOpen
}

func (c *Config) channelDisplay(id int64) string {
	if g := c.group(id); g != nil && g.ChannelDisplay != "" {
		return g.ChannelDisplay
	}
	return c.ChannelDisplay
}

func (c *Config) channelInvite(id int64) string {
	if g := c.group(id); g != nil && g.ChannelInviteURL != "" {
		return g.ChannelInviteURL
	}
	return c.ChannelInviteURL
}

// verifyMode resolves per-group, global, then built-in defaults.
func (c *Config) verifyMode(id int64) string {
	if g := c.group(id); g != nil && validMode(g.VerifyMode) {
		return g.VerifyMode
	}
	if validMode(c.VerifyMode) {
		return c.VerifyMode
	}
	return defaultVerifyMode
}

func (c *Config) questions(id int64) []Question {
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
