package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
)

const (
	ownerClaimLifetime  = 10 * time.Minute
	enrollmentLifetime  = 10 * time.Minute
	registrationPending = 10 * time.Minute
	setupReportCooldown = 10 * time.Minute
)

var (
	errEnrollmentInvalid       = errors.New("enrollment nonce is invalid")
	errBotMembershipUnreadable = errors.New("bot membership is unreadable")
	errBotMembershipIneligible = errors.New("bot is not an eligible group member")
	errRuntimeGroupNotFound    = errors.New("runtime-registered group was not found")
	errUnknownLeaveSuperseded  = errors.New("unknown-group cleanup was superseded by registration")
)

type botMembershipState uint8

const (
	botMembershipIneligible botMembershipState = iota
	botMembershipMember
	botMembershipAdmin
)

type registrationTransition struct {
	mu   sync.Mutex
	refs int
}

type registrationTransitionLocks struct {
	mu     sync.Mutex
	groups map[int64]*registrationTransition
}

func (l *registrationTransitionLocks) lock(groupID int64) func() {
	l.mu.Lock()
	if l.groups == nil {
		l.groups = make(map[int64]*registrationTransition)
	}
	transition := l.groups[groupID]
	if transition == nil {
		transition = &registrationTransition{}
		l.groups[groupID] = transition
	}
	transition.refs++
	l.mu.Unlock()

	transition.mu.Lock()
	return func() {
		transition.mu.Unlock()
		l.mu.Lock()
		transition.refs--
		if transition.refs == 0 {
			delete(l.groups, groupID)
		}
		l.mu.Unlock()
	}
}

type registrationService struct {
	root                context.Context
	bot                 *telego.Bot
	settings            *store.Settings
	cfg                 *config.Config
	username            string
	selfID              int64
	now                 func() time.Time
	onRegistered        func(context.Context, int64)
	onMembershipChanged func(context.Context, int64)

	transitions registrationTransitionLocks
	waitingMu   sync.Mutex
	waiting     map[int64]time.Time
	reportMu    sync.Mutex
	reportAfter map[int64]time.Time
}

func newRegistrationService(
	root context.Context,
	bot *telego.Bot,
	settings *store.Settings,
	cfg *config.Config,
	username string,
	selfID int64,
	onRegistered func(context.Context, int64),
	onMembershipChanged func(context.Context, int64),
) *registrationService {
	s := &registrationService{
		root:                root,
		bot:                 bot,
		settings:            settings,
		cfg:                 cfg,
		username:            username,
		selfID:              selfID,
		now:                 time.Now,
		onRegistered:        onRegistered,
		onMembershipChanged: onMembershipChanged,
		waiting:             make(map[int64]time.Time),
		reportAfter:         make(map[int64]time.Time),
	}
	state := settings.Registrations()
	for _, pending := range state.PendingRegistrations {
		s.scheduleUnknownLeave(pending.GroupID, pending.Title, time.Unix(pending.ExpiresAt, 0))
	}
	for _, leave := range state.UnknownGroupLeaves {
		s.scheduleUnknownLeave(leave.GroupID, leave.Title, time.Unix(leave.ExpiresAt, 0))
	}
	return s
}

func (s *registrationService) Register(handler *th.BotHandler) {
	handler.Handle(s.onOwnerClaim, th.And(th.CommandEqual("start"), startPayloadPrefix("owner_"), privateMessage))
	handler.Handle(s.onEnrollmentStart, th.And(th.CommandEqual("start"), startPayloadPrefix("enroll_")))
	handler.Handle(s.onEnrollmentCommand, th.And(th.CommandEqual("enroll"), privateMessage))
	handler.Handle(s.onUnregisterCommand, th.And(th.CommandEqual("unregister"), privateMessage))
	handler.Handle(s.onMyChatMember, s.registrationMembershipUpdate)
	handler.Handle(s.onEffectiveMembershipUpdate, s.effectiveMembershipUpdate)
}

func privateMessage(_ context.Context, update telego.Update) bool {
	return update.Message != nil && update.Message.Chat.Type == telego.ChatTypePrivate
}

func startPayloadPrefix(prefix string) th.Predicate {
	return func(_ context.Context, update telego.Update) bool {
		return strings.HasPrefix(startPayload(update.Message), prefix)
	}
}

func startPayload(message *telego.Message) string {
	if message == nil {
		return ""
	}
	fields := strings.Fields(message.Text)
	if len(fields) != 2 {
		return ""
	}
	command := strings.TrimPrefix(strings.ToLower(fields[0]), "/")
	if at := strings.IndexByte(command, '@'); at >= 0 {
		command = command[:at]
	}
	if command != "start" {
		return ""
	}
	return fields[1]
}

func commandArguments(message *telego.Message, expected string) []string {
	if message == nil {
		return nil
	}
	fields := strings.Fields(message.Text)
	if len(fields) == 0 {
		return nil
	}
	command := strings.TrimPrefix(strings.ToLower(fields[0]), "/")
	if at := strings.IndexByte(command, '@'); at >= 0 {
		command = command[:at]
	}
	if command != expected {
		return nil
	}
	return fields[1:]
}

func (s *registrationService) registrationMembershipUpdate(_ context.Context, update telego.Update) bool {
	membership := update.MyChatMember
	if membership == nil || membership.NewChatMember == nil {
		return false
	}
	if membership.Chat.Type != telego.ChatTypeGroup && membership.Chat.Type != telego.ChatTypeSupergroup {
		return false
	}
	status := membership.NewChatMember.MemberStatus()
	if status == telego.MemberStatusLeft || status == telego.MemberStatusBanned {
		return false
	}
	return !s.cfg.IsKnownChat(membership.Chat.ID) && !s.settings.IsKnownChat(membership.Chat.ID)
}

func (s *registrationService) effectiveMembershipUpdate(_ context.Context, update telego.Update) bool {
	membership := update.MyChatMember
	if membership == nil || membership.OldChatMember == nil || membership.NewChatMember == nil {
		return false
	}
	if membership.Chat.Type != telego.ChatTypeGroup && membership.Chat.Type != telego.ChatTypeSupergroup {
		return false
	}
	return s.settings.IsGroup(membership.Chat.ID) &&
		!reflect.DeepEqual(membership.OldChatMember, membership.NewChatMember)
}

func (s *registrationService) EnsureOwnerClaim() error {
	now := s.now()
	nonce, _, err := s.settings.EnsureOwnerClaim(now, s.cfg.OwnerClaimLifetime())
	if err != nil {
		return err
	}
	if nonce == "" {
		return nil
	}
	state := s.settings.Registrations()
	link := fmt.Sprintf("https://t.me/%s?start=owner_%s", s.username, nonce)
	log.Printf("OWNER UNCLAIMED: claim link is private, one use, and expires %s: %s",
		time.Unix(state.OwnerClaimExpiresAt, 0).UTC().Format(time.RFC3339), link)
	return nil
}

func (s *registrationService) onOwnerClaim(ctx *th.Context, update telego.Update) error {
	message := update.Message
	if message == nil || message.From == nil {
		return nil
	}
	payload := startPayload(message)
	nonce := strings.TrimPrefix(payload, "owner_")
	l := i18n.FromRequester(message.From.LanguageCode, i18n.LangEN)
	if s.cfg.OwnerClaimUserID != 0 && message.From.ID != s.cfg.OwnerClaimUserID {
		_, _ = s.bot.SendMessage(ctx.Context(), tu.Message(tu.ID(message.Chat.ID),
			i18n.Messages.Bot.Registration.OwnerClaimRefused.For(l)))
		log.Printf("owner claim refused: user=%d error=claim is pinned to another user", message.From.ID)
		return nil
	}
	if err := s.settings.ClaimOwner(message.From.ID, nonce, s.now()); err != nil {
		text := i18n.Messages.Bot.Registration.OwnerClaimRefused.For(l)
		if !errors.Is(err, store.ErrOwnerClaimInvalid) {
			text = i18n.Messages.Bot.Registration.OwnerClaimSaveFailed.For(l)
		}
		_, _ = s.bot.SendMessage(ctx.Context(), tu.Message(tu.ID(message.Chat.ID), text))
		log.Printf("owner claim refused: user=%d error=%v", message.From.ID, err)
		return nil
	}
	_, _ = s.bot.SendMessage(ctx.Context(), tu.Message(tu.ID(message.Chat.ID),
		i18n.Messages.Bot.Registration.OwnerClaimed.For(l)))
	log.Printf("owner claimed: user=%d", message.From.ID)
	return nil
}

func (s *registrationService) onEnrollmentCommand(ctx *th.Context, update telego.Update) error {
	message := update.Message
	if message == nil || message.From == nil {
		return nil
	}
	l := i18n.FromRequester(message.From.LanguageCode, i18n.LangEN)
	nonce, err := s.settings.IssueEnrollmentNonce(message.From.ID, s.now(), enrollmentLifetime)
	if err != nil {
		text := i18n.Messages.Bot.Registration.RegistrationSaveFailed.For(l)
		if errors.Is(err, store.ErrRegistrationOwnerOnly) {
			text = i18n.Messages.Bot.Registration.EnrollmentOwnerOnly.For(l)
		}
		_, _ = s.bot.SendMessage(ctx.Context(), tu.Message(tu.ID(message.Chat.ID), text))
		log.Printf("enrollment link refused: user=%d error=%v", message.From.ID, err)
		return nil
	}
	link := fmt.Sprintf("https://t.me/%s?startgroup=enroll_%s", s.username, nonce.Nonce)
	text := i18n.Messages.Bot.Registration.EnrollmentLink.Render(l, int(enrollmentLifetime/time.Minute), link)
	_, _ = s.bot.SendMessage(ctx.Context(), tu.Message(tu.ID(message.Chat.ID), text))
	log.Printf("enrollment link issued: owner=%d expires=%s", message.From.ID,
		time.Unix(nonce.ExpiresAt, 0).UTC().Format(time.RFC3339))
	return nil
}

type enrollmentResult struct {
	registered bool
	pending    store.PendingRegistration
}

func (s *registrationService) onEnrollmentStart(ctx *th.Context, update telego.Update) error {
	message := update.Message
	if message == nil || message.From == nil {
		return nil
	}
	if message.Chat.Type != telego.ChatTypeGroup && message.Chat.Type != telego.ChatTypeSupergroup {
		l := i18n.FromRequester(message.From.LanguageCode, i18n.LangEN)
		_, _ = s.bot.SendMessage(ctx.Context(), tu.Message(tu.ID(message.Chat.ID),
			i18n.Messages.Bot.Registration.EnrollmentRefused.For(l)))
		log.Printf("enrollment payload refused outside group: user=%d chat=%d", message.From.ID, message.Chat.ID)
		return nil
	}
	nonce := strings.TrimPrefix(startPayload(message), "enroll_")
	if message.From.IsBot || !s.actorIsAdmin(ctx.Context(), message.Chat.ID, message.From.ID) {
		s.refuseEnrollment(ctx.Context(), message.Chat, message.From.ID, "actor is not a current administrator")
		return nil
	}

	result, err := s.consumeEnrollment(
		ctx.Context(), message.Chat.ID, message.From.ID, groupTitle(message.Chat), nonce,
	)
	if err != nil {
		s.refuseEnrollment(ctx.Context(), message.Chat, message.From.ID, err.Error())
		return nil
	}
	if result.registered {
		s.cancelUnknownLeave(message.Chat.ID)
		s.registrationCompleted(ctx.Context(), message.Chat, *message.From)
		return nil
	}
	expires := time.Unix(result.pending.ExpiresAt, 0)
	s.scheduleUnknownLeave(message.Chat.ID, result.pending.Title, expires)
	s.sendRegistrationText(ctx.Context(), message.Chat.ID,
		i18n.Messages.Bot.Registration.RegistrationPending.Render(
			i18n.FromRequester(message.From.LanguageCode, s.groupLanguage(message.Chat.ID)), groupTitle(message.Chat)))
	log.Printf("group registration pending promotion: group=%d actor=%d expires=%s",
		message.Chat.ID, message.From.ID, expires.UTC().Format(time.RFC3339))
	return nil
}

func (s *registrationService) consumeEnrollment(
	ctx context.Context,
	groupID int64,
	actorID int64,
	title string,
	nonce string,
) (enrollmentResult, error) {
	unlock := s.transitions.lock(groupID)
	defer unlock()
	var result enrollmentResult
	_, err := s.mutateRegistrationsWithMembership(
		ctx,
		groupID,
		func(state *store.RegistrationState, membership botMembershipState) (bool, error) {
			if membership != botMembershipAdmin && membership != botMembershipMember {
				return false, errBotMembershipIneligible
			}
			now := s.now()
			index := -1
			for i, candidate := range state.EnrollmentNonces {
				if candidate.Nonce == nonce && candidate.IssuedBy == state.OwnerID &&
					now.Unix() < candidate.ExpiresAt {
					index = i
					break
				}
			}
			if index < 0 {
				return false, errEnrollmentInvalid
			}
			state.EnrollmentNonces = append(state.EnrollmentNonces[:index], state.EnrollmentNonces[index+1:]...)
			result = enrollmentResult{registered: membership == botMembershipAdmin}
			if result.registered {
				s.addRegisteredGroup(state, groupID, actorID, title)
			} else {
				result.pending = store.PendingRegistration{
					GroupID:      groupID,
					RegisteredBy: actorID,
					Title:        title,
					ExpiresAt:    now.Add(registrationPending).Unix(),
				}
				s.putPendingRegistration(state, result.pending)
			}
			return true, nil
		},
	)
	return result, err
}

func (s *registrationService) onMyChatMember(ctx *th.Context, update telego.Update) error {
	membership := update.MyChatMember
	if membership == nil {
		return nil
	}
	actor := membership.From
	if actor.ID <= 0 || actor.IsBot || !s.actorIsAdmin(ctx.Context(), membership.Chat.ID, actor.ID) {
		s.leaveUnknown(ctx.Context(), membership.Chat, actor.ID, "membership actor is not a current human administrator")
		return nil
	}
	now := s.now()
	state := s.settings.Registrations()
	pending, hasPending := pendingRegistration(state, membership.Chat.ID)
	if hasPending && now.Unix() >= pending.ExpiresAt {
		if err := s.expirePending(membership.Chat.ID, pending); err != nil {
			log.Printf("pending registration cleanup failed: group=%d error=%v", membership.Chat.ID, err)
		}
		s.leaveUnknown(ctx.Context(), membership.Chat, actor.ID, "pending registration expired")
		return nil
	}
	if hasPending {
		status, completed, err := s.completePending(ctx.Context(), membership.Chat.ID, pending)
		if err != nil {
			if errors.Is(err, errBotMembershipUnreadable) || errors.Is(err, errEnrollmentInvalid) {
				s.leaveUnknown(ctx.Context(), membership.Chat, actor.ID, err.Error())
			} else {
				s.registrationPersistenceFailure(ctx.Context(), membership.Chat, actor)
			}
			return nil
		}
		switch {
		case completed:
			s.cancelUnknownLeave(membership.Chat.ID)
			s.registrationCompleted(ctx.Context(), membership.Chat, actor)
		case status == botMembershipMember:
			s.scheduleUnknownLeave(membership.Chat.ID, pending.Title, time.Unix(pending.ExpiresAt, 0))
		default:
			s.leaveUnknown(ctx.Context(), membership.Chat, actor.ID, "pending group has ineligible bot status")
		}
		return nil
	}

	if state.OwnerID != 0 && actor.ID == state.OwnerID {
		status, pending, err := s.authorizeOwnerMembership(
			ctx.Context(), membership.Chat.ID, actor.ID, groupTitle(membership.Chat), now,
		)
		if err != nil {
			if errors.Is(err, errBotMembershipUnreadable) || errors.Is(err, errBotMembershipIneligible) {
				s.leaveUnknown(ctx.Context(), membership.Chat, actor.ID, err.Error())
			} else {
				s.registrationPersistenceFailure(ctx.Context(), membership.Chat, actor)
			}
			return nil
		}
		if status == botMembershipAdmin {
			s.cancelUnknownLeave(membership.Chat.ID)
			s.registrationCompleted(ctx.Context(), membership.Chat, actor)
			return nil
		}
		if status == botMembershipMember {
			expires := time.Unix(pending.ExpiresAt, 0)
			s.scheduleUnknownLeave(membership.Chat.ID, pending.Title, expires)
			s.sendRegistrationText(ctx.Context(), actor.ID,
				i18n.Messages.Bot.Registration.RegistrationPending.Render(
					i18n.FromRequester(actor.LanguageCode, s.groupLanguage(membership.Chat.ID)), groupTitle(membership.Chat)))
			return nil
		}
	}

	if membership.NewChatMember.MemberStatus() == telego.MemberStatusMember && state.OwnerID != 0 {
		leave, status, err := s.persistUnknownLeaveIfMember(
			ctx.Context(), membership.Chat.ID, groupTitle(membership.Chat), now.Add(registrationPending),
		)
		if err != nil {
			if errors.Is(err, errUnknownLeaveSuperseded) {
				return nil
			}
			log.Printf("unknown-group leave persistence failed: group=%d error=%v", membership.Chat.ID, err)
			s.leaveUnknown(ctx.Context(), membership.Chat, actor.ID, "unknown-group leave persistence failed")
			return nil
		}
		if status == botMembershipMember {
			s.scheduleUnknownLeave(leave.GroupID, leave.Title, time.Unix(leave.ExpiresAt, 0))
			log.Printf("unknown group awaiting enrollment payload: group=%d actor=%d", membership.Chat.ID, actor.ID)
			return nil
		}
	}
	s.leaveUnknown(ctx.Context(), membership.Chat, actor.ID, "owner or enrollment authorization is required")
	return nil
}

func (s *registrationService) onEffectiveMembershipUpdate(ctx *th.Context, update telego.Update) error {
	membership := update.MyChatMember
	if membership == nil || s.onMembershipChanged == nil {
		return nil
	}
	now := s.now()
	s.reportMu.Lock()
	if after, ok := s.reportAfter[membership.Chat.ID]; ok && now.Before(after) {
		s.reportMu.Unlock()
		return nil
	}
	s.reportAfter[membership.Chat.ID] = now.Add(setupReportCooldown)
	s.reportMu.Unlock()
	s.onMembershipChanged(ctx.Context(), membership.Chat.ID)
	return nil
}

func (s *registrationService) onUnregisterCommand(ctx *th.Context, update telego.Update) error {
	message := update.Message
	if message == nil || message.From == nil {
		return nil
	}
	l := i18n.FromRequester(message.From.LanguageCode, i18n.LangEN)
	state := s.settings.Registrations()
	if state.OwnerID == 0 || message.From.ID != state.OwnerID {
		s.sendRegistrationText(ctx.Context(), message.Chat.ID,
			i18n.Messages.Bot.Registration.UnregisterOwnerOnly.For(l))
		return nil
	}
	arguments := commandArguments(message, "unregister")
	if len(arguments) != 1 {
		s.sendRegistrationText(ctx.Context(), message.Chat.ID,
			i18n.Messages.Bot.Registration.UnregisterRefused.For(l))
		return nil
	}
	groupID, err := strconv.ParseInt(arguments[0], 10, 64)
	if err != nil || groupID == 0 {
		s.sendRegistrationText(ctx.Context(), message.Chat.ID,
			i18n.Messages.Bot.Registration.UnregisterRefused.For(l))
		return nil
	}
	title, err := s.unregisterGroup(groupID)
	if err != nil {
		text := i18n.Messages.Bot.Registration.UnregisterSaveFailed.For(l)
		if errors.Is(err, errRuntimeGroupNotFound) {
			text = i18n.Messages.Bot.Registration.UnregisterRefused.For(l)
		}
		s.sendRegistrationText(ctx.Context(), message.Chat.ID, text)
		log.Printf("group unregister refused: group=%d owner=%d error=%v", groupID, message.From.ID, err)
		return nil
	}
	s.leaveUnknown(ctx.Context(), telego.Chat{
		ID: groupID, Type: telego.ChatTypeSupergroup, Title: title,
	}, message.From.ID, "owner unregistered group")
	s.sendRegistrationText(ctx.Context(), message.Chat.ID,
		i18n.Messages.Bot.Registration.GroupUnregistered.Render(l, title))
	log.Printf("group unregistered: group=%d owner=%d", groupID, message.From.ID)
	return nil
}

func (s *registrationService) actorIsAdmin(ctx context.Context, groupID, actorID int64) bool {
	member, err := s.bot.GetChatMember(ctx, &telego.GetChatMemberParams{ChatID: tu.ID(groupID), UserID: actorID})
	if err != nil || member == nil {
		return false
	}
	status := member.MemberStatus()
	return status == telego.MemberStatusAdministrator || status == telego.MemberStatusCreator
}

func (s *registrationService) currentBotMembership(ctx context.Context, groupID int64) (botMembershipState, error) {
	member, err := s.bot.GetChatMember(ctx, &telego.GetChatMemberParams{
		ChatID: tu.ID(groupID),
		UserID: s.selfID,
	})
	if err != nil || member == nil {
		return botMembershipIneligible, errBotMembershipUnreadable
	}
	switch member.MemberStatus() {
	case telego.MemberStatusAdministrator, telego.MemberStatusCreator:
		return botMembershipAdmin, nil
	case telego.MemberStatusMember:
		return botMembershipMember, nil
	default:
		return botMembershipIneligible, nil
	}
}

func (s *registrationService) authorizeOwnerMembership(
	ctx context.Context,
	groupID int64,
	actorID int64,
	title string,
	now time.Time,
) (botMembershipState, store.PendingRegistration, error) {
	unlock := s.transitions.lock(groupID)
	defer unlock()
	pending := store.PendingRegistration{
		GroupID:      groupID,
		RegisteredBy: actorID,
		Title:        title,
		ExpiresAt:    now.Add(registrationPending).Unix(),
	}
	status, err := s.mutateRegistrationsWithMembership(
		ctx,
		groupID,
		func(state *store.RegistrationState, membership botMembershipState) (bool, error) {
			if membership != botMembershipAdmin && membership != botMembershipMember {
				return false, errBotMembershipIneligible
			}
			if membership == botMembershipAdmin {
				s.addRegisteredGroup(state, groupID, actorID, title)
			} else {
				s.putPendingRegistration(state, pending)
			}
			return true, nil
		},
	)
	return status, pending, err
}

func (s *registrationService) addRegisteredGroup(state *store.RegistrationState, groupID, actorID int64, title string) {
	registered := false
	for _, group := range state.RegisteredGroups {
		if group.ID == groupID {
			registered = true
			break
		}
	}
	if !registered {
		state.RegisteredGroups = append(state.RegisteredGroups, store.RegisteredGroup{
			ID:           groupID,
			RegisteredBy: actorID,
			Title:        title,
		})
	}
	removePendingRegistration(state, groupID)
	removeUnknownGroupLeave(state, groupID)
	if state.ControlGroupID == 0 && len(s.settings.GroupIDs()) == 0 {
		state.ControlGroupID = groupID
	}
}

func (s *registrationService) putPendingRegistration(state *store.RegistrationState, pending store.PendingRegistration) {
	removeUnknownGroupLeave(state, pending.GroupID)
	for i := range state.PendingRegistrations {
		if state.PendingRegistrations[i].GroupID == pending.GroupID {
			state.PendingRegistrations[i] = pending
			return
		}
	}
	state.PendingRegistrations = append(state.PendingRegistrations, pending)
}

func pendingRegistration(state store.RegistrationState, groupID int64) (store.PendingRegistration, bool) {
	for _, pending := range state.PendingRegistrations {
		if pending.GroupID == groupID {
			return pending, true
		}
	}
	return store.PendingRegistration{}, false
}

func removePendingRegistration(state *store.RegistrationState, groupID int64) {
	for i, pending := range state.PendingRegistrations {
		if pending.GroupID == groupID {
			state.PendingRegistrations = append(state.PendingRegistrations[:i], state.PendingRegistrations[i+1:]...)
			return
		}
	}
}

func (s *registrationService) completePending(
	ctx context.Context,
	groupID int64,
	pending store.PendingRegistration,
) (botMembershipState, bool, error) {
	unlock := s.transitions.lock(groupID)
	defer unlock()
	completed := false
	status, err := s.mutateRegistrationsWithMembership(
		ctx,
		groupID,
		func(state *store.RegistrationState, membership botMembershipState) (bool, error) {
			completed = false
			if membership != botMembershipAdmin {
				return false, nil
			}
			current, ok := pendingRegistration(*state, groupID)
			if !ok || current.ExpiresAt != pending.ExpiresAt || s.now().Unix() >= current.ExpiresAt {
				return false, errEnrollmentInvalid
			}
			s.addRegisteredGroup(state, groupID, current.RegisteredBy, current.Title)
			completed = true
			return true, nil
		},
	)
	return status, completed, err
}

func (s *registrationService) expirePending(groupID int64, expected store.PendingRegistration) error {
	unlock := s.transitions.lock(groupID)
	defer unlock()
	return s.mutateRegistrations(func(state *store.RegistrationState) error {
		current, ok := pendingRegistration(*state, groupID)
		if !ok || current.ExpiresAt != expected.ExpiresAt {
			return nil
		}
		removePendingRegistration(state, groupID)
		s.putUnknownGroupLeave(state, store.UnknownGroupLeave{
			GroupID:   groupID,
			Title:     current.Title,
			ExpiresAt: current.ExpiresAt,
		})
		return nil
	})
}

func unknownGroupLeave(state store.RegistrationState, groupID int64) (store.UnknownGroupLeave, bool) {
	for _, leave := range state.UnknownGroupLeaves {
		if leave.GroupID == groupID {
			return leave, true
		}
	}
	return store.UnknownGroupLeave{}, false
}

func (s *registrationService) putUnknownGroupLeave(state *store.RegistrationState, leave store.UnknownGroupLeave) {
	for i := range state.UnknownGroupLeaves {
		if state.UnknownGroupLeaves[i].GroupID == leave.GroupID {
			if leave.ExpiresAt > state.UnknownGroupLeaves[i].ExpiresAt {
				state.UnknownGroupLeaves[i] = leave
			}
			return
		}
	}
	state.UnknownGroupLeaves = append(state.UnknownGroupLeaves, leave)
}

func removeUnknownGroupLeave(state *store.RegistrationState, groupID int64) {
	for i, leave := range state.UnknownGroupLeaves {
		if leave.GroupID == groupID {
			state.UnknownGroupLeaves = append(state.UnknownGroupLeaves[:i], state.UnknownGroupLeaves[i+1:]...)
			return
		}
	}
}

func (s *registrationService) persistUnknownLeaveIfMember(
	ctx context.Context,
	groupID int64,
	title string,
	deadline time.Time,
) (store.UnknownGroupLeave, botMembershipState, error) {
	unlock := s.transitions.lock(groupID)
	defer unlock()
	leave := store.UnknownGroupLeave{GroupID: groupID, Title: title, ExpiresAt: deadline.Unix()}
	status, err := s.mutateRegistrationsWithMembership(
		ctx,
		groupID,
		func(state *store.RegistrationState, membership botMembershipState) (bool, error) {
			if membership != botMembershipMember {
				return false, nil
			}
			if _, ok := pendingRegistration(*state, groupID); ok {
				return false, errUnknownLeaveSuperseded
			}
			for _, registered := range state.RegisteredGroups {
				if registered.ID == groupID {
					return false, errUnknownLeaveSuperseded
				}
			}
			s.putUnknownGroupLeave(state, leave)
			if current, ok := unknownGroupLeave(*state, groupID); ok {
				leave = current
			}
			return true, nil
		},
	)
	return leave, status, err
}

func (s *registrationService) unregisterGroup(groupID int64) (string, error) {
	unlock := s.transitions.lock(groupID)
	defer unlock()
	title := ""
	err := s.mutateRegistrations(func(state *store.RegistrationState) error {
		index := -1
		for i, group := range state.RegisteredGroups {
			if group.ID == groupID {
				index = i
				title = group.Title
				break
			}
		}
		if index < 0 {
			return errRuntimeGroupNotFound
		}
		state.RegisteredGroups = append(state.RegisteredGroups[:index], state.RegisteredGroups[index+1:]...)
		removePendingRegistration(state, groupID)
		if state.ControlGroupID == groupID {
			state.ControlGroupID = 0
		}
		s.putUnknownGroupLeave(state, store.UnknownGroupLeave{
			GroupID:   groupID,
			Title:     title,
			ExpiresAt: s.now().Unix(),
		})
		return nil
	})
	if title == "" {
		title = strconv.FormatInt(groupID, 10)
	}
	if err == nil {
		s.reportMu.Lock()
		delete(s.reportAfter, groupID)
		s.reportMu.Unlock()
	}
	return title, err
}

func (s *registrationService) mutateRegistrations(mutate func(*store.RegistrationState) error) error {
	for {
		current := s.settings.Registrations()
		next := cloneRegistrationMutation(current)
		if err := mutate(&next); err != nil {
			return err
		}
		if _, err := s.settings.CommitRegistrations(current.Revision, next); errors.Is(err, store.ErrSettingsConflict) {
			continue
		} else {
			return err
		}
	}
}

func (s *registrationService) mutateRegistrationsWithMembership(
	ctx context.Context,
	groupID int64,
	mutate func(*store.RegistrationState, botMembershipState) (bool, error),
) (botMembershipState, error) {
	for {
		current := s.settings.Registrations()
		next := cloneRegistrationMutation(current)
		membership, err := s.currentBotMembership(ctx, groupID)
		if err != nil {
			return membership, err
		}
		commit, err := mutate(&next, membership)
		if err != nil || !commit {
			return membership, err
		}
		if _, err := s.settings.CommitRegistrations(current.Revision, next); errors.Is(err, store.ErrSettingsConflict) {
			continue
		} else {
			return membership, err
		}
	}
}

func cloneRegistrationMutation(current store.RegistrationState) store.RegistrationState {
	next := current
	next.RegisteredGroups = append([]store.RegisteredGroup(nil), current.RegisteredGroups...)
	next.EnrollmentNonces = append([]store.EnrollmentNonce(nil), current.EnrollmentNonces...)
	next.PendingRegistrations = append([]store.PendingRegistration(nil), current.PendingRegistrations...)
	next.UnknownGroupLeaves = append([]store.UnknownGroupLeave(nil), current.UnknownGroupLeaves...)
	return next
}

func (s *registrationService) registrationCompleted(ctx context.Context, chat telego.Chat, actor telego.User) {
	l := i18n.FromRequester(actor.LanguageCode, s.groupLanguage(chat.ID))
	text := i18n.Messages.Bot.Registration.GroupRegistered.Render(l, groupTitle(chat))
	if _, err := s.bot.SendMessage(ctx, tu.Message(tu.ID(actor.ID), text)); err != nil {
		_, _ = s.bot.SendMessage(ctx, tu.Message(tu.ID(chat.ID), text))
	}
	log.Printf("group registered: group=%d actor=%d", chat.ID, actor.ID)
	if s.onRegistered != nil {
		go s.onRegistered(s.root, chat.ID)
	}
}

func (s *registrationService) registrationPersistenceFailure(ctx context.Context, chat telego.Chat, actor telego.User) {
	l := i18n.FromRequester(actor.LanguageCode, s.groupLanguage(chat.ID))
	_, _ = s.bot.SendMessage(ctx, tu.Message(tu.ID(chat.ID),
		i18n.Messages.Bot.Registration.RegistrationSaveFailed.For(l)))
	s.leaveUnknown(ctx, chat, actor.ID, "registration persistence failed")
}

func (s *registrationService) refuseEnrollment(ctx context.Context, chat telego.Chat, actorID int64, reason string) {
	l := s.groupLanguage(chat.ID)
	_, _ = s.bot.SendMessage(ctx, tu.Message(tu.ID(chat.ID),
		i18n.Messages.Bot.Registration.EnrollmentRefused.For(l)))
	s.leaveUnknown(ctx, chat, actorID, reason)
}

func (s *registrationService) sendRegistrationText(ctx context.Context, chatID int64, text string) {
	_, _ = s.bot.SendMessage(ctx, tu.Message(tu.ID(chatID), text))
}

func (s *registrationService) leaveUnknown(ctx context.Context, chat telego.Chat, actorID int64, reason string) bool {
	unlock := s.transitions.lock(chat.ID)
	left := s.leaveUnknownLocked(ctx, chat, actorID, reason)
	unlock()
	if left {
		s.notifyUnauthorizedLeave(ctx, chat)
	}
	return left
}

func (s *registrationService) leaveUnknownLocked(
	ctx context.Context,
	chat telego.Chat,
	actorID int64,
	reason string,
) bool {
	if s.settings.IsKnownChat(chat.ID) || s.cfg.IsKnownChat(chat.ID) {
		_ = s.clearUnknownGroupLeave(chat.ID)
		return false
	}
	log.Printf("group registration refused: group=%d actor=%d reason=%s", chat.ID, actorID, reason)
	if err := s.bot.LeaveChat(ctx, &telego.LeaveChatParams{ChatID: tu.ID(chat.ID)}); err != nil {
		log.Printf("group registration refusal leave failed: group=%d error=%v", chat.ID, err)
		return false
	}
	if err := s.clearUnknownGroupLeave(chat.ID); err != nil {
		log.Printf("unknown-group leave cleanup commit failed: group=%d error=%v", chat.ID, err)
	}
	return true
}

func (s *registrationService) clearUnknownGroupLeave(groupID int64) error {
	if _, ok := unknownGroupLeave(s.settings.Registrations(), groupID); !ok {
		return nil
	}
	return s.mutateRegistrations(func(state *store.RegistrationState) error {
		removeUnknownGroupLeave(state, groupID)
		return nil
	})
}

func (s *registrationService) notifyUnauthorizedLeave(ctx context.Context, chat telego.Chat) {
	if s.cfg.AdminLogChatID == 0 {
		return
	}
	l := i18n.FromStored(s.cfg.LangForGroup(0))
	_, _ = s.bot.SendMessage(ctx, tu.Message(tu.ID(s.cfg.AdminLogChatID),
		i18n.Messages.Bot.Lifecycle.UnauthorizedChat.Render(l, chat.Title, chat.ID, chat.Type)))
}

func (s *registrationService) cancelUnknownLeave(groupID int64) {
	s.waitingMu.Lock()
	delete(s.waiting, groupID)
	s.waitingMu.Unlock()
}

func (s *registrationService) scheduleUnknownLeave(groupID int64, title string, deadline time.Time) {
	s.waitingMu.Lock()
	if current, ok := s.waiting[groupID]; ok && !deadline.After(current) {
		s.waitingMu.Unlock()
		return
	}
	s.waiting[groupID] = deadline
	s.waitingMu.Unlock()

	go func() {
		delay := time.Until(deadline)
		if delay < 0 {
			delay = 0
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-s.root.Done():
			return
		case <-timer.C:
		}
		s.waitingMu.Lock()
		current, ok := s.waiting[groupID]
		if !ok || !current.Equal(deadline) {
			s.waitingMu.Unlock()
			return
		}
		delete(s.waiting, groupID)
		s.waitingMu.Unlock()
		s.handleUnknownLeaveDeadline(groupID, title)
	}()
}

func (s *registrationService) handleUnknownLeaveDeadline(groupID int64, title string) {
	unlock := s.transitions.lock(groupID)
	state := s.settings.Registrations()
	now := s.now()
	if pending, ok := pendingRegistration(state, groupID); ok {
		if now.Unix() < pending.ExpiresAt {
			unlock()
			s.scheduleUnknownLeave(groupID, pending.Title, time.Unix(pending.ExpiresAt, 0))
			return
		}
		if err := s.mutateRegistrations(func(next *store.RegistrationState) error {
			current, exists := pendingRegistration(*next, groupID)
			if !exists || current.ExpiresAt != pending.ExpiresAt {
				return nil
			}
			removePendingRegistration(next, groupID)
			s.putUnknownGroupLeave(next, store.UnknownGroupLeave{
				GroupID: groupID, Title: current.Title, ExpiresAt: current.ExpiresAt,
			})
			return nil
		}); err != nil {
			log.Printf("pending registration expiry commit failed: group=%d error=%v", groupID, err)
		}
		state = s.settings.Registrations()
	}
	if leave, ok := unknownGroupLeave(state, groupID); ok {
		if now.Unix() < leave.ExpiresAt {
			unlock()
			s.scheduleUnknownLeave(groupID, leave.Title, time.Unix(leave.ExpiresAt, 0))
			return
		}
		if leave.Title != "" {
			title = leave.Title
		}
	}
	chat := telego.Chat{ID: groupID, Type: telego.ChatTypeSupergroup, Title: title}
	left := s.leaveUnknownLocked(context.Background(), chat, 0, "registration grace period expired")
	unlock()
	if left {
		s.notifyUnauthorizedLeave(context.Background(), chat)
	}
}

func (s *registrationService) groupLanguage(groupID int64) i18n.Lang {
	if group, ok := s.settings.Group(groupID); ok {
		return i18n.FromStored(group.Lang().Value)
	}
	return i18n.FromStored(s.cfg.LangForGroup(groupID))
}

func groupTitle(chat telego.Chat) string {
	if chat.Title != "" {
		return chat.Title
	}
	return strconv.FormatInt(chat.ID, 10)
}
