package main

import (
	"context"
	"errors"
	"fmt"
	"log"
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
	ownerClaimLifetime  = 24 * time.Hour
	enrollmentLifetime  = 10 * time.Minute
	registrationPending = 10 * time.Minute
)

var errEnrollmentInvalid = errors.New("enrollment nonce is invalid")

type registrationService struct {
	root         context.Context
	bot          *telego.Bot
	settings     *store.Settings
	cfg          *config.Config
	username     string
	selfID       int64
	now          func() time.Time
	onRegistered func(context.Context, int64)

	waitingMu sync.Mutex
	waiting   map[int64]time.Time
}

func newRegistrationService(
	root context.Context,
	bot *telego.Bot,
	settings *store.Settings,
	cfg *config.Config,
	username string,
	selfID int64,
	onRegistered func(context.Context, int64),
) *registrationService {
	s := &registrationService{
		root:         root,
		bot:          bot,
		settings:     settings,
		cfg:          cfg,
		username:     username,
		selfID:       selfID,
		now:          time.Now,
		onRegistered: onRegistered,
		waiting:      make(map[int64]time.Time),
	}
	for _, pending := range settings.Registrations().PendingRegistrations {
		s.scheduleUnknownLeave(pending.GroupID, pending.Title, time.Unix(pending.ExpiresAt, 0))
	}
	return s
}

func (s *registrationService) Register(handler *th.BotHandler) {
	handler.Handle(s.onOwnerClaim, th.And(th.CommandEqual("start"), startPayloadPrefix("owner_"), privateMessage))
	handler.Handle(s.onEnrollmentStart, th.And(th.CommandEqual("start"), startPayloadPrefix("enroll_")))
	handler.Handle(s.onEnrollmentCommand, th.And(th.CommandEqual("enroll"), privateMessage))
	handler.Handle(s.onMyChatMember, s.registrationMembershipUpdate)
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

func (s *registrationService) EnsureOwnerClaim() error {
	now := s.now()
	nonce, _, err := s.settings.EnsureOwnerClaim(now, ownerClaimLifetime)
	if err != nil {
		return err
	}
	if nonce == "" {
		return nil
	}
	state := s.settings.Registrations()
	link := fmt.Sprintf("https://t.me/%s?start=owner_%s", s.username, nonce)
	log.Printf("owner claim link (private, one use, expires %s): %s",
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
	if err := s.settings.ClaimOwner(message.From.ID, nonce, s.now()); err != nil {
		text := i18n.Messages.Bot.Registration.OwnerClaimRefused.For(l)
		if !errors.Is(err, store.ErrOwnerClaimInvalid) {
			text = i18n.Messages.Bot.Registration.RegistrationSaveFailed.For(l)
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
	selfMember, err := s.bot.GetChatMember(ctx.Context(), &telego.GetChatMemberParams{
		ChatID: tu.ID(message.Chat.ID),
		UserID: s.selfID,
	})
	if err != nil || selfMember == nil {
		s.refuseEnrollment(ctx.Context(), message.Chat, message.From.ID, "bot membership is unreadable")
		return nil
	}
	status := selfMember.MemberStatus()
	complete := status == telego.MemberStatusAdministrator || status == telego.MemberStatusCreator
	if !complete && status != telego.MemberStatusMember {
		s.refuseEnrollment(ctx.Context(), message.Chat, message.From.ID, "bot is not an eligible group member")
		return nil
	}

	now := s.now()
	err = s.mutateRegistrations(func(state *store.RegistrationState) error {
		index := -1
		for i, candidate := range state.EnrollmentNonces {
			if candidate.Nonce == nonce && candidate.IssuedBy == state.OwnerID && now.Unix() < candidate.ExpiresAt {
				index = i
				break
			}
		}
		if index < 0 {
			return errEnrollmentInvalid
		}
		state.EnrollmentNonces = append(state.EnrollmentNonces[:index], state.EnrollmentNonces[index+1:]...)
		if complete {
			s.addRegisteredGroup(state, message.Chat.ID, message.From.ID, groupTitle(message.Chat))
			return nil
		}
		s.putPendingRegistration(state, store.PendingRegistration{
			GroupID:      message.Chat.ID,
			RegisteredBy: message.From.ID,
			Title:        groupTitle(message.Chat),
			ExpiresAt:    now.Add(registrationPending).Unix(),
		})
		return nil
	})
	if err != nil {
		s.refuseEnrollment(ctx.Context(), message.Chat, message.From.ID, err.Error())
		return nil
	}
	if complete {
		s.registrationCompleted(ctx.Context(), message.Chat, *message.From)
		return nil
	}
	expires := now.Add(registrationPending)
	s.scheduleUnknownLeave(message.Chat.ID, groupTitle(message.Chat), expires)
	s.sendRegistrationText(ctx.Context(), message.Chat.ID,
		i18n.Messages.Bot.Registration.RegistrationPending.Render(
			i18n.FromRequester(message.From.LanguageCode, s.groupLanguage(message.Chat.ID)), groupTitle(message.Chat)))
	log.Printf("group registration pending promotion: group=%d actor=%d expires=%s", message.Chat.ID, message.From.ID, expires.UTC().Format(time.RFC3339))
	return nil
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
	status := membership.NewChatMember.MemberStatus()
	botAdmin := status == telego.MemberStatusAdministrator || status == telego.MemberStatusCreator
	botMember := status == telego.MemberStatusMember
	now := s.now()
	state := s.settings.Registrations()
	pending, hasPending := pendingRegistration(state, membership.Chat.ID)
	if hasPending && now.Unix() >= pending.ExpiresAt {
		_ = s.removePending(membership.Chat.ID)
		s.leaveUnknown(ctx.Context(), membership.Chat, actor.ID, "pending registration expired")
		return nil
	}
	if hasPending {
		if !botAdmin {
			if botMember {
				s.scheduleUnknownLeave(membership.Chat.ID, pending.Title, time.Unix(pending.ExpiresAt, 0))
				return nil
			}
			s.leaveUnknown(ctx.Context(), membership.Chat, actor.ID, "pending group has ineligible bot status")
			return nil
		}
		if err := s.completePending(membership.Chat.ID, pending); err != nil {
			s.registrationPersistenceFailure(ctx.Context(), membership.Chat, actor)
			return nil
		}
		s.registrationCompleted(ctx.Context(), membership.Chat, actor)
		return nil
	}

	if state.OwnerID != 0 && actor.ID == state.OwnerID {
		if botAdmin {
			if err := s.registerGroup(membership.Chat.ID, actor.ID, groupTitle(membership.Chat)); err != nil {
				s.registrationPersistenceFailure(ctx.Context(), membership.Chat, actor)
				return nil
			}
			s.registrationCompleted(ctx.Context(), membership.Chat, actor)
			return nil
		}
		if botMember {
			expires := now.Add(registrationPending)
			if err := s.persistPending(membership.Chat.ID, actor.ID, groupTitle(membership.Chat), expires); err != nil {
				s.registrationPersistenceFailure(ctx.Context(), membership.Chat, actor)
				return nil
			}
			s.scheduleUnknownLeave(membership.Chat.ID, groupTitle(membership.Chat), expires)
			s.sendRegistrationText(ctx.Context(), actor.ID,
				i18n.Messages.Bot.Registration.RegistrationPending.Render(
					i18n.FromRequester(actor.LanguageCode, s.groupLanguage(membership.Chat.ID)), groupTitle(membership.Chat)))
			return nil
		}
	}

	if botMember && state.OwnerID != 0 {
		s.scheduleUnknownLeave(membership.Chat.ID, groupTitle(membership.Chat), now.Add(registrationPending))
		log.Printf("unknown group awaiting enrollment payload: group=%d actor=%d", membership.Chat.ID, actor.ID)
		return nil
	}
	s.leaveUnknown(ctx.Context(), membership.Chat, actor.ID, "owner or enrollment authorization is required")
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

func (s *registrationService) registerGroup(groupID, actorID int64, title string) error {
	return s.mutateRegistrations(func(state *store.RegistrationState) error {
		s.addRegisteredGroup(state, groupID, actorID, title)
		return nil
	})
}

func (s *registrationService) addRegisteredGroup(state *store.RegistrationState, groupID, actorID int64, title string) {
	for _, group := range state.RegisteredGroups {
		if group.ID == groupID {
			return
		}
	}
	state.RegisteredGroups = append(state.RegisteredGroups, store.RegisteredGroup{
		ID:           groupID,
		RegisteredBy: actorID,
		Title:        title,
	})
	for i := range state.PendingRegistrations {
		if state.PendingRegistrations[i].GroupID == groupID {
			state.PendingRegistrations = append(state.PendingRegistrations[:i], state.PendingRegistrations[i+1:]...)
			break
		}
	}
	if state.ControlGroupID == 0 && len(s.settings.GroupIDs()) == 0 {
		state.ControlGroupID = groupID
	}
}

func (s *registrationService) persistPending(groupID, actorID int64, title string, expires time.Time) error {
	return s.mutateRegistrations(func(state *store.RegistrationState) error {
		s.putPendingRegistration(state, store.PendingRegistration{
			GroupID:      groupID,
			RegisteredBy: actorID,
			Title:        title,
			ExpiresAt:    expires.Unix(),
		})
		return nil
	})
}

func (s *registrationService) putPendingRegistration(state *store.RegistrationState, pending store.PendingRegistration) {
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

func (s *registrationService) completePending(groupID int64, pending store.PendingRegistration) error {
	return s.mutateRegistrations(func(state *store.RegistrationState) error {
		current, ok := pendingRegistration(*state, groupID)
		if !ok || current.ExpiresAt != pending.ExpiresAt || s.now().Unix() >= current.ExpiresAt {
			return errEnrollmentInvalid
		}
		s.addRegisteredGroup(state, groupID, current.RegisteredBy, current.Title)
		return nil
	})
}

func (s *registrationService) removePending(groupID int64) error {
	return s.mutateRegistrations(func(state *store.RegistrationState) error {
		for i, pending := range state.PendingRegistrations {
			if pending.GroupID == groupID {
				state.PendingRegistrations = append(state.PendingRegistrations[:i], state.PendingRegistrations[i+1:]...)
				break
			}
		}
		return nil
	})
}

func (s *registrationService) mutateRegistrations(mutate func(*store.RegistrationState) error) error {
	for {
		current := s.settings.Registrations()
		next := current
		next.RegisteredGroups = append([]store.RegisteredGroup(nil), current.RegisteredGroups...)
		next.EnrollmentNonces = append([]store.EnrollmentNonce(nil), current.EnrollmentNonces...)
		next.PendingRegistrations = append([]store.PendingRegistration(nil), current.PendingRegistrations...)
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

func (s *registrationService) leaveUnknown(ctx context.Context, chat telego.Chat, actorID int64, reason string) {
	if s.settings.IsKnownChat(chat.ID) || s.cfg.IsKnownChat(chat.ID) {
		return
	}
	log.Printf("group registration refused: group=%d actor=%d reason=%s", chat.ID, actorID, reason)
	if err := s.bot.LeaveChat(ctx, &telego.LeaveChatParams{ChatID: tu.ID(chat.ID)}); err != nil {
		log.Printf("group registration refusal leave failed: group=%d error=%v", chat.ID, err)
		return
	}
	if s.cfg.AdminLogChatID != 0 {
		l := i18n.FromStored(s.cfg.LangForGroup(0))
		_, _ = s.bot.SendMessage(ctx, tu.Message(tu.ID(s.cfg.AdminLogChatID),
			i18n.Messages.Bot.Lifecycle.UnauthorizedChat.Render(l, chat.Title, chat.ID, chat.Type)))
	}
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
		if s.settings.IsKnownChat(groupID) {
			return
		}
		state := s.settings.Registrations()
		if pending, ok := pendingRegistration(state, groupID); ok {
			if s.now().Unix() < pending.ExpiresAt {
				s.scheduleUnknownLeave(groupID, pending.Title, time.Unix(pending.ExpiresAt, 0))
				return
			}
			_ = s.removePending(groupID)
		}
		s.leaveUnknown(context.Background(), telego.Chat{
			ID:    groupID,
			Type:  telego.ChatTypeSupergroup,
			Title: title,
		}, 0, "registration grace period expired")
	}()
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
