package store

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
)

type configPresence struct {
	top map[string]json.RawMessage
}

func readConfigPresence(path string) (configPresence, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return configPresence{top: map[string]json.RawMessage{}}, nil
		}
		return configPresence{}, fmt.Errorf("read settings baseline: %w", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return configPresence{}, fmt.Errorf("parse settings baseline: %w", err)
	}
	return configPresence{top: top}, nil
}

func rawKeyPresent(object map[string]json.RawMessage, key string) bool {
	raw, ok := object[key]
	return ok && len(raw) != 0 && string(raw) != "null"
}

func baselineSource(present bool) Source {
	if present {
		return SourceConfig
	}
	return SourceDefault
}

// LoadBaseline builds the immutable settings baseline from config values and their JSON presence.
func LoadBaseline(configPath string, cfg *config.Config) (SettingsBaseline, error) {
	presence, err := readConfigPresence(configPath)
	if err != nil {
		return SettingsBaseline{}, err
	}
	return settingsBaselineFromConfig(cfg, presence), nil
}

func settingsBaselineFromConfig(cfg *config.Config, presence configPresence) SettingsBaseline {
	topHas := func(key string) bool { return rawKeyPresent(presence.top, key) }

	timeoutSeconds := cfg.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = 240
	}
	timeoutSeconds = min(max(timeoutSeconds, 30), 1800)
	lookupTTLSeconds := 180
	if cfg.LookupTTLSeconds != nil {
		lookupTTLSeconds = max(*cfg.LookupTTLSeconds, 0)
	}
	lookupAutoDeleteEnabled := lookupTTLSeconds > 0
	verifyMaxFails := cfg.VerifyMaxFails
	if verifyMaxFails == 0 {
		verifyMaxFails = 3
	}
	verifyRetrySeconds := cfg.VerifyRetrySeconds
	if verifyRetrySeconds == 0 {
		verifyRetrySeconds = 180
	}
	privateQueryPerMin := cfg.PrivateQueryPerMin
	if privateQueryPerMin <= 0 {
		privateQueryPerMin = 3
	}
	verifyMode := cfg.VerifyMode
	verifyModeSource := baselineSource(topHas("verify_mode"))
	if !config.ValidMode(verifyMode) {
		verifyMode = config.ModeKernel
		verifyModeSource = SourceDefault
	}
	lang := cfg.Lang
	langSource := baselineSource(topHas("lang"))
	if lang == "" {
		lang = "zh"
		langSource = SourceDefault
	}

	defaultGroup := GroupBaseline{
		Enabled:                 BaselineValue[bool]{Value: true, Source: SourceDefault},
		VerifyMode:              BaselineValue[string]{Value: verifyMode, Source: verifyModeSource},
		NameSpoiler:             BaselineValue[bool]{Value: true, Source: SourceDefault},
		BanSeconds:              BaselineValue[int]{Value: config.ClampBanSeconds(cfg.BanSeconds), Source: baselineSource(topHas("ban_seconds"))},
		LookupTTLSeconds:        BaselineValue[int]{Value: lookupTTLSeconds, Source: baselineSource(topHas("lookup_ttl_seconds"))},
		LookupAutoDeleteEnabled: BaselineValue[bool]{Value: lookupAutoDeleteEnabled, Source: baselineSource(topHas("lookup_ttl_seconds"))},
		TimeoutSeconds:          BaselineValue[int]{Value: timeoutSeconds, Source: baselineSource(topHas("timeout_seconds"))},
		VerifyMaxFails:          BaselineValue[int]{Value: verifyMaxFails, Source: baselineSource(topHas("verify_max_fails"))},
		VerifyRetrySeconds:      BaselineValue[int]{Value: verifyRetrySeconds, Source: baselineSource(topHas("verify_retry_seconds"))},
		AntispamEnabled:         BaselineValue[bool]{Value: cfg.BlockChannelSenders, Source: baselineSource(topHas("block_channel_senders"))},
		ChannelWhitelist:        BaselineValue[[]int64]{Value: cfg.ChannelWhitelist, Source: baselineSource(topHas("channel_whitelist"))},
		TrustedMemberGroupIDs:   BaselineValue[[]int64]{Value: cfg.TrustedMemberGroupIDs, Source: baselineSource(topHas("trusted_member_group_ids"))},
		KnownChatIDs:            BaselineValue[[]int64]{Value: cfg.KnownChatIDs, Source: baselineSource(topHas("known_chat_ids"))},
		RequiredChannelID:       BaselineValue[int64]{Value: cfg.RequiredChannelID, Source: baselineSource(topHas("required_channel_id"))},
		ChannelDisplay:          BaselineValue[string]{Value: cfg.ChannelDisplay, Source: baselineSource(topHas("channel_display"))},
		ChannelInviteURL:        BaselineValue[string]{Value: cfg.ChannelInviteURL, Source: baselineSource(topHas("channel_invite_url"))},
		Questions:               BaselineValue[[]config.Question]{Value: cfg.Questions, Source: baselineSource(topHas("questions"))},
		FallbackQuestions:       BaselineValue[[]config.ShortQuestion]{Value: cfg.FallbackQuestions, Source: baselineSource(topHas("fallback_questions"))},
		FallbackBuiltin:         BaselineValue[bool]{Value: len(cfg.FallbackQuestions) == 0, Source: baselineSource(len(cfg.FallbackQuestions) > 0)},
		Lang:                    BaselineValue[string]{Value: lang, Source: langSource},
	}
	if len(cfg.FallbackQuestions) == 0 {
		defaultGroup.FallbackQuestions.Source = SourceDefault
	}

	baseline := SettingsBaseline{
		DefaultGroup:   defaultGroup,
		ControlGroupID: cfg.ControlGroupID,
		Global: GlobalBaseline{
			RichMessages:       BaselineValue[bool]{Value: cfg.RichMessages, Source: baselineSource(topHas("rich_messages"))},
			PrivateQueryPerMin: BaselineValue[int]{Value: privateQueryPerMin, Source: baselineSource(topHas("private_query_per_min"))},
		},
	}
	baseline.Groups = make([]GroupBaseline, 0, max(len(cfg.Groups), len(cfg.GroupIDs)))
	groupSeen := make(map[int64]bool, max(len(cfg.Groups), len(cfg.GroupIDs)))
	for _, configured := range cfg.Groups {
		group := defaultGroup
		group.ID = configured.ID

		if configured.RequiredChannelID != nil {
			group.RequiredChannelID = BaselineValue[int64]{Value: *configured.RequiredChannelID, Source: SourceConfig}
		}
		if configured.ChannelDisplay != "" {
			group.ChannelDisplay = BaselineValue[string]{Value: configured.ChannelDisplay, Source: SourceConfig}
		}
		if configured.ChannelInviteURL != "" {
			group.ChannelInviteURL = BaselineValue[string]{Value: configured.ChannelInviteURL, Source: SourceConfig}
		}
		if config.ValidMode(configured.VerifyMode) {
			group.VerifyMode = BaselineValue[string]{Value: configured.VerifyMode, Source: SourceConfig}
		}
		if len(configured.Questions) > 0 {
			group.Questions = BaselineValue[[]config.Question]{Value: configured.Questions, Source: SourceConfig}
		}
		if configured.TrustedMemberGroupIDs != nil {
			group.TrustedMemberGroupIDs = BaselineValue[[]int64]{Value: configured.TrustedMemberGroupIDs, Source: SourceConfig}
		}
		if configured.Lang != "" {
			group.Lang = BaselineValue[string]{Value: configured.Lang, Source: SourceConfig}
		}
		baseline.Groups = append(baseline.Groups, group)
		groupSeen[group.ID] = true
	}
	for _, groupID := range cfg.GroupIDs {
		if groupID == 0 || groupSeen[groupID] {
			continue
		}
		group := defaultGroup
		group.ID = groupID
		baseline.Groups = append(baseline.Groups, group)
		groupSeen[groupID] = true
	}
	return baseline
}
