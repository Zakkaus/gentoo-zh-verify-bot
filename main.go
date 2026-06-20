package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

// version is the build version, set at build time via
// -ldflags "-X main.version=$(git describe --tags)"; "dev" for a plain `go build`.
var version = "dev"

func main() {
	configPath := flag.String("config", "/etc/gentoo-zh-verify-bot/config.json", "path to config.json")
	flag.Parse()

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("BOT_TOKEN environment variable is required")
	}

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	configurePkg(cfg)
	configureNews(cfg)
	githubToken = os.Getenv("GITHUB_TOKEN") // optional: lifts GitHub API rate limit for /pkg
	if githubToken != "" {
		log.Printf("GITHUB_TOKEN set — GitHub API rate limit raised (~5000/h)")
	}

	bot, err := telego.NewBot(token)
	if err != nil {
		log.Fatalf("create bot: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	updates, err := bot.UpdatesViaLongPolling(ctx, &telego.GetUpdatesParams{
		Timeout:        30,
		AllowedUpdates: []string{"chat_join_request", "callback_query", "message", "my_chat_member"},
	})
	if err != nil {
		log.Fatalf("start long polling: %v", err)
	}

	bh, err := th.NewBotHandler(bot, updates)
	if err != nil {
		log.Fatalf("create handler: %v", err)
	}
	defer func() { _ = bh.Stop() }()

	v := NewVerifier(cfg)
	me, err := bot.GetMe(ctx)
	if err != nil {
		log.Fatalf("GetMe failed (required for the verification deep link): %v", err)
	}
	v.botUsername = me.Username
	v.botID = me.ID
	log.Printf("verify bot @%s (%s) started — groups=%d timeout=%ds", me.Username, version, len(cfg.Groups), cfg.TimeoutSeconds)
	for i := range cfg.Groups {
		g := &cfg.Groups[i]
		log.Printf("  group %d: required_channel=%d questions=%d", g.ID, cfg.requiredChannel(g.ID), len(cfg.questions(g.ID)))
	}
	go v.logGroupAdmin(ctx, bot, me.ID) // non-fatal: report which groups the bot can actually moderate
	v.register(bh)
	setupCommands(ctx, bot, cfg.WarnLimit)
	sd := os.Getenv("STATE_DIRECTORY")
	if sd != "" {
		if err := os.MkdirAll(sd, 0o700); err != nil {
			// Don't crash — persistence just won't work; the save helpers log each failure too.
			log.Printf("WARNING: cannot create STATE_DIRECTORY %q (%v) — persistence will not work", sd, err)
		}
		v.statePath = sd + "/pending.json"
		v.load(bot)
		v.warnPath = sd + "/warns.json"
		v.loadWarns()
		v.acPath = sd + "/antispam.json"
		v.loadAntispam()
		v.vfailPath = sd + "/verifyfail.json"
		v.loadVerifyFails()
	} else {
		log.Printf("WARNING: STATE_DIRECTORY is unset — persistence is DISABLED: pending verifications, warn counts, the /bc state and feed cursors will NOT survive a restart (set StateDirectory= in the systemd unit)")
	}

	var feeds []*FeedConfig // one shared poller fans new bugs + news out to all configured feeds
	for i := range cfg.Feeds {
		if cfg.Feeds[i].ChatID != 0 {
			feeds = append(feeds, &cfg.Feeds[i])
		} else {
			log.Printf("WARNING: a feed entry has chat_id=0 (missing/invalid) — it is disabled; set its chat_id to the target channel")
		}
	}
	if len(feeds) > 0 {
		go runFeeds(ctx, bot, feeds, sd)
	}

	go pkgC.refresh(ctx) // warm the package-search cache in the background (cancelled on shutdown)

	if err := bh.Start(); err != nil {
		log.Fatalf("handler stopped: %v", err)
	}
}
