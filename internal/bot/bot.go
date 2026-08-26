// Package bot owns Telegram handler wiring and process-level bot diagnostics.
package bot

import (
	"log"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/lookup"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/moderate"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/panel"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/tg"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/verify"
	th "github.com/mymmrac/telego/telegohandler"
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
	verification   *verify.Service
	administration *panel.Panel
	moderation     *moderate.Service
	lookups        *lookup.Service
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
) *Service {
	return &Service{
		cfg:            cfg,
		settings:       settings,
		verification:   verification,
		administration: administration,
		moderation:     moderation,
		lookups:        lookups,
		dm: &dmHandler{
			cfg:            cfg,
			settings:       settings,
			telegram:       telegram,
			last:           make(map[int64]time.Time),
			catalogueReply: isBuiltInPrivateReply(cfg.PrivateReply),
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
	registerHandlerRoutes(bh, s.handlerRoutes())
}

func registerHandlerRoutes(bh *th.BotHandler, routes []handlerRoute) {
	for _, route := range routes {
		bh.Handle(route.handler, route.predicates...)
	}
}

func (s *Service) handlerRoutes() []handlerRoute {
	return []handlerRoute{
		{name: "verify.answer", handler: s.verification.OnAnswer, predicates: []th.Predicate{th.CallbackDataPrefix(verify.AnswerCallbackPrefix)}},
		{name: "verify.admin_action", handler: s.verification.OnAdminAction, predicates: []th.Predicate{th.CallbackDataPrefix(verify.AdminCallbackPrefix)}},
		{name: "verify.channel_recheck", handler: s.verification.OnChannelRecheck, predicates: []th.Predicate{th.CallbackDataPrefix(verify.ChannelRecheckCallbackPrefix)}},
		{name: "panel.settings_callback", handler: s.administration.OnSettingsCallback, predicates: []th.Predicate{th.CallbackDataPrefix(panel.SettingsCallbackPrefix)}},
		{name: "verify.join_request", handler: s.verification.OnJoinRequest, predicates: []th.Predicate{th.AnyChatJoinRequest()}},
		{name: "verify.member_joined", handler: s.verification.OnMemberJoined, predicates: []th.Predicate{th.AnyChatMember()}},
		{name: "panel.chat_shared", handler: s.administration.OnPanelChatShared, predicates: []th.Predicate{s.administration.PanelChatSharedDM}},
		{name: "panel.input", handler: s.administration.OnPanelInput, predicates: []th.Predicate{s.administration.PanelInputDM}},
		{name: "verify.kernel_answer", handler: s.verification.OnKernelAnswer, predicates: []th.Predicate{s.verification.KernelAnswerDM}},
		{name: "bot.private_dm", handler: s.dm.onPrivateDM, predicates: []th.Predicate{privateNonStart}},
		{name: "moderate.sb", handler: s.moderation.OnPurge, predicates: []th.Predicate{th.CommandEqual("sb")}},
		{name: "moderate.ban", handler: s.moderation.OnBan, predicates: []th.Predicate{th.CommandEqual("ban")}},
		{name: "moderate.warn", handler: s.moderation.OnWarn, predicates: []th.Predicate{th.CommandEqual("warn")}},
		{name: "moderate.clearwarn", handler: s.moderation.OnClearWarn, predicates: []th.Predicate{th.CommandEqual("clearwarn")}},
		{name: "moderate.bc", handler: s.moderation.OnBC, predicates: []th.Predicate{th.CommandEqual("bc")}},
		{name: "panel.ping", handler: s.administration.OnPing, predicates: []th.Predicate{th.CommandEqual("ping")}},
		{name: "panel.start", handler: s.administration.OnStart, predicates: []th.Predicate{th.CommandEqual("start")}},
		{name: "panel.settings", handler: s.administration.OnSettings, predicates: []th.Predicate{th.CommandEqual("settings")}},
		{name: "panel.stop", handler: s.administration.OnStop, predicates: []th.Predicate{th.CommandEqual("stop")}},
		{name: "panel.stats", handler: s.administration.OnStats, predicates: []th.Predicate{th.CommandEqual("stats")}},
		{name: "lookup.pkg", handler: s.lookups.OnPkg, predicates: []th.Predicate{th.CommandEqual(gentooPrefix + "pkg")}},
		{name: "lookup.use", handler: s.lookups.OnUse, predicates: []th.Predicate{th.CommandEqual(gentooPrefix + "use")}},
		{name: "lookup.bug", handler: s.lookups.OnBug, predicates: []th.Predicate{th.CommandEqual(gentooPrefix + "bug")}},
		{name: "lookup.news", handler: s.lookups.OnNews, predicates: []th.Predicate{th.CommandEqual(gentooPrefix + "news")}},
		{name: "lookup.wiki", handler: s.lookups.OnWiki, predicates: []th.Predicate{th.CommandEqual("wiki")}},
		{name: "lookup.bbs", handler: s.lookups.OnBbs, predicates: []th.Predicate{th.CommandEqual(gentooPrefix + "bbs")}},
		{name: "lookup.pkgs", handler: s.lookups.OnPkgs, predicates: []th.Predicate{th.CommandEqual("pkgs")}},
		{name: "lookup.distro", handler: s.lookups.OnPkgs, predicates: []th.Predicate{th.CommandEqual("distro")}},
		{name: "lookup.arm", handler: s.lookups.OnArm, predicates: []th.Predicate{th.CommandEqual(gentooPrefix + "arm")}},
		{name: "lookup.armpkgs", handler: s.lookups.OnArmpkgs, predicates: []th.Predicate{th.CommandEqual("armpkgs")}},
		{name: "lookup.kernel", handler: s.lookups.OnKernel, predicates: []th.Predicate{th.CommandEqual("kernel")}},
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
