package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
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
	flag.Parse()

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("BOT_TOKEN environment variable is required")
	}

	cfg, err := (config.LoadConfig(*configPath))
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	configurePkg(cfg)
	configureNews(cfg)
	githubToken = os.Getenv("GITHUB_TOKEN") // optional: lifts GitHub API rate limit for /pkg
	if githubToken != "" {
		log.Printf("GITHUB_TOKEN set — GitHub API rate limit raised (~5000/h)")
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

	v := NewVerifier(cfg)
	me, err := bot.GetMe(ctx)
	if err != nil {
		log.Fatalf("GetMe failed (required for the verification deep link): %v", err)
	}
	v.botUsername = me.Username
	v.botID = me.ID
	v.probe = bot // liveness prober for the on-demand reachability check in the expiry path
	log.Printf("verify bot @%s (%s) started — groups=%d timeout=%ds", me.Username, version, len(cfg.Groups), cfg.TimeoutSeconds)
	for i := range cfg.Groups {
		g := &cfg.Groups[i]
		log.Printf("  group %d: required_channel=%d questions=%d", g.ID, cfg.RequiredChannel(g.ID), len(cfg.QuestionsFor(g.ID)))
	}
	go v.logGroupAdmin(ctx, bot, me.ID) // non-fatal: report which groups the bot can actually moderate
	v.register(bh)
	setupCommands(ctx, bot, cfg.WarnLimit)
	sd := os.Getenv("STATE_DIRECTORY")
	if sd != "" {
		if err := os.MkdirAll(sd, 0o700); err != nil {
			// Persistence failure is non-fatal and logged by every save.
			log.Printf("WARNING: cannot create STATE_DIRECTORY %q (%v) — persistence will not work", sd, err)
		}
		// Remove temp files orphaned between atomic creation and rename.
		store.ReclaimTemps(sd)
		v.statePath = sd + "/pending.json"
		v.hbPath = sd + "/heartbeat.json"
		v.load(bot)
		v.warnPath = sd + "/warns.json"
		v.loadWarns()
		v.acPath = sd + "/antispam.json"
		v.loadAntispam()
		v.vfailPath = sd + "/verifyfail.json"
		v.loadVerifyFails()
		v.settingsPath = sd + "/settings.json"
		v.loadSettings() // restore a persisted /stop (verification paused) across restarts
		v.agentPath = sd + "/agents.json"
		v.loadAgents() // keep the AI-agent tally across restarts so /stats totals stay meaningful
	} else {
		log.Printf("WARNING: STATE_DIRECTORY is unset — persistence is DISABLED: pending verifications, warn counts, the /bc state and feed cursors will NOT survive a restart (set StateDirectory= in the systemd unit)")
	}

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
		feedDone = make(chan struct{})
		go func() {
			defer close(feedDone)
			runFeeds(ctx, bot, feeds, sd)
		}()
	}

	go pkgC.refresh(ctx)        // warm the package-search cache in the background (cancelled on shutdown)
	go v.runHeartbeat(ctx, bot) // liveness probe: pause verification timeouts during a Telegram/network outage and refresh on recovery

	if err := bh.Start(); err != nil {
		log.Fatalf("handler stopped: %v", err)
	}
	if streamEndedUnexpectedly(ctx.Err()) {
		log.Fatal("update stream ended without a shutdown signal — exiting non-zero so systemd restarts us")
	}
	// Freeze timers, then flush state updated by any callback whose own save was interrupted.
	_ = bh.Stop()
	v.stopForShutdown() // freeze pending timers so a verification deadline firing during exit can't wrongly decline/ban
	v.save()
	v.saveVerifyFails()
	v.saveHeartbeat() // record a clean exit time so the next start sees a recent heartbeat, not a false outage
	if feedDone != nil {
		// Bound shutdown even if the feed's final network call stalls.
		select {
		case <-feedDone:
		case <-time.After(5 * time.Second):
			log.Printf("shutdown: feed state flush timed out")
		}
	}
}
