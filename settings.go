package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
)

type configPresence struct {
	top map[string]json.RawMessage
}

func readConfigPresence(path string) (configPresence, error) {
	data, err := os.ReadFile(path)
	if err != nil {
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

func baselineSource(present bool) store.Source {
	if present {
		return store.SourceConfig
	}
	return store.SourceDefault
}

func settingsBaseline(configPath string, cfg *config.Config) (store.SettingsBaseline, error) {
	presence, err := readConfigPresence(configPath)
	if err != nil {
		return store.SettingsBaseline{}, err
	}
	return settingsBaselineFromConfig(cfg, presence), nil
}

func settingsBaselineFromConfig(cfg *config.Config, presence configPresence) store.SettingsBaseline {
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
		verifyModeSource = store.SourceDefault
	}

	defaultGroup := store.GroupBaseline{
		Enabled:                 store.BaselineValue[bool]{Value: true, Source: store.SourceDefault},
		VerifyMode:              store.BaselineValue[string]{Value: verifyMode, Source: verifyModeSource},
		NameSpoiler:             store.BaselineValue[bool]{Value: true, Source: store.SourceDefault},
		BanSeconds:              store.BaselineValue[int]{Value: config.ClampBanSeconds(cfg.BanSeconds), Source: baselineSource(topHas("ban_seconds"))},
		LookupTTLSeconds:        store.BaselineValue[int]{Value: lookupTTLSeconds, Source: baselineSource(topHas("lookup_ttl_seconds"))},
		LookupAutoDeleteEnabled: store.BaselineValue[bool]{Value: lookupAutoDeleteEnabled, Source: baselineSource(topHas("lookup_ttl_seconds"))},
		TimeoutSeconds:          store.BaselineValue[int]{Value: timeoutSeconds, Source: baselineSource(topHas("timeout_seconds"))},
		VerifyMaxFails:          store.BaselineValue[int]{Value: verifyMaxFails, Source: baselineSource(topHas("verify_max_fails"))},
		VerifyRetrySeconds:      store.BaselineValue[int]{Value: verifyRetrySeconds, Source: baselineSource(topHas("verify_retry_seconds"))},
		AntispamEnabled:         store.BaselineValue[bool]{Value: cfg.BlockChannelSenders, Source: baselineSource(topHas("block_channel_senders"))},
		ChannelWhitelist:        store.BaselineValue[[]int64]{Value: cfg.ChannelWhitelist, Source: baselineSource(topHas("channel_whitelist"))},
		TrustedMemberGroupIDs:   store.BaselineValue[[]int64]{Value: cfg.TrustedMemberGroupIDs, Source: baselineSource(topHas("trusted_member_group_ids"))},
		KnownChatIDs:            store.BaselineValue[[]int64]{Value: cfg.KnownChatIDs, Source: baselineSource(topHas("known_chat_ids"))},
		RequiredChannelID:       store.BaselineValue[int64]{Value: cfg.RequiredChannelID, Source: baselineSource(topHas("required_channel_id"))},
		ChannelDisplay:          store.BaselineValue[string]{Value: cfg.ChannelDisplay, Source: baselineSource(topHas("channel_display"))},
		ChannelInviteURL:        store.BaselineValue[string]{Value: cfg.ChannelInviteURL, Source: baselineSource(topHas("channel_invite_url"))},
		Questions:               store.BaselineValue[[]config.Question]{Value: cfg.Questions, Source: baselineSource(topHas("questions"))},
		FallbackQuestions:       store.BaselineValue[[]config.ShortQuestion]{Value: cfg.FallbackQuestions, Source: baselineSource(topHas("fallback_questions"))},
		FallbackBuiltin:         store.BaselineValue[bool]{Value: len(cfg.FallbackQuestions) == 0, Source: baselineSource(len(cfg.FallbackQuestions) > 0)},
		Lang:                    store.BaselineValue[string]{Value: "", Source: store.SourceDefault},
	}
	if len(cfg.FallbackQuestions) == 0 {
		defaultGroup.FallbackQuestions.Source = store.SourceDefault
	}

	baseline := store.SettingsBaseline{
		DefaultGroup:   defaultGroup,
		ControlGroupID: cfg.ControlGroupID,
		Global: store.GlobalBaseline{
			RichMessages:       store.BaselineValue[bool]{Value: cfg.RichMessages, Source: baselineSource(topHas("rich_messages"))},
			PrivateQueryPerMin: store.BaselineValue[int]{Value: privateQueryPerMin, Source: baselineSource(topHas("private_query_per_min"))},
		},
	}
	baseline.Groups = make([]store.GroupBaseline, 0, max(len(cfg.Groups), len(cfg.GroupIDs)))
	groupSeen := make(map[int64]bool, max(len(cfg.Groups), len(cfg.GroupIDs)))
	for _, configured := range cfg.Groups {
		group := defaultGroup
		group.ID = configured.ID

		if configured.RequiredChannelID != nil {
			group.RequiredChannelID = store.BaselineValue[int64]{Value: *configured.RequiredChannelID, Source: store.SourceConfig}
		}
		if configured.ChannelDisplay != "" {
			group.ChannelDisplay = store.BaselineValue[string]{Value: configured.ChannelDisplay, Source: store.SourceConfig}
		}
		if configured.ChannelInviteURL != "" {
			group.ChannelInviteURL = store.BaselineValue[string]{Value: configured.ChannelInviteURL, Source: store.SourceConfig}
		}
		if config.ValidMode(configured.VerifyMode) {
			group.VerifyMode = store.BaselineValue[string]{Value: configured.VerifyMode, Source: store.SourceConfig}
		}
		if len(configured.Questions) > 0 {
			group.Questions = store.BaselineValue[[]config.Question]{Value: configured.Questions, Source: store.SourceConfig}
		}
		if configured.TrustedMemberGroupIDs != nil {
			group.TrustedMemberGroupIDs = store.BaselineValue[[]int64]{Value: configured.TrustedMemberGroupIDs, Source: store.SourceConfig}
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

func configWithEffectiveGroups(cfg *config.Config, settings *store.Settings) *config.Config {
	effective := *cfg
	effective.Groups = append([]config.GroupConfig(nil), cfg.Groups...)
	effective.GroupIDs = append([]int64(nil), cfg.GroupIDs...)
	groupSeen := make(map[int64]bool, len(effective.Groups))
	for _, group := range effective.Groups {
		groupSeen[group.ID] = true
	}
	idSeen := make(map[int64]bool, len(effective.GroupIDs))
	for _, groupID := range effective.GroupIDs {
		idSeen[groupID] = true
	}
	for _, groupID := range settings.GroupIDs() {
		if !groupSeen[groupID] {
			effective.Groups = append(effective.Groups, config.GroupConfig{ID: groupID})
			groupSeen[groupID] = true
		}
		if !idSeen[groupID] {
			effective.GroupIDs = append(effective.GroupIDs, groupID)
			idSeen[groupID] = true
		}
	}
	return &effective
}

func installRuntimeSettings(v *Verifier, settings *store.Settings) {
	v.settings = settings
}

func installBaselineRuntimeSettings(v *Verifier) {
	baseline := settingsBaselineFromConfig(v.cfg, configPresence{})
	if len(baseline.Groups) == 0 {
		return
	}
	settings, err := store.NewSettings("", baseline)
	if err != nil {
		panic(fmt.Sprintf("invalid runtime settings baseline: %v", err))
	}
	installRuntimeSettings(v, settings)
}

func (v *Verifier) groupSettings(groupID int64) (store.GroupView, bool) {
	if v.settings == nil {
		return store.GroupView{}, false
	}
	return v.settings.Group(groupID)
}

func (v *Verifier) fallbackGroupSettings(groupID int64) store.GroupBaseline {
	baseline := settingsBaselineFromConfig(v.cfg, configPresence{})
	for _, group := range baseline.Groups {
		if group.ID == groupID {
			return group
		}
	}
	return baseline.DefaultGroup
}

func (v *Verifier) controlGroupID() int64 {
	if v.settings != nil {
		return v.settings.ControlGroupID()
	}
	if v.cfg.ControlGroupID != 0 {
		return v.cfg.ControlGroupID
	}
	if len(v.cfg.GroupIDs) != 0 {
		return v.cfg.GroupIDs[0]
	}
	if len(v.cfg.Groups) != 0 {
		return v.cfg.Groups[0].ID
	}
	return 0
}

func (v *Verifier) updateGroupSettings(groupID int64, update func(store.GroupView, *store.GroupOverrides)) error {
	if v.settings == nil {
		return fmt.Errorf("%w: runtime settings are not installed", store.ErrSettingsUnavailable)
	}
	group, ok := v.settings.Group(groupID)
	if !ok {
		return fmt.Errorf("%w: %d", store.ErrUnknownGroup, groupID)
	}
	overrides := group.Overrides()
	update(group, &overrides)
	_, err := v.settings.CommitGroup(groupID, group.Revision(), overrides)
	return err
}

func (v *Verifier) updateGlobalSettings(update func(store.GlobalView, *store.GlobalOverrides)) error {
	if v.settings == nil {
		return fmt.Errorf("%w: runtime settings are not installed", store.ErrSettingsUnavailable)
	}
	global := v.settings.Global()
	overrides := global.Overrides()
	update(global, &overrides)
	_, err := v.settings.CommitGlobal(global.Revision(), overrides)
	return err
}
