package main

import (
	"encoding/json"
	"fmt"
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
	SilentBugs      *bool  `json:"silent_bugs"`      // post bugs without a notification (default true)
}

func (f *FeedConfig) bugsOn() bool     { return f.Bugs == nil || *f.Bugs }
func (f *FeedConfig) newsOn() bool     { return f.News == nil || *f.News }
func (f *FeedConfig) silentBugs() bool { return f.SilentBugs == nil || *f.SilentBugs }

// interval is the poll interval, defaulting to 5 min with a 60 s floor.
func (f *FeedConfig) interval() time.Duration {
	if f.IntervalSeconds >= 60 {
		return time.Duration(f.IntervalSeconds) * time.Second
	}
	return 5 * time.Minute
}

// Config is loaded from a JSON file. The bot token comes from the BOT_TOKEN env var.
type Config struct {
	// GroupIDs: the groups this bot guards. You may also use the singular "group_id".
	GroupIDs []int64 `json:"group_ids"`
	GroupID  int64   `json:"group_id"`
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
	// WarnLimit: number of /warn strikes before the user is auto-kicked (default 3).
	WarnLimit int `json:"warn_limit"`
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
	if c.GroupID != 0 && !c.IsGroup(c.GroupID) {
		c.GroupIDs = append(c.GroupIDs, c.GroupID)
	}
	if len(c.GroupIDs) == 0 {
		return nil, fmt.Errorf("at least one group is required (group_ids or group_id)")
	}
	if len(c.Questions) == 0 {
		return nil, fmt.Errorf("at least one question is required")
	}
	for i, q := range c.Questions {
		if len(q.Options) < 2 {
			return nil, fmt.Errorf("question %d: need at least 2 options", i)
		}
		if q.Answer < 0 || q.Answer >= len(q.Options) {
			return nil, fmt.Errorf("question %d: answer index %d out of range", i, q.Answer)
		}
	}
	if c.RequiredChannelID != 0 && c.ChannelInviteURL == "" && !strings.HasPrefix(c.ChannelDisplay, "@") {
		return nil, fmt.Errorf("required_channel_id is set but the channel has no reachable link: set channel_display to an @handle, or set channel_invite_url (private channel)")
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
	if c.Feed != nil { // accept singular "feed" as one entry in "feeds"
		c.Feeds = append(c.Feeds, *c.Feed)
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

// warnLimit is the number of /warn strikes before an auto-kick (default 3).
func (c *Config) warnLimit() int {
	if c.WarnLimit > 0 {
		return c.WarnLimit
	}
	return 3
}

// IsKnownChat reports whether id is a chat the bot is meant to be in: a guarded
// group, the required channel, a feed target, or the admin-log chat. Any other
// group/channel is unauthorized and the bot auto-leaves it.
func (c *Config) IsKnownChat(id int64) bool {
	if c.IsGroup(id) ||
		(c.RequiredChannelID != 0 && id == c.RequiredChannelID) ||
		(c.AdminLogChatID != 0 && id == c.AdminLogChatID) {
		return true
	}
	for i := range c.Feeds {
		if c.Feeds[i].ChatID == id {
			return true
		}
	}
	return false
}
