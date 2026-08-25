// Package bot owns Telegram handler wiring and process-level bot diagnostics.
package bot

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/lookup"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/moderate"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/panel"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/tg"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/verify"
	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
)

type handlerRoute struct {
	name       string
	handler    th.Handler
	predicates []th.Predicate
}

// Service owns Telegram middleware, first-match routes, command menus, and startup diagnostics.
type Service struct {
	cfg            *config.Config
	settings       *store.Settings
	telegram       *tg.Client
	verification   *verify.Service
	administration *panel.Panel
	moderation     *moderate.Service
	lookups        *lookup.Service
	version        string
	dm             *dmHandler
}

// New constructs the process-level bot wiring from the final service graph.
func New(
	cfg *config.Config,
	settings *store.Settings,
	telegram *tg.Client,
	verification *verify.Service,
	administration *panel.Panel,
	moderation *moderate.Service,
	lookups *lookup.Service,
	version string,
) *Service {
	return &Service{
		cfg:            cfg,
		settings:       settings,
		telegram:       telegram,
		verification:   verification,
		administration: administration,
		moderation:     moderation,
		lookups:        lookups,
		version:        version,
		dm: &dmHandler{
			cfg:      cfg,
			telegram: telegram,
			last:     make(map[int64]time.Time),
		},
	}
}

// Register installs middleware and handlers in their first-match behavioral order.
func (s *Service) Register(bh *th.BotHandler) {
	// One malformed update must not terminate the bot.
	bh.Use(th.PanicRecoveryHandler(func(recovered any) error {
		log.Printf("recovered from handler panic: %v", recovered)
		return nil
	}))
	// Channel-sender filtering runs before handlers.
	bh.Use(s.moderation.FilterChannelSenders)
	for _, route := range s.handlerRoutes() {
		bh.Handle(route.handler, route.predicates...)
	}
}

func (s *Service) handlerRoutes() []handlerRoute {
	return []handlerRoute{
		{name: "verify.answer", handler: s.verification.OnAnswer, predicates: []th.Predicate{th.CallbackDataPrefix(verify.AnswerCallbackPrefix)}},
		{name: "verify.admin_action", handler: s.verification.OnAdminAction, predicates: []th.Predicate{th.CallbackDataPrefix(verify.AdminCallbackPrefix)}},
		{name: "verify.channel_recheck", handler: s.verification.OnChannelRecheck, predicates: []th.Predicate{th.CallbackDataPrefix(verify.ChannelRecheckCallbackPrefix)}},
		{name: "verify.join_request", handler: s.verification.OnJoinRequest, predicates: []th.Predicate{th.AnyChatJoinRequest()}},
		{name: "bot.unauthorized_chat", handler: s.onMyChatMember, predicates: []th.Predicate{th.AnyMyChatMember()}},
		{name: "verify.kernel_answer", handler: s.verification.OnKernelAnswer, predicates: []th.Predicate{s.verification.KernelAnswerDM}},
		{name: "bot.private_dm", handler: s.dm.onPrivateDM, predicates: []th.Predicate{privateNonStart}},
		{name: "moderate.sb", handler: s.moderation.OnPurge, predicates: []th.Predicate{th.CommandEqual("sb")}},
		{name: "moderate.ban", handler: s.moderation.OnBan, predicates: []th.Predicate{th.CommandEqual("ban")}},
		{name: "moderate.warn", handler: s.moderation.OnWarn, predicates: []th.Predicate{th.CommandEqual("warn")}},
		{name: "moderate.clearwarn", handler: s.moderation.OnClearWarn, predicates: []th.Predicate{th.CommandEqual("clearwarn")}},
		{name: "moderate.bc", handler: s.moderation.OnBC, predicates: []th.Predicate{th.CommandEqual("bc")}},
		{name: "panel.ping", handler: s.administration.OnPing, predicates: []th.Predicate{th.CommandEqual("ping")}},
		{name: "panel.start", handler: s.administration.OnStart, predicates: []th.Predicate{th.CommandEqual("start")}},
		{name: "panel.stop", handler: s.administration.OnStop, predicates: []th.Predicate{th.CommandEqual("stop")}},
		{name: "panel.stats", handler: s.administration.OnStats, predicates: []th.Predicate{th.CommandEqual("stats")}},
		{name: "lookup.pkg", handler: s.lookups.OnPkg, predicates: []th.Predicate{th.CommandEqual("pkg")}},
		{name: "lookup.use", handler: s.lookups.OnUse, predicates: []th.Predicate{th.CommandEqual("use")}},
		{name: "lookup.bug", handler: s.lookups.OnBug, predicates: []th.Predicate{th.CommandEqual("bug")}},
		{name: "lookup.news", handler: s.lookups.OnNews, predicates: []th.Predicate{th.CommandEqual("news")}},
		{name: "lookup.wiki", handler: s.lookups.OnWiki, predicates: []th.Predicate{th.CommandEqual("wiki")}},
		{name: "lookup.bbs", handler: s.lookups.OnBbs, predicates: []th.Predicate{th.CommandEqual("bbs")}},
		{name: "lookup.pkgs", handler: s.lookups.OnPkgs, predicates: []th.Predicate{th.CommandEqual("pkgs")}},
		{name: "lookup.distro", handler: s.lookups.OnPkgs, predicates: []th.Predicate{th.CommandEqual("distro")}},
		{name: "lookup.arm", handler: s.lookups.OnArm, predicates: []th.Predicate{th.CommandEqual("arm")}},
		{name: "lookup.armpkgs", handler: s.lookups.OnArmpkgs, predicates: []th.Predicate{th.CommandEqual("armpkgs")}},
		{name: "panel.rich", handler: s.administration.OnRich, predicates: []th.Predicate{th.CommandEqual("rich")}},
		{name: "panel.spoiler", handler: s.administration.OnSpoiler, predicates: []th.Predicate{th.CommandEqual("spoiler")}},
		{name: "panel.vmode", handler: s.administration.OnVMode, predicates: []th.Predicate{th.CommandEqual("vmode")}},
		{name: "panel.autodel", handler: s.administration.OnAutoDel, predicates: []th.Predicate{th.CommandEqual("autodel")}},
		{name: "panel.bantime", handler: s.administration.OnBanTime, predicates: []th.Predicate{th.CommandEqual("bantime")}},
		{name: "moderate.mute", handler: s.moderation.OnMute, predicates: []th.Predicate{th.CommandEqual("mute")}},
		{name: "moderate.unmute", handler: s.moderation.OnUnmute, predicates: []th.Predicate{th.CommandEqual("unmute")}},
		{name: "panel.help", handler: s.administration.OnHelp, predicates: []th.Predicate{th.CommandEqual("help")}},
	}
}

func unauthorizedChatAlert(_ i18n.Lang, chat telego.Chat) string {
	return fmt.Sprintf("🚪 已自动退出未授权聊天:%s(id %d,%s)", chat.Title, chat.ID, chat.Type)
}

func (s *Service) onMyChatMember(ctx *th.Context, update telego.Update) error {
	cm := update.MyChatMember
	if cm == nil || cm.Chat.Type == "private" {
		return nil
	}
	switch cm.NewChatMember.MemberStatus() {
	case "left", "kicked":
		return nil
	}
	if s.isKnownChat(cm.Chat.ID) {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	log.Printf("auto-leave: leaving unauthorized chat %d (%q, %s)", cm.Chat.ID, cm.Chat.Title, cm.Chat.Type)
	if err := bot.LeaveChat(c, &telego.LeaveChatParams{ChatID: tu.ID(cm.Chat.ID)}); err != nil {
		log.Printf("auto-leave: failed to leave %d: %v", cm.Chat.ID, err)
		return nil
	}
	s.telegram.Alert(c, s.cfg.AdminLogChatID,
		unauthorizedChatAlert(i18n.FromStored(s.cfg.LangForGroup(0)), cm.Chat))
	return nil
}

func (s *Service) isKnownChat(chatID int64) bool {
	if s.settings == nil {
		return s.cfg.IsKnownChat(chatID)
	}
	if s.settings.IsGroup(chatID) || s.cfg.AdminLogChatID == chatID {
		return true
	}
	for _, configuredFeed := range s.cfg.Feeds {
		if configuredFeed.ChatID == chatID {
			return true
		}
	}
	for _, groupID := range s.settings.GroupIDs() {
		group, _ := s.settings.Group(groupID)
		if s.verification.RequiredChannelID(groupID) == chatID {
			return true
		}
		for _, knownID := range group.KnownChatIDs().Value {
			if knownID == chatID {
				return true
			}
		}
		for _, trustedID := range group.TrustedMemberGroupIDs().Value {
			if trustedID == chatID {
				return true
			}
		}
	}
	return false
}

// LogStartup reports the running version, effective groups, and required Telegram rights.
func (s *Service) LogStartup(ctx context.Context, telegramBot *telego.Bot, identity verify.Identity) {
	log.Printf("verify bot @%s (%s) started — groups=%d", identity.Username, s.version, len(s.settings.GroupIDs()))
	for _, groupID := range s.settings.GroupIDs() {
		group, _ := s.settings.Group(groupID)
		log.Printf("  group %d: required_channel=%d questions=%d timeout=%ds", groupID,
			s.verification.RequiredChannelID(groupID), len(group.Questions().Value), group.TimeoutSeconds().Value)
	}
	go func() {
		s.moderation.LogGroupAdmin(ctx, telegramBot, identity.ID)
		s.verification.LogVerificationAccess(ctx, telegramBot, identity.ID)
	}()
}
