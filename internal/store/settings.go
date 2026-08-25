package store

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
)

// SettingsSchemaVersion is the settings.json schema written by this package.
const SettingsSchemaVersion = 2

// Source identifies where an effective setting came from.
type Source uint8

const (
	// SourceDefault is a built-in program default.
	SourceDefault Source = iota
	// SourceConfig is a value supplied by config.json.
	SourceConfig
	// SourceRuntime is a sparse settings.json override.
	SourceRuntime
)

func (s Source) String() string {
	switch s {
	case SourceDefault:
		return "built-in default"
	case SourceConfig:
		return "config file"
	case SourceRuntime:
		return "runtime override"
	default:
		return "unknown"
	}
}

// Setting pairs an effective value with its provenance.
type Setting[T any] struct {
	Value  T
	Source Source
}

// BaselineValue is one immutable config-or-default input to Settings.
type BaselineValue[T any] struct {
	Value  T
	Source Source
}

// GroupBaseline contains the configured/default values for one group.
type GroupBaseline struct {
	ID                      int64
	Enabled                 BaselineValue[bool]
	DMFirst                 BaselineValue[bool]
	VerifyMode              BaselineValue[string]
	NameSpoiler             BaselineValue[bool]
	BanSeconds              BaselineValue[int]
	LookupTTLSeconds        BaselineValue[int]
	LookupAutoDeleteEnabled BaselineValue[bool]
	TimeoutSeconds          BaselineValue[int]
	VerifyMaxFails          BaselineValue[int]
	VerifyRetrySeconds      BaselineValue[int]
	AntispamEnabled         BaselineValue[bool]
	ChannelWhitelist        BaselineValue[[]int64]
	TrustedMemberGroupIDs   BaselineValue[[]int64]
	KnownChatIDs            BaselineValue[[]int64]
	RequiredChannelID       BaselineValue[int64]
	ChannelDisplay          BaselineValue[string]
	ChannelInviteURL        BaselineValue[string]
	Questions               BaselineValue[[]config.Question]
	FallbackQuestions       BaselineValue[[]config.ShortQuestion]
	FallbackBuiltin         BaselineValue[bool]
	Lang                    BaselineValue[string]
}

// GlobalBaseline contains bot-wide configured/default values.
type GlobalBaseline struct {
	RichMessages       BaselineValue[bool]
	PrivateQueryPerMin BaselineValue[int]
}

// SettingsBaseline is the immutable config.json and built-in baseline.
type SettingsBaseline struct {
	Groups         []GroupBaseline
	DefaultGroup   GroupBaseline
	ControlGroupID int64
	Global         GlobalBaseline
}

// GroupOverrides is the sparse per-group settings.json record. A nil field follows the baseline.
type GroupOverrides struct {
	Enabled                 *bool                   `json:"enabled,omitempty"`
	DMFirst                 *bool                   `json:"dm_first,omitempty"`
	VerifyMode              *string                 `json:"verify_mode,omitempty"`
	NameSpoiler             *bool                   `json:"name_spoiler,omitempty"`
	BanSeconds              *int                    `json:"ban_seconds,omitempty"`
	LookupTTLSeconds        *int                    `json:"lookup_ttl_seconds,omitempty"`
	LookupAutoDeleteEnabled *bool                   `json:"lookup_auto_delete_enabled,omitempty"`
	TimeoutSeconds          *int                    `json:"timeout_seconds,omitempty"`
	VerifyMaxFails          *int                    `json:"verify_max_fails,omitempty"`
	VerifyRetrySeconds      *int                    `json:"verify_retry_seconds,omitempty"`
	AntispamEnabled         *bool                   `json:"antispam_enabled,omitempty"`
	ChannelWhitelist        *[]int64                `json:"channel_whitelist,omitempty"`
	TrustedMemberGroupIDs   *[]int64                `json:"trusted_member_group_ids,omitempty"`
	KnownChatIDs            *[]int64                `json:"known_chat_ids,omitempty"`
	RequiredChannelID       *int64                  `json:"required_channel_id,omitempty"`
	ChannelDisplay          *string                 `json:"channel_display,omitempty"`
	ChannelInviteURL        *string                 `json:"channel_invite_url,omitempty"`
	Questions               *[]config.Question      `json:"questions,omitempty"`
	FallbackQuestions       *[]config.ShortQuestion `json:"fallback_questions,omitempty"`
	FallbackBuiltin         *bool                   `json:"fallback_builtin,omitempty"`
	Lang                    *string                 `json:"lang,omitempty"`
}

// GlobalOverrides is the sparse bot-wide settings.json record.
type GlobalOverrides struct {
	RichMessages       *bool `json:"rich_messages,omitempty"`
	PrivateQueryPerMin *int  `json:"private_query_per_min,omitempty"`
}

// RegisteredGroup records an owner-authorized runtime group.
type RegisteredGroup struct {
	ID           int64  `json:"id"`
	RegisteredBy int64  `json:"registered_by"`
	Title        string `json:"title,omitempty"`
}

// EnrollmentNonce is one owner-issued, expiring registration capability.
type EnrollmentNonce struct {
	Nonce     string `json:"nonce"`
	IssuedBy  int64  `json:"issued_by"`
	ExpiresAt int64  `json:"expires_at"`
}

// PendingRegistration retains an authorized add-then-promote registration.
type PendingRegistration struct {
	GroupID      int64  `json:"group_id"`
	RegisteredBy int64  `json:"registered_by"`
	Title        string `json:"title,omitempty"`
	ExpiresAt    int64  `json:"expires_at"`
}

// UnknownGroupLeave retains cleanup for a group without registration authorization.
type UnknownGroupLeave struct {
	GroupID   int64  `json:"group_id"`
	Title     string `json:"title,omitempty"`
	ExpiresAt int64  `json:"expires_at"`
}

// RegistrationState is the durable owner and runtime-group metadata transaction.
type RegistrationState struct {
	Revision             uint64
	OwnerID              int64
	OwnerClaimNonce      string
	OwnerClaimExpiresAt  int64
	ControlGroupID       int64
	RegisteredGroups     []RegisteredGroup
	EnrollmentNonces     []EnrollmentNonce
	PendingRegistrations []PendingRegistration
	UnknownGroupLeaves   []UnknownGroupLeave
}

// PersistenceStatus describes whether setting commits are durable and currently allowed.
type PersistenceStatus struct {
	Configured bool
	Durable    bool
	Writable   bool
	LastError  error
}

// CommitResult reports the new revision and whether the commit reached durable storage.
type CommitResult struct {
	Revision uint64
	Durable  bool
}

// ErrSettingsConflict identifies an optimistic-concurrency failure.
var ErrSettingsConflict = errors.New("settings revision conflict")

// ErrSettingsUnavailable identifies a state file that must not be overwritten.
var ErrSettingsUnavailable = errors.New("settings writes unavailable")

// ErrSettingsNotDurable identifies an operation, such as registration, that cannot be runtime-only.
var ErrSettingsNotDurable = errors.New("settings persistence is not durable")

// ErrUnknownGroup identifies a group outside the configured and registered effective list.
var ErrUnknownGroup = errors.New("unknown settings group")

// ErrOwnerClaimInvalid identifies a claim token that is absent, used, mismatched, or expired.
var ErrOwnerClaimInvalid = errors.New("owner claim is invalid")

// ErrRegistrationOwnerOnly identifies an enrollment request made by a non-owner.
var ErrRegistrationOwnerOnly = errors.New("registration owner authorization required")

// ConflictError carries the expected and current revision for a refused write.
type ConflictError struct {
	GroupID  int64
	Expected uint64
	Actual   uint64
	Global   bool
}

func (e *ConflictError) Error() string {
	scope := fmt.Sprintf("group %d", e.GroupID)
	if e.Global {
		scope = "global settings"
	}
	return fmt.Sprintf("%s: %s: expected revision %d, current revision %d", ErrSettingsConflict, scope, e.Expected, e.Actual)
}

func (e *ConflictError) Is(target error) bool { return target == ErrSettingsConflict }

type groupRecord struct {
	Revision uint64 `json:"revision"`
	GroupOverrides
}

type globalRecord struct {
	Revision uint64 `json:"revision"`
	GlobalOverrides
}

type legacyAntispamState struct {
	Enabled   bool    `json:"enabled"`
	Whitelist []int64 `json:"whitelist"`
}

type settingsFile struct {
	Version              int                   `json:"version"`
	Enabled              *bool                 `json:"enabled"`
	NameSpoiler          *bool                 `json:"name_spoiler"`
	VerifyMode           string                `json:"verify_mode"`
	RegistrationRevision uint64                `json:"registration_revision"`
	OwnerID              int64                 `json:"owner_id"`
	OwnerClaimNonce      string                `json:"owner_claim_nonce"`
	OwnerClaimExpiresAt  int64                 `json:"owner_claim_expires_at"`
	ControlGroupID       int64                 `json:"control_group_id"`
	RegisteredGroups     []RegisteredGroup     `json:"registered_groups"`
	EnrollmentNonces     []EnrollmentNonce     `json:"enrollment_nonces"`
	PendingRegistrations []PendingRegistration `json:"pending_registrations"`
	UnknownGroupLeaves   []UnknownGroupLeave   `json:"unknown_group_leaves,omitempty"`
	Global               globalRecord          `json:"global"`
	Groups               map[int64]groupRecord `json:"groups"`
}

type effectiveGroup struct {
	id                      int64
	revision                uint64
	registered              bool
	baseline                GroupBaseline
	overrides               GroupOverrides
	enabled                 Setting[bool]
	dmFirst                 Setting[bool]
	verifyMode              Setting[string]
	nameSpoiler             Setting[bool]
	banSeconds              Setting[int]
	lookupTTLSeconds        Setting[int]
	lookupAutoDeleteEnabled Setting[bool]
	timeoutSeconds          Setting[int]
	verifyMaxFails          Setting[int]
	verifyRetrySeconds      Setting[int]
	antispamEnabled         Setting[bool]
	channelWhitelist        Setting[[]int64]
	trustedMemberGroupIDs   Setting[[]int64]
	knownChatIDs            Setting[[]int64]
	requiredChannelID       Setting[int64]
	channelDisplay          Setting[string]
	channelInviteURL        Setting[string]
	questions               Setting[[]config.Question]
	fallbackQuestions       Setting[[]config.ShortQuestion]
	fallbackBuiltin         Setting[bool]
	lang                    Setting[string]
}

type effectiveGlobal struct {
	revision           uint64
	overrides          GlobalOverrides
	richMessages       Setting[bool]
	privateQueryPerMin Setting[int]
}

type settingsSnapshot struct {
	groups       map[int64]*effectiveGroup
	groupIDs     []int64
	global       effectiveGlobal
	registration RegistrationState
}

type statusError struct{ err error }

// Settings owns the one immutable runtime-settings snapshot and its serialized commit path.
type Settings struct {
	path         string
	baseline     SettingsBaseline
	baselineByID map[int64]GroupBaseline
	writer       sync.Mutex
	state        settingsFile
	writable     bool
	snapshot     atomic.Pointer[settingsSnapshot]
	lastError    atomic.Pointer[statusError]
}

// GroupView is a read-only, allocation-free handle into one immutable snapshot.
type GroupView struct{ group *effectiveGroup }

// GlobalView is a read-only, allocation-free handle into the immutable global snapshot.
type GlobalView struct{ global *effectiveGlobal }

// NewSettings loads, migrates, and owns one settings.json transaction.
func NewSettings(path string, baseline SettingsBaseline) (*Settings, error) {
	baseline = cloneSettingsBaseline(baseline)
	normalizeBaselineLanguages(&baseline)
	if err := validateBaseline(baseline); err != nil {
		return nil, err
	}
	s := &Settings{
		path:         path,
		baseline:     baseline,
		baselineByID: make(map[int64]GroupBaseline, len(baseline.Groups)),
		writable:     true,
	}
	for _, group := range baseline.Groups {
		s.baselineByID[group.ID] = group
	}
	s.state = s.newState()

	exists := false
	if path != "" {
		if _, err := os.Stat(path); err == nil {
			exists = true
		} else if !os.IsNotExist(err) {
			exists = true
		}
	}
	migrateAntispam := path != ""
	writeMigration := false
	var versionZeroSource []byte
	if exists {
		var loaded settingsFile
		source, err := loadWithSource(path, &loaded)
		if err != nil {
			s.setLastError(err)
			if ReadFailed(err) {
				s.writable = false
				migrateAntispam = false
			}
		} else {
			switch {
			case loaded.Version == 0:
				versionZeroSource = source
				s.state = s.migrateLegacy(loaded)
				writeMigration = true
			case loaded.Version == 1:
				s.state = migrateVersionOne(loaded)
				writeMigration = true
			case loaded.Version == SettingsSchemaVersion:
				s.state = normalizeFile(loaded)
				migrateAntispam = false
			case loaded.Version > SettingsSchemaVersion:
				s.writable = false
				migrateAntispam = false
				s.setLastError(fmt.Errorf("%w: schema version %d is newer than supported version %d", ErrSettingsUnavailable, loaded.Version, SettingsSchemaVersion))
			default:
				s.writable = false
				migrateAntispam = false
				s.setLastError(fmt.Errorf("%w: invalid schema version %d", ErrSettingsUnavailable, loaded.Version))
			}
		}
	}

	if s.writable && migrateAntispam {
		legacy, present, err := loadLegacyAntispam(filepath.Join(filepath.Dir(path), "antispam.json"))
		if err != nil {
			s.writable = false
			s.setLastError(fmt.Errorf("%w: legacy antispam migration: %v", ErrSettingsUnavailable, err))
		} else if present {
			s.state = s.migrateLegacyAntispam(s.state, legacy)
			writeMigration = true
		}
	}
	if s.writable && writeMigration {
		if versionZeroSource != nil {
			backupPath := path + ".v0.bak"
			if err := writeBytes(backupPath, versionZeroSource); err != nil {
				log.Printf("ERROR settings migration: could not back up schema-v0 state %s to %s: %v", path, backupPath, err)
			}
		}
		if err := s.writeState(&s.state); err != nil {
			s.setLastError(err)
		} else {
			s.setLastError(nil)
		}
	}

	snap, err := s.buildSnapshot(s.state)
	if err != nil {
		s.writable = false
		s.setLastError(fmt.Errorf("%w: %v", ErrSettingsUnavailable, err))
		s.state = s.newState()
		snap, _ = s.buildSnapshot(s.state)
	}
	s.snapshot.Store(snap)
	return s, nil
}

// Persistence returns a lock-free persistence status snapshot.
func (s *Settings) Persistence() PersistenceStatus {
	status := PersistenceStatus{
		Configured: s.path != "",
		Durable:    s.path != "" && s.writable,
		Writable:   s.writable,
	}
	if current := s.lastError.Load(); current != nil {
		status.LastError = current.err
	}
	return status
}

// Group returns one immutable effective group view.
func (s *Settings) Group(groupID int64) (GroupView, bool) {
	group, ok := s.snapshot.Load().groups[groupID]
	return GroupView{group: group}, ok
}

// IsGroup reports whether an ID is configured or durably registered.
func (s *Settings) IsGroup(groupID int64) bool {
	_, ok := s.snapshot.Load().groups[groupID]
	return ok
}

// IsKnownChat reports whether an ID is an effective group, required channel, trusted group, or allowlisted support chat.
func (s *Settings) IsKnownChat(chatID int64) bool {
	if chatID == 0 {
		return false
	}
	snapshot := s.snapshot.Load()
	if _, ok := snapshot.groups[chatID]; ok {
		return true
	}
	for _, groupID := range snapshot.groupIDs {
		group := snapshot.groups[groupID]
		if group.requiredChannelID.Value == chatID {
			return true
		}
		for _, knownID := range group.knownChatIDs.Value {
			if knownID == chatID {
				return true
			}
		}
		for _, trustedID := range group.trustedMemberGroupIDs.Value {
			if trustedID == chatID {
				return true
			}
		}
	}
	return false
}

// GroupIDs returns the effective configured-then-registered group order.
func (s *Settings) GroupIDs() []int64 {
	return append([]int64(nil), s.snapshot.Load().groupIDs...)
}

// ControlGroupID returns the configured control group, or the first effective group when unset.
func (s *Settings) ControlGroupID() int64 {
	snap := s.snapshot.Load()
	if id := snap.registration.ControlGroupID; id != 0 {
		if _, ok := snap.groups[id]; ok {
			return id
		}
	}
	if len(snap.groupIDs) != 0 {
		return snap.groupIDs[0]
	}
	return 0
}

// Global returns the immutable effective bot-wide settings view.
func (s *Settings) Global() GlobalView {
	return GlobalView{global: &s.snapshot.Load().global}
}

// Registrations returns a detached copy of owner and runtime-group metadata.
func (s *Settings) Registrations() RegistrationState {
	return cloneRegistrationState(s.snapshot.Load().registration)
}

// CommitGroup validates and atomically commits a complete sparse record at the expected revision.
func (s *Settings) CommitGroup(groupID int64, expectedRevision uint64, next GroupOverrides) (CommitResult, error) {
	s.writer.Lock()
	defer s.writer.Unlock()
	if !s.writable {
		return CommitResult{}, s.unavailableError()
	}
	current := s.snapshot.Load()
	group, ok := current.groups[groupID]
	if !ok {
		return CommitResult{}, fmt.Errorf("%w: %d", ErrUnknownGroup, groupID)
	}
	if group.revision != expectedRevision {
		return CommitResult{}, &ConflictError{GroupID: groupID, Expected: expectedRevision, Actual: group.revision}
	}
	next = compactGroupOverrides(cloneGroupOverrides(next), group.baseline)
	if reflect.DeepEqual(group.overrides, next) {
		return CommitResult{Revision: group.revision, Durable: s.path != ""}, nil
	}
	candidate := cloneSettingsFile(s.state)
	record := candidate.Groups[groupID]
	record.Revision = group.revision + 1
	record.GroupOverrides = next
	candidate.Groups[groupID] = record
	snap, err := s.buildSnapshot(candidate)
	if err != nil {
		return CommitResult{}, err
	}
	if err := s.writeState(&candidate); err != nil {
		s.setLastError(err)
		return CommitResult{}, err
	}
	s.state = candidate
	s.snapshot.Store(snap)
	s.setLastError(nil)
	return CommitResult{Revision: record.Revision, Durable: s.path != ""}, nil
}

// CommitGlobal validates and atomically commits bot-wide overrides at the expected revision.
func (s *Settings) CommitGlobal(expectedRevision uint64, next GlobalOverrides) (CommitResult, error) {
	s.writer.Lock()
	defer s.writer.Unlock()
	if !s.writable {
		return CommitResult{}, s.unavailableError()
	}
	current := s.snapshot.Load().global
	if current.revision != expectedRevision {
		return CommitResult{}, &ConflictError{Expected: expectedRevision, Actual: current.revision, Global: true}
	}
	next = compactGlobalOverrides(cloneGlobalOverrides(next), s.baseline.Global)
	if reflect.DeepEqual(current.overrides, next) {
		return CommitResult{Revision: current.revision, Durable: s.path != ""}, nil
	}
	candidate := cloneSettingsFile(s.state)
	candidate.Global.Revision = current.revision + 1
	candidate.Global.GlobalOverrides = next
	snap, err := s.buildSnapshot(candidate)
	if err != nil {
		return CommitResult{}, err
	}
	if err := s.writeState(&candidate); err != nil {
		s.setLastError(err)
		return CommitResult{}, err
	}
	s.state = candidate
	s.snapshot.Store(snap)
	s.setLastError(nil)
	return CommitResult{Revision: candidate.Global.Revision, Durable: s.path != ""}, nil
}

// CommitRegistrations atomically commits owner and runtime-group metadata; it always requires durable storage.
func (s *Settings) CommitRegistrations(expectedRevision uint64, next RegistrationState) (CommitResult, error) {
	s.writer.Lock()
	defer s.writer.Unlock()
	if s.path == "" || !s.writable {
		if !s.writable {
			return CommitResult{}, fmt.Errorf("%w: %v", ErrSettingsNotDurable, s.unavailableError())
		}
		return CommitResult{}, ErrSettingsNotDurable
	}
	current := s.snapshot.Load().registration
	if current.Revision != expectedRevision {
		return CommitResult{}, &ConflictError{Expected: expectedRevision, Actual: current.Revision, Global: true}
	}
	if next.Revision != expectedRevision {
		return CommitResult{}, &ConflictError{Expected: next.Revision, Actual: expectedRevision, Global: true}
	}
	next = normalizeRegistrationState(next)
	next.Revision = expectedRevision
	if reflect.DeepEqual(current, next) {
		return CommitResult{Revision: current.Revision, Durable: true}, nil
	}
	candidate := cloneSettingsFile(s.state)
	candidate.RegistrationRevision = current.Revision + 1
	candidate.OwnerID = next.OwnerID
	candidate.OwnerClaimNonce = next.OwnerClaimNonce
	candidate.OwnerClaimExpiresAt = next.OwnerClaimExpiresAt
	candidate.ControlGroupID = next.ControlGroupID
	candidate.RegisteredGroups = append([]RegisteredGroup(nil), next.RegisteredGroups...)
	candidate.EnrollmentNonces = append([]EnrollmentNonce(nil), next.EnrollmentNonces...)
	candidate.PendingRegistrations = append([]PendingRegistration(nil), next.PendingRegistrations...)
	candidate.UnknownGroupLeaves = append([]UnknownGroupLeave(nil), next.UnknownGroupLeaves...)

	keep := make(map[int64]bool, len(s.baseline.Groups)+len(next.RegisteredGroups))
	for _, group := range s.baseline.Groups {
		keep[group.ID] = true
	}
	for _, group := range next.RegisteredGroups {
		keep[group.ID] = true
	}
	for groupID := range candidate.Groups {
		if !keep[groupID] {
			delete(candidate.Groups, groupID)
		}
	}
	snap, err := s.buildSnapshot(candidate)
	if err != nil {
		return CommitResult{}, err
	}
	if err := s.writeState(&candidate); err != nil {
		s.setLastError(err)
		return CommitResult{}, err
	}
	s.state = candidate
	s.snapshot.Store(snap)
	s.setLastError(nil)
	return CommitResult{Revision: candidate.RegistrationRevision, Durable: true}, nil
}

// EnsureOwnerClaim returns the current unexpired claim nonce or durably creates a replacement.
func (s *Settings) EnsureOwnerClaim(now time.Time, lifetime time.Duration) (nonce string, created bool, err error) {
	if lifetime <= 0 {
		return "", false, fmt.Errorf("owner claim lifetime must be positive")
	}
	for {
		current := s.Registrations()
		if current.OwnerID != 0 {
			return "", false, nil
		}
		if current.OwnerClaimNonce != "" && now.Unix() < current.OwnerClaimExpiresAt {
			return current.OwnerClaimNonce, false, nil
		}
		nonce, err = randomRegistrationNonce()
		if err != nil {
			return "", false, err
		}
		next := cloneRegistrationState(current)
		next.OwnerClaimNonce = nonce
		next.OwnerClaimExpiresAt = now.Add(lifetime).Unix()
		if _, err = s.CommitRegistrations(current.Revision, next); errors.Is(err, ErrSettingsConflict) {
			continue
		}
		return nonce, err == nil, err
	}
}

// ClaimOwner atomically consumes one unexpired owner claim and binds the first owner.
func (s *Settings) ClaimOwner(userID int64, nonce string, now time.Time) error {
	if userID <= 0 {
		return ErrOwnerClaimInvalid
	}
	for {
		current := s.Registrations()
		if current.OwnerID != 0 || current.OwnerClaimNonce == "" ||
			current.OwnerClaimNonce != nonce || !now.Before(time.Unix(current.OwnerClaimExpiresAt, 0)) {
			return ErrOwnerClaimInvalid
		}
		next := cloneRegistrationState(current)
		next.OwnerID = userID
		next.OwnerClaimNonce = ""
		next.OwnerClaimExpiresAt = 0
		if _, err := s.CommitRegistrations(current.Revision, next); errors.Is(err, ErrSettingsConflict) {
			continue
		} else {
			return err
		}
	}
}

// IssueEnrollmentNonce durably creates one owner-authorized, single-use registration capability.
func (s *Settings) IssueEnrollmentNonce(ownerID int64, now time.Time, lifetime time.Duration) (EnrollmentNonce, error) {
	if lifetime <= 0 {
		return EnrollmentNonce{}, fmt.Errorf("enrollment lifetime must be positive")
	}
	for {
		current := s.Registrations()
		if current.OwnerID == 0 || current.OwnerID != ownerID {
			return EnrollmentNonce{}, ErrRegistrationOwnerOnly
		}
		nonce, err := randomRegistrationNonce()
		if err != nil {
			return EnrollmentNonce{}, err
		}
		issued := EnrollmentNonce{Nonce: nonce, IssuedBy: ownerID, ExpiresAt: now.Add(lifetime).Unix()}
		next := cloneRegistrationState(current)
		next.EnrollmentNonces = next.EnrollmentNonces[:0]
		for _, existing := range current.EnrollmentNonces {
			if now.Unix() < existing.ExpiresAt {
				next.EnrollmentNonces = append(next.EnrollmentNonces, existing)
			}
		}
		next.EnrollmentNonces = append(next.EnrollmentNonces, issued)
		if _, err = s.CommitRegistrations(current.Revision, next); errors.Is(err, ErrSettingsConflict) {
			continue
		} else if err != nil {
			return EnrollmentNonce{}, err
		}
		return issued, nil
	}
}

func (v GroupView) ID() int64                   { return v.group.id }
func (v GroupView) Revision() uint64            { return v.group.revision }
func (v GroupView) RuntimeRegistered() bool     { return v.group.registered }
func (v GroupView) Enabled() Setting[bool]      { return v.group.enabled }
func (v GroupView) DMFirst() Setting[bool]      { return v.group.dmFirst }
func (v GroupView) VerifyMode() Setting[string] { return v.group.verifyMode }
func (v GroupView) NameSpoiler() Setting[bool]  { return v.group.nameSpoiler }
func (v GroupView) BanSeconds() Setting[int]    { return v.group.banSeconds }
func (v GroupView) LookupTTLSeconds() Setting[int] {
	return v.group.lookupTTLSeconds
}
func (v GroupView) LookupAutoDeleteEnabled() Setting[bool] {
	return v.group.lookupAutoDeleteEnabled
}
func (v GroupView) TimeoutSeconds() Setting[int]      { return v.group.timeoutSeconds }
func (v GroupView) VerifyMaxFails() Setting[int]      { return v.group.verifyMaxFails }
func (v GroupView) VerifyRetrySeconds() Setting[int]  { return v.group.verifyRetrySeconds }
func (v GroupView) AntispamEnabled() Setting[bool]    { return v.group.antispamEnabled }
func (v GroupView) ChannelDisplay() Setting[string]   { return v.group.channelDisplay }
func (v GroupView) ChannelInviteURL() Setting[string] { return v.group.channelInviteURL }
func (v GroupView) FallbackBuiltin() Setting[bool]    { return v.group.fallbackBuiltin }
func (v GroupView) Lang() Setting[string]             { return v.group.lang }
func (v GroupView) RequiredChannelID() Setting[int64] { return v.group.requiredChannelID }

// Baseline returns a detached copy of the configured/default group baseline.
func (v GroupView) Baseline() GroupBaseline { return cloneGroupBaseline(v.group.baseline) }

// Overrides returns a detached copy suitable for editing and CommitGroup.
func (v GroupView) Overrides() GroupOverrides { return cloneGroupOverrides(v.group.overrides) }

func (v GroupView) ChannelWhitelist() Setting[[]int64] {
	return Setting[[]int64]{Value: cloneInt64s(v.group.channelWhitelist.Value), Source: v.group.channelWhitelist.Source}
}

func (v GroupView) TrustedMemberGroupIDs() Setting[[]int64] {
	return Setting[[]int64]{Value: cloneInt64s(v.group.trustedMemberGroupIDs.Value), Source: v.group.trustedMemberGroupIDs.Source}
}

func (v GroupView) KnownChatIDs() Setting[[]int64] {
	return Setting[[]int64]{Value: cloneInt64s(v.group.knownChatIDs.Value), Source: v.group.knownChatIDs.Source}
}

func (v GroupView) Questions() Setting[[]config.Question] {
	return Setting[[]config.Question]{Value: cloneQuestions(v.group.questions.Value), Source: v.group.questions.Source}
}

func (v GroupView) FallbackQuestions() Setting[[]config.ShortQuestion] {
	return Setting[[]config.ShortQuestion]{Value: cloneShortQuestions(v.group.fallbackQuestions.Value), Source: v.group.fallbackQuestions.Source}
}

func (v GlobalView) Revision() uint64 { return v.global.revision }
func (v GlobalView) RichMessages() Setting[bool] {
	return v.global.richMessages
}
func (v GlobalView) PrivateQueryPerMin() Setting[int] {
	return v.global.privateQueryPerMin
}
func (v GlobalView) Overrides() GlobalOverrides {
	return cloneGlobalOverrides(v.global.overrides)
}

func (s *Settings) newState() settingsFile {
	return settingsFile{
		Version:              SettingsSchemaVersion,
		ControlGroupID:       s.baseline.ControlGroupID,
		RegisteredGroups:     []RegisteredGroup{},
		EnrollmentNonces:     []EnrollmentNonce{},
		PendingRegistrations: []PendingRegistration{},
		UnknownGroupLeaves:   []UnknownGroupLeave{},
		Groups:               make(map[int64]groupRecord),
	}
}

func (s *Settings) migrateLegacy(legacy settingsFile) settingsFile {
	migrated := s.newState()
	for _, group := range s.baseline.Groups {
		var overrides GroupOverrides
		if legacy.Enabled != nil {
			overrides.Enabled = boolPtr(*legacy.Enabled)
		}
		if legacy.NameSpoiler != nil {
			overrides.NameSpoiler = boolPtr(*legacy.NameSpoiler)
		}
		if config.ValidMode(legacy.VerifyMode) {
			overrides.VerifyMode = stringPtr(legacy.VerifyMode)
		}
		if !emptyGroupOverrides(overrides) {
			migrated.Groups[group.ID] = groupRecord{Revision: 1, GroupOverrides: overrides}
		}
	}
	return migrated
}

func migrateVersionOne(state settingsFile) settingsFile {
	migrated := normalizeFile(state)
	migrated.Version = SettingsSchemaVersion
	for groupID, record := range migrated.Groups {
		if record.LookupTTLSeconds == nil {
			continue
		}
		enabled := *record.LookupTTLSeconds > 0
		record.LookupAutoDeleteEnabled = boolPtr(enabled)
		if !enabled {
			record.LookupTTLSeconds = nil
		}
		migrated.Groups[groupID] = record
	}
	return migrated
}

func loadLegacyAntispam(path string) (legacyAntispamState, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return legacyAntispamState{}, false, nil
		}
		return legacyAntispamState{}, false, err
	}
	var legacy legacyAntispamState
	if err := json.Unmarshal(data, &legacy); err != nil {
		return legacyAntispamState{}, false, err
	}
	return legacy, true, nil
}

func (s *Settings) migrateLegacyAntispam(state settingsFile, legacy legacyAntispamState) settingsFile {
	migrated := cloneSettingsFile(state)
	apply := func(groupID int64) {
		record := migrated.Groups[groupID]
		if record.Revision == 0 {
			record.Revision = 1
		}
		record.AntispamEnabled = boolPtr(legacy.Enabled)
		record.ChannelWhitelist = cloneSlicePtr(&legacy.Whitelist, cloneInt64s)
		migrated.Groups[groupID] = record
	}
	for _, group := range s.baseline.Groups {
		apply(group.ID)
	}
	for _, group := range migrated.RegisteredGroups {
		apply(group.ID)
	}
	return migrated
}

func (s *Settings) buildSnapshot(state settingsFile) (*settingsSnapshot, error) {
	registration := RegistrationState{
		Revision:             state.RegistrationRevision,
		OwnerID:              state.OwnerID,
		OwnerClaimNonce:      state.OwnerClaimNonce,
		OwnerClaimExpiresAt:  state.OwnerClaimExpiresAt,
		ControlGroupID:       state.ControlGroupID,
		RegisteredGroups:     append([]RegisteredGroup(nil), state.RegisteredGroups...),
		EnrollmentNonces:     append([]EnrollmentNonce(nil), state.EnrollmentNonces...),
		PendingRegistrations: append([]PendingRegistration(nil), state.PendingRegistrations...),
		UnknownGroupLeaves:   append([]UnknownGroupLeave(nil), state.UnknownGroupLeaves...),
	}
	if err := s.validateRegistrations(registration); err != nil {
		return nil, err
	}

	groups := make(map[int64]*effectiveGroup, len(s.baseline.Groups)+len(state.RegisteredGroups))
	order := make([]int64, 0, len(s.baseline.Groups)+len(state.RegisteredGroups))
	for _, baseline := range s.baseline.Groups {
		record := state.Groups[baseline.ID]
		group := buildEffectiveGroup(baseline, record, false)
		if err := validateEffectiveGroup(group); err != nil {
			return nil, fmt.Errorf("group %d: %w", baseline.ID, err)
		}
		groups[baseline.ID] = group
		order = append(order, baseline.ID)
	}
	for _, registered := range state.RegisteredGroups {
		baseline := cloneGroupBaseline(s.baseline.DefaultGroup)
		baseline.ID = registered.ID
		record := state.Groups[registered.ID]
		group := buildEffectiveGroup(baseline, record, true)
		if err := validateEffectiveGroup(group); err != nil {
			return nil, fmt.Errorf("group %d: %w", registered.ID, err)
		}
		groups[registered.ID] = group
		order = append(order, registered.ID)
	}
	if state.ControlGroupID != 0 {
		if _, ok := groups[state.ControlGroupID]; !ok {
			return nil, fmt.Errorf("control group %d is not effective", state.ControlGroupID)
		}
	}
	global := effectiveGlobal{
		revision:           state.Global.Revision,
		overrides:          cloneGlobalOverrides(state.Global.GlobalOverrides),
		richMessages:       resolve(state.Global.RichMessages, s.baseline.Global.RichMessages),
		privateQueryPerMin: resolve(state.Global.PrivateQueryPerMin, s.baseline.Global.PrivateQueryPerMin),
	}
	if global.privateQueryPerMin.Value <= 0 {
		return nil, fmt.Errorf("private query rate must be positive")
	}
	return &settingsSnapshot{groups: groups, groupIDs: order, global: global, registration: registration}, nil
}

func buildEffectiveGroup(baseline GroupBaseline, record groupRecord, registered bool) *effectiveGroup {
	builtin := resolve(record.FallbackBuiltin, baseline.FallbackBuiltin)
	fallback := resolveSlice(record.FallbackQuestions, baseline.FallbackQuestions, cloneShortQuestions)
	if builtin.Value {
		fallback.Value = []config.ShortQuestion{}
		if record.FallbackBuiltin != nil {
			fallback.Source = SourceRuntime
		} else if baseline.FallbackBuiltin.Value {
			fallback.Source = baseline.FallbackBuiltin.Source
		}
	}
	return &effectiveGroup{
		id:               baseline.ID,
		revision:         record.Revision,
		registered:       registered,
		baseline:         cloneGroupBaseline(baseline),
		overrides:        cloneGroupOverrides(record.GroupOverrides),
		enabled:          resolve(record.Enabled, baseline.Enabled),
		dmFirst:          resolve(record.DMFirst, baseline.DMFirst),
		verifyMode:       resolve(record.VerifyMode, baseline.VerifyMode),
		nameSpoiler:      resolve(record.NameSpoiler, baseline.NameSpoiler),
		banSeconds:       resolve(record.BanSeconds, baseline.BanSeconds),
		lookupTTLSeconds: resolve(record.LookupTTLSeconds, baseline.LookupTTLSeconds),
		lookupAutoDeleteEnabled: resolve(
			record.LookupAutoDeleteEnabled,
			baseline.LookupAutoDeleteEnabled,
		),
		timeoutSeconds:        resolve(record.TimeoutSeconds, baseline.TimeoutSeconds),
		verifyMaxFails:        resolve(record.VerifyMaxFails, baseline.VerifyMaxFails),
		verifyRetrySeconds:    resolve(record.VerifyRetrySeconds, baseline.VerifyRetrySeconds),
		antispamEnabled:       resolve(record.AntispamEnabled, baseline.AntispamEnabled),
		channelWhitelist:      resolveSlice(record.ChannelWhitelist, baseline.ChannelWhitelist, cloneInt64s),
		trustedMemberGroupIDs: resolveSlice(record.TrustedMemberGroupIDs, baseline.TrustedMemberGroupIDs, cloneInt64s),
		knownChatIDs:          resolveSlice(record.KnownChatIDs, baseline.KnownChatIDs, cloneInt64s),
		requiredChannelID:     resolve(record.RequiredChannelID, baseline.RequiredChannelID),
		channelDisplay:        resolve(record.ChannelDisplay, baseline.ChannelDisplay),
		channelInviteURL:      resolve(record.ChannelInviteURL, baseline.ChannelInviteURL),
		questions:             resolveSlice(record.Questions, baseline.Questions, cloneQuestions),
		fallbackQuestions:     fallback,
		fallbackBuiltin:       builtin,
		lang:                  resolve(record.Lang, baseline.Lang),
	}
}

func resolve[T any](override *T, baseline BaselineValue[T]) Setting[T] {
	if override != nil {
		return Setting[T]{Value: *override, Source: SourceRuntime}
	}
	return Setting[T](baseline)
}

func resolveSlice[T any](override *[]T, baseline BaselineValue[[]T], clone func([]T) []T) Setting[[]T] {
	if override != nil {
		return Setting[[]T]{Value: clone(*override), Source: SourceRuntime}
	}
	return Setting[[]T]{Value: clone(baseline.Value), Source: baseline.Source}
}

func normalizeBaselineLanguages(baseline *SettingsBaseline) {
	if baseline.DefaultGroup.Lang.Value == "" {
		baseline.DefaultGroup.Lang = BaselineValue[string]{Value: "zh", Source: SourceDefault}
	}
	for i := range baseline.Groups {
		if baseline.Groups[i].Lang.Value == "" {
			baseline.Groups[i].Lang = BaselineValue[string]{Value: "zh", Source: SourceDefault}
		}
	}
}

func validateBaseline(baseline SettingsBaseline) error {
	if err := validateBaselineSources(baseline.DefaultGroup); err != nil {
		return fmt.Errorf("default group: %w", err)
	}
	if err := validateEffectiveGroup(buildEffectiveGroup(baseline.DefaultGroup, groupRecord{}, false)); err != nil {
		return fmt.Errorf("default group: %w", err)
	}
	seen := make(map[int64]bool, len(baseline.Groups))
	for _, group := range baseline.Groups {
		if group.ID == 0 {
			return fmt.Errorf("settings baseline group ID 0 is invalid")
		}
		if seen[group.ID] {
			return fmt.Errorf("duplicate settings baseline group %d", group.ID)
		}
		seen[group.ID] = true
		if err := validateBaselineSources(group); err != nil {
			return fmt.Errorf("group %d: %w", group.ID, err)
		}
		if err := validateEffectiveGroup(buildEffectiveGroup(group, groupRecord{}, false)); err != nil {
			return fmt.Errorf("group %d: %w", group.ID, err)
		}
	}
	if baseline.ControlGroupID != 0 && !seen[baseline.ControlGroupID] {
		return fmt.Errorf("control group %d is not in the baseline", baseline.ControlGroupID)
	}
	if baseline.Global.PrivateQueryPerMin.Value <= 0 {
		return fmt.Errorf("private query rate must be positive")
	}
	return nil
}

func validateBaselineSources(group GroupBaseline) error {
	sources := []Source{
		group.Enabled.Source, group.DMFirst.Source, group.VerifyMode.Source, group.NameSpoiler.Source,
		group.BanSeconds.Source, group.LookupTTLSeconds.Source, group.LookupAutoDeleteEnabled.Source,
		group.TimeoutSeconds.Source, group.VerifyMaxFails.Source, group.VerifyRetrySeconds.Source,
		group.AntispamEnabled.Source, group.ChannelWhitelist.Source,
		group.TrustedMemberGroupIDs.Source, group.KnownChatIDs.Source, group.RequiredChannelID.Source,
		group.ChannelDisplay.Source, group.ChannelInviteURL.Source, group.Questions.Source,
		group.FallbackQuestions.Source, group.FallbackBuiltin.Source, group.Lang.Source,
	}
	for _, source := range sources {
		if source != SourceDefault && source != SourceConfig {
			return fmt.Errorf("baseline source %d is not config or default", source)
		}
	}
	return nil
}

func validateEffectiveGroup(group *effectiveGroup) error {
	if !config.ValidMode(group.verifyMode.Value) {
		return fmt.Errorf("invalid verify mode %q", group.verifyMode.Value)
	}
	if group.lang.Value == "" || !config.ValidLanguage(group.lang.Value) {
		return fmt.Errorf("invalid language %q", group.lang.Value)
	}
	if group.banSeconds.Source == SourceRuntime && group.banSeconds.Value != config.ClampBanSeconds(group.banSeconds.Value) {
		return fmt.Errorf("ban_seconds %d is outside Telegram's supported range", group.banSeconds.Value)
	}
	if group.lookupTTLSeconds.Source == SourceRuntime && group.lookupTTLSeconds.Value <= 0 {
		return fmt.Errorf("lookup_ttl_seconds must be positive")
	}
	if group.timeoutSeconds.Source == SourceRuntime && (group.timeoutSeconds.Value < 30 || group.timeoutSeconds.Value > 1800) {
		return fmt.Errorf("timeout_seconds must be between 30 and 1800")
	}
	if group.channelWhitelist.Source == SourceRuntime {
		if err := validateIDs("channel_whitelist", group.channelWhitelist.Value); err != nil {
			return err
		}
	}
	if group.trustedMemberGroupIDs.Source == SourceRuntime {
		if err := validateIDs("trusted_member_group_ids", group.trustedMemberGroupIDs.Value); err != nil {
			return err
		}
	}
	if group.knownChatIDs.Source == SourceRuntime {
		if err := validateIDs("known_chat_ids", group.knownChatIDs.Value); err != nil {
			return err
		}
	}
	if group.requiredChannelID.Source == SourceRuntime || group.channelDisplay.Source == SourceRuntime || group.channelInviteURL.Source == SourceRuntime {
		if group.requiredChannelID.Value != 0 && group.channelInviteURL.Value == "" && !strings.HasPrefix(group.channelDisplay.Value, "@") {
			return fmt.Errorf("required channel has no reachable display or invite URL")
		}
	}
	if group.questions.Source == SourceRuntime {
		if err := validateQuestions(group.questions.Value); err != nil {
			return err
		}
	}
	if group.fallbackBuiltin.Value {
		if group.overrides.FallbackQuestions != nil {
			return fmt.Errorf("fallback_questions cannot be overridden while fallback_builtin is true")
		}
	} else if err := validateFallbackQuestions(group.fallbackQuestions.Value); err != nil {
		return err
	}
	return nil
}

func validateQuestions(questions []config.Question) error {
	for i, question := range questions {
		if strings.TrimSpace(question.Q) == "" {
			return fmt.Errorf("question %d has an empty prompt", i)
		}
		if len(question.Options) < 2 {
			return fmt.Errorf("question %d needs at least two options", i)
		}
		if question.Answer < 0 || question.Answer >= len(question.Options) {
			return fmt.Errorf("question %d answer index %d is out of range", i, question.Answer)
		}
	}
	return nil
}

func validateFallbackQuestions(questions []config.ShortQuestion) error {
	if len(questions) == 0 {
		return fmt.Errorf("custom fallback questions cannot be empty")
	}
	for i, question := range questions {
		if strings.TrimSpace(question.Q) == "" || len(question.Answers) == 0 {
			return fmt.Errorf("fallback question %d needs a prompt and answers", i)
		}
		for _, answer := range question.Answers {
			if strings.TrimSpace(answer) == "" {
				return fmt.Errorf("fallback question %d has an empty answer", i)
			}
		}
	}
	return nil
}

func validateIDs(name string, ids []int64) error {
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id == 0 {
			return fmt.Errorf("%s contains ID 0", name)
		}
		if seen[id] {
			return fmt.Errorf("%s contains duplicate ID %d", name, id)
		}
		seen[id] = true
	}
	return nil
}

func (s *Settings) validateRegistrations(state RegistrationState) error {
	if state.OwnerID < 0 {
		return fmt.Errorf("owner ID must not be negative")
	}
	if (state.OwnerClaimNonce == "") != (state.OwnerClaimExpiresAt == 0) {
		return fmt.Errorf("owner claim nonce and expiry must be set together")
	}
	if state.OwnerClaimNonce != "" && state.OwnerClaimExpiresAt <= 0 {
		return fmt.Errorf("owner claim expiry must be positive")
	}
	if state.OwnerID != 0 && state.OwnerClaimNonce != "" {
		return fmt.Errorf("owner claim must be cleared after ownership is claimed")
	}
	seenGroups := make(map[int64]bool, len(s.baseline.Groups)+len(state.RegisteredGroups))
	for _, group := range s.baseline.Groups {
		seenGroups[group.ID] = true
	}
	for _, group := range state.RegisteredGroups {
		if group.ID == 0 {
			return fmt.Errorf("registered group ID 0 is invalid")
		}
		if group.RegisteredBy <= 0 {
			return fmt.Errorf("registered group %d has no valid actor", group.ID)
		}
		if seenGroups[group.ID] {
			return fmt.Errorf("duplicate registered group %d", group.ID)
		}
		seenGroups[group.ID] = true
	}
	seenNonces := make(map[string]bool, len(state.EnrollmentNonces))
	for _, nonce := range state.EnrollmentNonces {
		if nonce.Nonce == "" || seenNonces[nonce.Nonce] {
			return fmt.Errorf("enrollment nonce is empty or duplicated")
		}
		if nonce.IssuedBy == 0 || nonce.ExpiresAt <= 0 {
			return fmt.Errorf("enrollment nonce issuer and expiry are required")
		}
		seenNonces[nonce.Nonce] = true
	}
	seenPending := make(map[int64]bool, len(state.PendingRegistrations))
	for _, pending := range state.PendingRegistrations {
		if pending.GroupID == 0 || seenPending[pending.GroupID] {
			return fmt.Errorf("pending registration group is zero or duplicated")
		}
		if pending.RegisteredBy == 0 || pending.ExpiresAt <= 0 {
			return fmt.Errorf("pending registration actor and expiry are required")
		}
		seenPending[pending.GroupID] = true
	}
	seenLeaves := make(map[int64]bool, len(state.UnknownGroupLeaves))
	for _, leave := range state.UnknownGroupLeaves {
		if leave.GroupID == 0 || seenLeaves[leave.GroupID] {
			return fmt.Errorf("unknown-group leave group is zero or duplicated")
		}
		if leave.ExpiresAt <= 0 {
			return fmt.Errorf("unknown-group leave %d has no valid expiry", leave.GroupID)
		}
		if seenPending[leave.GroupID] {
			return fmt.Errorf("group %d cannot be pending registration and unknown-group cleanup", leave.GroupID)
		}
		seenLeaves[leave.GroupID] = true
	}
	if state.ControlGroupID != 0 && !seenGroups[state.ControlGroupID] {
		return fmt.Errorf("control group %d is not configured or registered", state.ControlGroupID)
	}
	return nil
}

func (s *Settings) writeState(state *settingsFile) error {
	if s.path == "" {
		return nil
	}
	candidate := cloneSettingsFile(*state)
	snap, err := s.buildSnapshot(candidate)
	if err != nil {
		return err
	}
	groupID := candidate.ControlGroupID
	if groupID == 0 && len(snap.groupIDs) > 0 {
		groupID = snap.groupIDs[0]
	}
	if groupID == 0 {
		candidate.Enabled = nil
		candidate.NameSpoiler = nil
		candidate.VerifyMode = ""
	} else {
		group := snap.groups[groupID]
		candidate.Enabled = boolPtr(group.enabled.Value)
		candidate.NameSpoiler = boolPtr(group.nameSpoiler.Value)
		candidate.VerifyMode = group.verifyMode.Value
	}
	candidate.Version = SettingsSchemaVersion
	if err := Write(s.path, candidate); err != nil {
		return err
	}
	state.Enabled = candidate.Enabled
	state.NameSpoiler = candidate.NameSpoiler
	state.VerifyMode = candidate.VerifyMode
	state.Version = candidate.Version
	return nil
}

func (s *Settings) unavailableError() error {
	if current := s.lastError.Load(); current != nil {
		return fmt.Errorf("%w: %v", ErrSettingsUnavailable, current.err)
	}
	return ErrSettingsUnavailable
}

func (s *Settings) setLastError(err error) {
	if err == nil {
		s.lastError.Store(nil)
		return
	}
	s.lastError.Store(&statusError{err: err})
}

func normalizeFile(state settingsFile) settingsFile {
	state.RegisteredGroups = nonNilRegisteredGroups(state.RegisteredGroups)
	state.EnrollmentNonces = nonNilEnrollmentNonces(state.EnrollmentNonces)
	state.PendingRegistrations = nonNilPendingRegistrations(state.PendingRegistrations)
	state.UnknownGroupLeaves = nonNilUnknownGroupLeaves(state.UnknownGroupLeaves)
	if state.Groups == nil {
		state.Groups = make(map[int64]groupRecord)
	}
	return state
}

func normalizeRegistrationState(state RegistrationState) RegistrationState {
	state.RegisteredGroups = nonNilRegisteredGroups(state.RegisteredGroups)
	state.EnrollmentNonces = nonNilEnrollmentNonces(state.EnrollmentNonces)
	state.PendingRegistrations = nonNilPendingRegistrations(state.PendingRegistrations)
	state.UnknownGroupLeaves = nonNilUnknownGroupLeaves(state.UnknownGroupLeaves)
	return state
}

func nonNilRegisteredGroups(values []RegisteredGroup) []RegisteredGroup {
	if values == nil {
		return []RegisteredGroup{}
	}
	return append([]RegisteredGroup(nil), values...)
}

func nonNilEnrollmentNonces(values []EnrollmentNonce) []EnrollmentNonce {
	if values == nil {
		return []EnrollmentNonce{}
	}
	return append([]EnrollmentNonce(nil), values...)
}

func nonNilPendingRegistrations(values []PendingRegistration) []PendingRegistration {
	if values == nil {
		return []PendingRegistration{}
	}
	return append([]PendingRegistration(nil), values...)
}

func nonNilUnknownGroupLeaves(values []UnknownGroupLeave) []UnknownGroupLeave {
	if values == nil {
		return []UnknownGroupLeave{}
	}
	return append([]UnknownGroupLeave(nil), values...)
}

func cloneSettingsBaseline(value SettingsBaseline) SettingsBaseline {
	out := value
	out.Groups = make([]GroupBaseline, len(value.Groups))
	for i := range value.Groups {
		out.Groups[i] = cloneGroupBaseline(value.Groups[i])
	}
	out.DefaultGroup = cloneGroupBaseline(value.DefaultGroup)
	return out
}

func cloneGroupBaseline(value GroupBaseline) GroupBaseline {
	out := value
	out.ChannelWhitelist.Value = cloneInt64s(value.ChannelWhitelist.Value)
	out.TrustedMemberGroupIDs.Value = cloneInt64s(value.TrustedMemberGroupIDs.Value)
	out.KnownChatIDs.Value = cloneInt64s(value.KnownChatIDs.Value)
	out.Questions.Value = cloneQuestions(value.Questions.Value)
	out.FallbackQuestions.Value = cloneShortQuestions(value.FallbackQuestions.Value)
	return out
}

func cloneSettingsFile(value settingsFile) settingsFile {
	out := value
	out.Enabled = clonePtr(value.Enabled)
	out.NameSpoiler = clonePtr(value.NameSpoiler)
	out.RegisteredGroups = nonNilRegisteredGroups(value.RegisteredGroups)
	out.EnrollmentNonces = nonNilEnrollmentNonces(value.EnrollmentNonces)
	out.PendingRegistrations = nonNilPendingRegistrations(value.PendingRegistrations)
	out.UnknownGroupLeaves = nonNilUnknownGroupLeaves(value.UnknownGroupLeaves)
	out.Global.GlobalOverrides = cloneGlobalOverrides(value.Global.GlobalOverrides)
	out.Groups = make(map[int64]groupRecord, len(value.Groups))
	for id, record := range value.Groups {
		record.GroupOverrides = cloneGroupOverrides(record.GroupOverrides)
		out.Groups[id] = record
	}
	return out
}

func cloneRegistrationState(value RegistrationState) RegistrationState {
	out := value
	out.RegisteredGroups = nonNilRegisteredGroups(value.RegisteredGroups)
	out.EnrollmentNonces = nonNilEnrollmentNonces(value.EnrollmentNonces)
	out.PendingRegistrations = nonNilPendingRegistrations(value.PendingRegistrations)
	out.UnknownGroupLeaves = nonNilUnknownGroupLeaves(value.UnknownGroupLeaves)
	return out
}

func cloneGroupOverrides(value GroupOverrides) GroupOverrides {
	out := value
	out.Enabled = clonePtr(value.Enabled)
	out.DMFirst = clonePtr(value.DMFirst)
	out.VerifyMode = clonePtr(value.VerifyMode)
	out.NameSpoiler = clonePtr(value.NameSpoiler)
	out.BanSeconds = clonePtr(value.BanSeconds)
	out.LookupTTLSeconds = clonePtr(value.LookupTTLSeconds)
	out.LookupAutoDeleteEnabled = clonePtr(value.LookupAutoDeleteEnabled)
	out.TimeoutSeconds = clonePtr(value.TimeoutSeconds)
	out.VerifyMaxFails = clonePtr(value.VerifyMaxFails)
	out.VerifyRetrySeconds = clonePtr(value.VerifyRetrySeconds)
	out.AntispamEnabled = clonePtr(value.AntispamEnabled)
	out.ChannelWhitelist = cloneSlicePtr(value.ChannelWhitelist, cloneInt64s)
	out.TrustedMemberGroupIDs = cloneSlicePtr(value.TrustedMemberGroupIDs, cloneInt64s)
	out.KnownChatIDs = cloneSlicePtr(value.KnownChatIDs, cloneInt64s)
	out.RequiredChannelID = clonePtr(value.RequiredChannelID)
	out.ChannelDisplay = clonePtr(value.ChannelDisplay)
	out.ChannelInviteURL = clonePtr(value.ChannelInviteURL)
	out.Questions = cloneSlicePtr(value.Questions, cloneQuestions)
	out.FallbackQuestions = cloneSlicePtr(value.FallbackQuestions, cloneShortQuestions)
	out.FallbackBuiltin = clonePtr(value.FallbackBuiltin)
	out.Lang = clonePtr(value.Lang)
	return out
}

func cloneGlobalOverrides(value GlobalOverrides) GlobalOverrides {
	return GlobalOverrides{
		RichMessages:       clonePtr(value.RichMessages),
		PrivateQueryPerMin: clonePtr(value.PrivateQueryPerMin),
	}
}

func compactGroupOverrides(value GroupOverrides, baseline GroupBaseline) GroupOverrides {
	value.Enabled = omitBaseline(value.Enabled, baseline.Enabled.Value)
	value.DMFirst = omitBaseline(value.DMFirst, baseline.DMFirst.Value)
	value.VerifyMode = omitBaseline(value.VerifyMode, baseline.VerifyMode.Value)
	value.NameSpoiler = omitBaseline(value.NameSpoiler, baseline.NameSpoiler.Value)
	value.BanSeconds = omitBaseline(value.BanSeconds, baseline.BanSeconds.Value)
	value.LookupTTLSeconds = omitBaseline(value.LookupTTLSeconds, baseline.LookupTTLSeconds.Value)
	value.LookupAutoDeleteEnabled = omitBaseline(value.LookupAutoDeleteEnabled, baseline.LookupAutoDeleteEnabled.Value)
	value.TimeoutSeconds = omitBaseline(value.TimeoutSeconds, baseline.TimeoutSeconds.Value)
	value.VerifyMaxFails = omitBaseline(value.VerifyMaxFails, baseline.VerifyMaxFails.Value)
	value.VerifyRetrySeconds = omitBaseline(value.VerifyRetrySeconds, baseline.VerifyRetrySeconds.Value)
	value.AntispamEnabled = omitBaseline(value.AntispamEnabled, baseline.AntispamEnabled.Value)
	value.ChannelWhitelist = omitBaseline(value.ChannelWhitelist, baseline.ChannelWhitelist.Value)
	value.TrustedMemberGroupIDs = omitBaseline(value.TrustedMemberGroupIDs, baseline.TrustedMemberGroupIDs.Value)
	value.KnownChatIDs = omitBaseline(value.KnownChatIDs, baseline.KnownChatIDs.Value)
	value.RequiredChannelID = omitBaseline(value.RequiredChannelID, baseline.RequiredChannelID.Value)
	value.ChannelDisplay = omitBaseline(value.ChannelDisplay, baseline.ChannelDisplay.Value)
	value.ChannelInviteURL = omitBaseline(value.ChannelInviteURL, baseline.ChannelInviteURL.Value)
	value.Questions = omitBaseline(value.Questions, baseline.Questions.Value)
	value.FallbackQuestions = omitBaseline(value.FallbackQuestions, baseline.FallbackQuestions.Value)
	value.FallbackBuiltin = omitBaseline(value.FallbackBuiltin, baseline.FallbackBuiltin.Value)
	value.Lang = omitBaseline(value.Lang, baseline.Lang.Value)
	return value
}

func compactGlobalOverrides(value GlobalOverrides, baseline GlobalBaseline) GlobalOverrides {
	value.RichMessages = omitBaseline(value.RichMessages, baseline.RichMessages.Value)
	value.PrivateQueryPerMin = omitBaseline(value.PrivateQueryPerMin, baseline.PrivateQueryPerMin.Value)
	return value
}

func omitBaseline[T any](value *T, baseline T) *T {
	if value != nil && reflect.DeepEqual(*value, baseline) {
		return nil
	}
	return value
}

func clonePtr[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneSlicePtr[T any](value *[]T, clone func([]T) []T) *[]T {
	if value == nil {
		return nil
	}
	cloned := clone(*value)
	return &cloned
}

func cloneInt64s(values []int64) []int64 {
	if values == nil {
		return []int64{}
	}
	out := make([]int64, len(values))
	copy(out, values)
	return out
}

func cloneQuestions(values []config.Question) []config.Question {
	if values == nil {
		return []config.Question{}
	}
	out := make([]config.Question, len(values))
	for i := range values {
		out[i] = values[i]
		out[i].Options = append([]string(nil), values[i].Options...)
	}
	return out
}

func cloneShortQuestions(values []config.ShortQuestion) []config.ShortQuestion {
	if values == nil {
		return []config.ShortQuestion{}
	}
	out := make([]config.ShortQuestion, len(values))
	for i := range values {
		out[i] = values[i]
		out[i].Answers = append([]string(nil), values[i].Answers...)
	}
	return out
}

func emptyGroupOverrides(value GroupOverrides) bool {
	return reflect.DeepEqual(value, GroupOverrides{})
}

func boolPtr(value bool) *bool       { return &value }
func stringPtr(value string) *string { return &value }

func randomRegistrationNonce() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate registration nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}
