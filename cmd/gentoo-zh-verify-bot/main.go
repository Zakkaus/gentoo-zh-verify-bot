package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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
)

// version is set with -ldflags; plain builds use "dev".
var version = "dev"

// Pin retries so transient polling errors do not close the update stream.
const pollRetryInterval = 5 * time.Second

// A live context means the update stream died unexpectedly; exit non-zero so systemd restarts it.
func streamEndedUnexpectedly(ctxErr error) bool { return ctxErr == nil }

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

	sd := os.Getenv("STATE_DIRECTORY")
	cfg, runtimeSettings, err := loadRuntimeState(*configPath, sd)
	if err != nil {
		log.Fatal(err)
	}

	// TELEGRAM_API_URL selects a lower-latency self-hosted Bot API server.
	var botOpts []telego.BotOption
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

	updates, err := bot.UpdatesViaLongPolling(ctx, &telego.GetUpdatesParams{
		Timeout:        30,
		AllowedUpdates: []string{"chat_join_request", "callback_query", "message", "my_chat_member"},
	}, telego.WithLongPollingRetryTimeout(pollRetryInterval)) // survive a transient poll error instead of closing the stream
	if err != nil {
		log.Fatalf("start long polling: %v", err)
	}

	bh, err := th.NewBotHandler(bot, updates)
	if err != nil {
		log.Fatalf("create handler: %v", err)
	}
	// Fatal exits skip defers; the graceful path stops the handler explicitly.

	startedAt := time.Now()
	me, err := bot.GetMe(ctx)
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
		cfg, runtimeSettings, telegram, verification, administration, moderation, lookups, version,
	)
	registration := newRegistrationService(
		ctx, bot, runtimeSettings, cfg, identity.Username, identity.ID,
		func(checkCtx context.Context, groupID int64) {
			moderation.LogGroupSetup(checkCtx, bot, identity.ID, groupID)
		},
	)
	if err := registration.EnsureOwnerClaim(); err != nil {
		log.Printf("WARNING: owner claim is unavailable until durable settings storage is restored: %v", err)
	}
	registration.Register(bh)
	log.Printf("verify bot @%s (%s) started — groups=%d", identity.Username, version, len(runtimeSettings.GroupIDs()))
	go moderation.LogGroupAdmin(ctx, bot, identity.ID)
	application.Register(bh)
	application.SetupCommands(ctx, bot)

	var feeds []*config.FeedConfig // one shared poller fans new bugs + news out to all configured feeds
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

	go lookups.Warm(ctx)                   // warm the package-search cache in the background (cancelled on shutdown)
	go verification.RunHeartbeat(ctx, bot) // liveness probe: pause verification timeouts during a Telegram/network outage and refresh on recovery

	if err := bh.Start(); err != nil {
		log.Fatalf("handler stopped: %v", err)
	}
	if streamEndedUnexpectedly(ctx.Err()) {
		log.Fatal("update stream ended without a shutdown signal — exiting non-zero so systemd restarts us")
	}
	// Freeze timers, then flush state updated by any callback whose own save was interrupted.
	_ = bh.Stop()
	verification.Shutdown() // freeze timers before persisting so no deadline can decline or ban during exit
	if feedDone != nil {
		// Bound shutdown even if the feed's final network call stalls.
		select {
		case <-feedDone:
		case <-time.After(5 * time.Second):
			log.Printf("shutdown: feed state flush timed out")
		}
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
	if status := runtimeSettings.Persistence(); status.LastError != nil {
		log.Printf("WARNING: runtime settings persistence unavailable: %v", status.LastError)
	}
	return store.EffectiveConfig(cfg, runtimeSettings), runtimeSettings, nil
}
