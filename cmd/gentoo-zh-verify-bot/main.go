package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	botapp "github.com/Zakkaus/gentoo-zh-verify-bot/internal/bot"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/feed"
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

// version is set with -ldflags; plain builds use "dev".
var version = "dev"

// Pin retries so transient polling errors do not close the update stream.
const pollRetryInterval = 5 * time.Second

const shutdownDeadline = 20 * time.Second

const maxConcurrentUpdateHandlers = 64

const telegramUpdateRetention = 24 * time.Hour

type retentionOutageObserver struct {
	heartbeatPath string
	alert         func(time.Duration)

	mu       sync.Mutex
	reported bool
}

func (o *retentionOutageObserver) observe(now time.Time) {
	outage, ok := heartbeatOutage(o.heartbeatPath, now)
	if !ok {
		return
	}
	o.mu.Lock()
	if outage <= telegramUpdateRetention {
		o.reported = false
		o.mu.Unlock()
		return
	}
	if o.reported {
		o.mu.Unlock()
		return
	}
	o.reported = true
	o.mu.Unlock()
	if o.alert != nil {
		o.alert(outage)
	}
}

func heartbeatOutage(path string, now time.Time) (time.Duration, bool) {
	if path == "" {
		return 0, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	var heartbeat struct {
		LastOnline int64 `json:"last_online"`
	}
	if json.Unmarshal(data, &heartbeat) != nil || heartbeat.LastOnline <= 0 {
		return 0, false
	}
	lastOnline := time.Unix(heartbeat.LastOnline, 0)
	if lastOnline.After(now) {
		return 0, false
	}
	return now.Sub(lastOnline), true
}

type outageAwareBot struct {
	*telego.Bot
	observer *retentionOutageObserver
}

func (b *outageAwareBot) GetMe(ctx context.Context) (*telego.User, error) {
	me, err := b.Bot.GetMe(ctx)
	if err == nil {
		b.observer.observe(time.Now())
	}
	return me, err
}

func alertRetentionOutage(
	ctx context.Context,
	bot *telego.Bot,
	cfg *config.Config,
	groupIDs []int64,
	outage time.Duration,
) {
	log.Printf("recovery: Telegram outage exceeded update retention (~%s); alerting group administrators", outage.Round(time.Hour))
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, groupID := range groupIDs {
		target := groupID
		if cfg.AdminLogChatID != 0 {
			target = cfg.AdminLogChatID
		}
		language := i18n.FromStored(cfg.LangForGroup(groupID))
		text := i18n.Messages.Verification.Admin.OutageBacklog.Render(language, groupID)
		if _, err := bot.SendMessage(sendCtx, tu.Message(tu.ID(target), text)); err != nil && ctx.Err() == nil {
			log.Printf("recovery: retention alert for group %d failed: %v", groupID, err)
		}
	}
}

// alertPersistenceProblem tells the operator, in Telegram, that runtime settings are degraded.
func alertPersistenceProblem(ctx context.Context, bot *telego.Bot, cfg *config.Config, settings *store.Settings) {
	status := settings.Persistence()
	if status.LastError == nil {
		return
	}
	log.Printf("WARNING: runtime settings persistence unavailable: %v", status.LastError)
	targets := []int64{cfg.AdminLogChatID}
	if cfg.AdminLogChatID == 0 {
		targets = settings.GroupIDs()
	}
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, target := range targets {
		if target == 0 {
			continue
		}
		language := i18n.FromStored(cfg.LangForGroup(target))
		text := i18n.Messages.Verification.Admin.SettingsDegraded.Render(language, status.LastError.Error())
		if _, err := bot.SendMessage(sendCtx, tu.Message(tu.ID(target), text)); err != nil && ctx.Err() == nil {
			log.Printf("settings-degraded alert to %d failed: %v", target, err)
		}
	}
}

// A live context means the update stream died unexpectedly; exit non-zero so systemd restarts it.
func streamEndedUnexpectedly(ctxErr error) bool { return ctxErr == nil }

type runtimeLifecycle struct {
	handlerDone       <-chan error
	stopHandlers      func(context.Context) error
	waitRegistration  func()
	heartbeatDone     <-chan struct{}
	flushVerification func()
	feedDone          <-chan struct{}
	notifierDone      <-chan error
	shutdownDeadline  time.Duration
}

func runRuntimeLifecycle(ctx context.Context, lifecycle runtimeLifecycle) error {
	var handlerErr error
	handlerStopped := false
	select {
	case handlerErr = <-lifecycle.handlerDone:
		handlerStopped = true
	case <-ctx.Done():
	}
	if handlerStopped && streamEndedUnexpectedly(ctx.Err()) {
		if handlerErr != nil {
			return fmt.Errorf("handler stopped unexpectedly: %w", handlerErr)
		}
		return fmt.Errorf("update stream ended without a shutdown signal — exiting non-zero so systemd restarts us")
	}

	deadline := lifecycle.shutdownDeadline
	if deadline <= 0 {
		deadline = shutdownDeadline
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), deadline)
	defer shutdownCancel()
	log.Printf("shutdown: waiting up to %s to drain fetched updates and in-flight update handlers", deadline)
	if !handlerStopped {
		select {
		case handlerErr = <-lifecycle.handlerDone:
			handlerStopped = true
		case <-shutdownCtx.Done():
			log.Printf("shutdown: fetched updates did not drain before deadline: %v", shutdownCtx.Err())
		}
	}
	if handlerStopped && handlerErr != nil {
		log.Printf("shutdown: handler loop stopped: %v", handlerErr)
	}
	if lifecycle.stopHandlers != nil {
		if err := lifecycle.stopHandlers(shutdownCtx); err != nil {
			log.Printf("shutdown: update handlers did not stop cleanly: %v", err)
		}
	}
	if lifecycle.waitRegistration != nil {
		registrationDone := make(chan struct{})
		go func() {
			lifecycle.waitRegistration()
			close(registrationDone)
		}()
		waitForShutdownComponent(shutdownCtx, "registration timers", registrationDone)
	}
	waitForShutdownComponent(shutdownCtx, "Telegram heartbeat", lifecycle.heartbeatDone)

	log.Printf("shutdown: flushing verification state")
	if lifecycle.flushVerification != nil {
		lifecycle.flushVerification()
	}
	waitForShutdownComponent(shutdownCtx, "feed state flush", lifecycle.feedDone)

	if lifecycle.notifierDone != nil {
		select {
		case err := <-lifecycle.notifierDone:
			if err != nil {
				log.Printf("shutdown: systemd notification failed: %v", err)
			}
		case <-shutdownCtx.Done():
			log.Printf("shutdown: systemd notifier did not stop before deadline: %v", shutdownCtx.Err())
		}
	}
	return nil
}

func exitOnRuntimeError(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func main() {
	configPath := flag.String("config", "/etc/gentoo-zh-verify-bot/config.json", "path to config.json")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("BOT_TOKEN environment variable is required")
	}

	notifier, err := newSystemdNotifier()
	if err != nil {
		log.Fatalf("connect systemd notifier: %v", err)
	}
	defer notifier.close()
	progress := make(chan struct{}, 1)

	sd := os.Getenv("STATE_DIRECTORY")
	cfg, runtimeSettings, err := loadRuntimeState(*configPath, sd)
	if err != nil {
		log.Fatal(err)
	}

	// TELEGRAM_API_URL selects a lower-latency self-hosted Bot API server.
	botOpts := []telego.BotOption{withPollingProgress(progress)}
	if apiURL := strings.TrimSpace(os.Getenv("TELEGRAM_API_URL")); apiURL != "" {
		botOpts = append(botOpts, telego.WithAPIServer(apiURL))
		log.Printf("using Bot API server %s", apiURL)
	}
	bot, err := telego.NewBot(token, botOpts...)
	if err != nil {
		log.Fatalf("create bot: %v", err)
	}
	telegram := tg.New(bot)
	githubToken := os.Getenv("GITHUB_TOKEN")
	lookups := lookup.New(runtimeSettings, telegram, cfg, githubToken)
	if githubToken != "" {
		log.Printf("GITHUB_TOKEN set — GitHub API rate limit raised (~5000/h)")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	startupComplete := make(chan struct{})
	notifierDone := make(chan error, 1)
	go func() {
		notifierDone <- runSystemdLifecycle(ctx, notifier, startupComplete, progress)
	}()

	heartbeatPath := ""
	if sd != "" {
		heartbeatPath = filepath.Join(sd, "heartbeat.json")
	}
	// A settings store that could not be loaded cleanly changes how the bot behaves everywhere.
	// A log line alone leaves the operator to discover that by accident.
	alertPersistenceProblem(ctx, bot, cfg, runtimeSettings)
	outageObserver := &retentionOutageObserver{heartbeatPath: heartbeatPath}
	outageObserver.alert = func(outage time.Duration) {
		alertRetentionOutage(ctx, bot, cfg, runtimeSettings.GroupIDs(), outage)
	}
	heartbeatBot := &outageAwareBot{Bot: bot, observer: outageObserver}
	startedAt := time.Now()
	me, err := heartbeatBot.GetMe(ctx)
	if err != nil {
		log.Fatalf("GetMe failed (required for the verification deep link): %v", err)
	}
	identity := verify.Identity{ID: me.ID, Username: me.Username}
	verification := verify.New(runtimeSettings, telegram, cfg, &i18n.Messages, bot, identity, sd)
	if sd == "" {
		log.Printf("WARNING: STATE_DIRECTORY is unset — persistence is DISABLED: settings changes are runtime-only, and pending verifications, warn counts, and feed cursors will NOT survive a restart (set StateDirectory= in the systemd unit)")
	}
	moderation := moderate.New(runtimeSettings, telegram, cfg, sd)
	administration := panel.New(
		runtimeSettings, telegram, cfg, &i18n.Messages,
		verification, moderation, lookups, version, startedAt,
	)
	application := botapp.New(
		cfg, runtimeSettings, telegram, verification, administration, moderation, lookups,
	)
	registration := newRegistrationService(
		ctx, bot, runtimeSettings, cfg, identity.Username, identity.ID,
		func(checkCtx context.Context, groupID int64) {
			moderation.LogGroupSetup(checkCtx, bot, identity.ID, groupID)
			application.SetupCommands(checkCtx, bot)
		},
		func(checkCtx context.Context, groupID int64) {
			moderation.LogGroupSetup(checkCtx, bot, identity.ID, groupID)
		},
		verification.RemoveGroup,
	)
	registration.onOwnerClaimed = func(checkCtx context.Context) {
		application.SetupCommands(checkCtx, bot)
	}
	if err := registration.EnsureOwnerClaim(); err != nil {
		log.Printf("WARNING: owner claim is unavailable until durable settings storage is restored: %v", err)
	}

	application.SetupCommands(ctx, bot)
	bh, handlerDone, err := prepareUpdateHandler(
		ctx,
		bot,
		func(handler *th.BotHandler) {
			registration.Register(handler)
			application.Register(handler)
		},
		func() (<-chan telego.Update, error) {
			return bot.UpdatesViaLongPolling(ctx, &telego.GetUpdatesParams{
				Timeout:        30,
				AllowedUpdates: []string{"chat_join_request", "callback_query", "message", "my_chat_member"},
			}, telego.WithLongPollingRetryTimeout(pollRetryInterval))
		},
	)
	if err != nil {
		log.Fatalf("start long polling: %v", err)
	}

	var feeds []*config.FeedConfig
	for i := range cfg.Feeds {
		if cfg.Feeds[i].ChatID != 0 {
			feeds = append(feeds, &cfg.Feeds[i])
		} else {
			log.Printf("WARNING: a feed entry has chat_id=0 (missing/invalid) — it is disabled; set its chat_id to the target channel")
		}
	}
	var feedDone chan struct{}
	if len(feeds) > 0 {
		feedService := feed.New(bot, feeds, sd)
		feedDone = make(chan struct{})
		go func() {
			defer close(feedDone)
			feedService.Run(ctx)
		}()
	}

	heartbeatDone := make(chan struct{})
	go lookups.Warm(ctx)
	go func() {
		defer close(heartbeatDone)
		verification.RunHeartbeat(ctx, heartbeatBot)
	}()

	log.Printf("verify bot @%s (%s) started — groups=%d", identity.Username, version, len(runtimeSettings.GroupIDs()))
	close(startupComplete)

	exitOnRuntimeError(runRuntimeLifecycle(ctx, runtimeLifecycle{
		handlerDone:       handlerDone,
		stopHandlers:      bh.StopWithContext,
		waitRegistration:  registration.Wait,
		heartbeatDone:     heartbeatDone,
		flushVerification: verification.Shutdown,
		feedDone:          feedDone,
		notifierDone:      notifierDone,
		shutdownDeadline:  shutdownDeadline,
	}))
}

func prepareUpdateHandler(
	ctx context.Context,
	bot *telego.Bot,
	register func(*th.BotHandler),
	startPolling func() (<-chan telego.Update, error),
) (*th.BotHandler, <-chan error, error) {
	handlerUpdates := make(chan telego.Update)
	inFlight := make(chan struct{}, maxConcurrentUpdateHandlers)
	handler, err := th.NewBotHandler(bot, handlerUpdates)
	if err != nil {
		return nil, nil, err
	}
	handler.Use(func(handlerCtx *th.Context, update telego.Update) error {
		defer func() { <-inFlight }()
		return handlerCtx.Next(update)
	})
	register(handler)

	handlerDone := make(chan error, 1)
	go func() {
		handlerDone <- handler.Start()
	}()
	// Polling must not start until Start has initialized the update consumer.
	for !handler.IsRunning() {
		select {
		case err := <-handlerDone:
			return nil, nil, fmt.Errorf("update handler stopped before polling started: %v", err)
		default:
			runtime.Gosched()
		}
	}

	updates, err := startPolling()
	if err != nil {
		close(handlerUpdates)
		_ = handler.Stop()
		<-handlerDone
		return nil, nil, err
	}
	go forwardUpdates(ctx, updates, handlerUpdates, inFlight)
	return handler, handlerDone, nil
}

func forwardUpdates(
	ctx context.Context,
	source <-chan telego.Update,
	destination chan<- telego.Update,
	inFlight chan struct{},
) {
	defer close(destination)
	// Telego may already have confirmed buffered offsets. Cancellation switches to
	// an uncancelable drain instead of abandoning an update held at any blocking step.
	draining := false
	for {
		var (
			update telego.Update
			ok     bool
		)
		if draining {
			update, ok = <-source
		} else {
			select {
			case <-ctx.Done():
				draining = true
				continue
			case update, ok = <-source:
			}
		}
		if !ok {
			return
		}

		slotAcquired := false
		if !draining {
			select {
			case <-ctx.Done():
				draining = true
			case inFlight <- struct{}{}:
				slotAcquired = true
			}
		}
		if !slotAcquired {
			inFlight <- struct{}{}
		}

		sent := false
		if !draining {
			select {
			case <-ctx.Done():
				draining = true
			case destination <- update:
				sent = true
			}
		}
		if !sent {
			destination <- update
		}
	}
}

func waitForShutdownComponent(ctx context.Context, name string, done <-chan struct{}) {
	if done == nil {
		return
	}
	log.Printf("shutdown: waiting for %s", name)
	select {
	case <-done:
		log.Printf("shutdown: %s complete", name)
	case <-ctx.Done():
		log.Printf("shutdown: %s timed out: %v", name, ctx.Err())
	}
}

func loadRuntimeState(configPath, stateDirectory string) (*config.Config, *store.Settings, error) {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return nil, nil, fmt.Errorf("config: %w", err)
	}
	settingsPath := ""
	if stateDirectory != "" {
		if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
			log.Printf("WARNING: cannot create STATE_DIRECTORY %q (%v) — persistence will not work", stateDirectory, err)
		}
		store.ReclaimTemps(stateDirectory)
		settingsPath = filepath.Join(stateDirectory, "settings.json")
	}
	baseline, err := store.LoadBaseline(configPath, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("settings baseline: %w", err)
	}
	runtimeSettings, err := store.NewSettings(settingsPath, baseline)
	if err != nil {
		return nil, nil, fmt.Errorf("settings: %w", err)
	}
	return cfg, runtimeSettings, nil
}
