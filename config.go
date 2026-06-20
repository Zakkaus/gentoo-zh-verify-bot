package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// Question is one verification quiz item. Answer is the 0-based index into Options.
type Question struct {
	Q       string   `json:"q"`
	Options []string `json:"options"`
	Answer  int      `json:"answer"`
}

// OverlayCfg is a GitHub overlay searched by /pkg.
type OverlayCfg struct {
	Name   string `json:"name"`
	Repo   string `json:"repo"`   // owner/name
	Branch string `json:"branch"` // default "master" if empty
}

// GroupConfig is one guarded group's settings. Every field except ID is optional and
// falls back to the top-level global default when unset — so groups can share settings
// or each be configured independently. Built from "groups" plus the legacy group_ids/group_id.
type GroupConfig struct {
	ID                int64      `json:"id"`
	RequiredChannelID *int64     `json:"required_channel_id"` // nil => global required_channel_id
	ChannelDisplay    string     `json:"channel_display"`     // "" => global channel_display
	ChannelInviteURL  string     `json:"channel_invite_url"`  // "" => global channel_invite_url
	Questions         []Question `json:"questions"`           // empty => global questions
}

// FeedConfig configures the optional auto-feed: the bot polls Gentoo Bugzilla + news
// and posts new items to ChatID. A nil Feed or zero ChatID disables the feature.
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

// interval is the poll interval, defaulting to 5 min with a 60 s floor.
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

// Config is loaded from a JSON file. The bot token comes from the BOT_TOKEN env var.
type Config struct {
	// Groups: per-group settings (each can override the globals below). The legacy
	// group_ids / group_id are merged in as groups with no overrides. After LoadConfig,
	// Groups is the canonical list and GroupIDs mirrors its ids.
	Groups   []GroupConfig `json:"groups"`
	GroupIDs []int64       `json:"group_ids"`
	GroupID  int64         `json:"group_id"`
	// RequiredChannelID: applicants must have joined this channel (0 = disabled).
	RequiredChannelID int64  `json:"required_channel_id"`
	ChannelDisplay    string `json:"channel_display"`
	// ChannelInviteURL: explicit join link for the required channel — needed for
	// PRIVATE channels (no public @handle). If empty, an @handle channel_display
	// is turned into a t.me link automatically.
	ChannelInviteURL string `json:"channel_invite_url"`
	// TimeoutSeconds before an unfinished verification is auto-declined.
	TimeoutSeconds int `json:"timeout_seconds"`
	// AdminLogChatID (optional): receives a line per moderation / failed-approve event.
	AdminLogChatID int64 `json:"admin_log_chat_id"`
	// NotifyTTLSeconds: auto-delete the bot's own in-group messages after N seconds
	// (0 => default 60; negative => never delete).
	NotifyTTLSeconds int `json:"notify_ttl_seconds"`
	// LookupTTLSeconds: auto-delete a lookup command (/pkg /use /bug /news /wiki /bbs /pkgs
	// /arm /armpkgs, …) AND its answer after N seconds. Unset (nil) => default 180 (3 min),
	// enabled; 0 or negative => disabled. Admins can toggle/adjust at runtime with /autodel.
	LookupTTLSeconds *int `json:"lookup_ttl_seconds"`
	// WarnLimit: number of /warn strikes before the user is auto-kicked (default 3).
	WarnLimit int `json:"warn_limit"`
	// PrivateQueryPerMin: how many lookup queries (/pkg /use /bug …) a user may run per
	// minute in a PRIVATE chat (anti-abuse; default 3). Guarded groups are never limited.
	PrivateQueryPerMin int `json:"private_query_per_min"`
	// Overlays searched by /pkg (defaults to gentoo-zh + guru when empty).
	Overlays []OverlayCfg `json:"overlays"`
	// NewsURL: the Gentoo news-items index for /news (defaults to gentoo.org when empty).
	NewsURL string `json:"news_url"`
	// StatsTimezone: IANA tz for the daily /stats reset boundary (defaults to UTC+8 when empty/invalid).
	StatsTimezone string `json:"stats_timezone"`
	// RichMessages: use Bot API 10.1 rich messages for the text-heavy /use output (more
	// info; unsupported on old/third-party clients; falls back to HTML on server reject).
	RichMessages bool `json:"rich_messages"`
	// UserAgent (optional): overrides the outbound HTTP User-Agent for /pkg /use /news /bug.
	UserAgent string `json:"user_agent"`
	// PrivateReply: the unified auto-reply sent when someone DMs the bot outside the
	// verification flow (the commands only work in groups). Empty => a built-in default.
	PrivateReply string `json:"private_reply"`
	// BlockChannelSenders: delete + ban messages posted on behalf of a channel ("channel
	// sock-puppets") in the guarded groups. Needs the bot's privacy mode OFF to see them.
	BlockChannelSenders bool `json:"block_channel_senders"`
	// ChannelWhitelist: channel sender chats allowed to post in the groups (never blocked).
	ChannelWhitelist []int64 `json:"channel_whitelist"`
	// Feeds (optional): one or more auto-feed destinations (see FeedConfig), each with its own
	// chat, language and filters. The singular "feed" is also accepted and merged into Feeds.
	Feeds     []FeedConfig `json:"feeds"`
	Feed      *FeedConfig  `json:"feed"`
	Questions []Question   `json:"questions"`
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
	// Canonical group list = explicit "groups" + legacy group_ids/group_id (as groups
	// with no overrides). Then mirror the ids into GroupIDs so IsGroup etc. stay simple.
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
	for i := range c.Groups {
		g := &c.Groups[i]
		if err := validateQuestions(g.Questions, fmt.Sprintf("group %d", g.ID)); err != nil {
			return nil, err
		}
		if len(c.questions(g.ID)) == 0 {
			return nil, fmt.Errorf("group %d: no questions (add global questions or this group's own questions)", g.ID)
		}
		if c.requiredChannel(g.ID) != 0 && c.channelInvite(g.ID) == "" && !strings.HasPrefix(c.channelDisplay(g.ID), "@") {
			return nil, fmt.Errorf("group %d: required_channel_id is set but the channel has no reachable link (set channel_display to an @handle, or channel_invite_url for a private channel)", g.ID)
		}
	}
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = 240
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
	if c.PrivateReply == "" {
		c.PrivateReply = defaultPrivateReply
	}
	if c.Feed != nil { // accept singular "feed" as one entry in "feeds"
		c.Feeds = append(c.Feeds, *c.Feed)
	}
	// Drop duplicate feed targets: feed state is keyed by chat_id, so two feeds posting to
	// the same chat would share one cursor and silently drop each other's items. Keep first.
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

// group returns the per-group config for id, or nil if id isn't a guarded group.
func (c *Config) group(id int64) *GroupConfig {
	for i := range c.Groups {
		if c.Groups[i].ID == id {
			return &c.Groups[i]
		}
	}
	return nil
}

// requiredChannel / channelDisplay / channelInvite / questions resolve a setting for a
// group: its per-group override when set, otherwise the top-level global default.
func (c *Config) requiredChannel(id int64) int64 {
	if g := c.group(id); g != nil && g.RequiredChannelID != nil {
		return *g.RequiredChannelID
	}
	return c.RequiredChannelID
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

func (c *Config) questions(id int64) []Question {
	if g := c.group(id); g != nil && len(g.Questions) > 0 {
		return g.Questions
	}
	return c.Questions
}

// IsKnownChat reports whether id is a chat the bot is meant to be in: a guarded
// group, a (global or per-group) required channel, a feed target, or the admin-log
// chat. Any other group/channel is unauthorized and the bot auto-leaves it.
func (c *Config) IsKnownChat(id int64) bool {
	if c.IsGroup(id) ||
		(c.RequiredChannelID != 0 && id == c.RequiredChannelID) ||
		(c.AdminLogChatID != 0 && id == c.AdminLogChatID) {
		return true
	}
	for i := range c.Groups {
		if c.Groups[i].RequiredChannelID != nil && *c.Groups[i].RequiredChannelID == id {
			return true
		}
	}
	for i := range c.Feeds {
		if c.Feeds[i].ChatID == id {
			return true
		}
	}
	return false
}
