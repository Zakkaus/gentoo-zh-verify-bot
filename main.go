package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

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
		AllowedUpdates: []string{"chat_join_request", "callback_query", "message"},
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
	log.Printf("verify bot @%s started — groups=%d channel=%d timeout=%ds questions=%d",
		me.Username, len(cfg.GroupIDs), cfg.RequiredChannelID, cfg.TimeoutSeconds, len(cfg.Questions))
	v.register(bh)
	setupCommands(ctx, bot)
	sd := os.Getenv("STATE_DIRECTORY")
	if sd != "" {
		v.statePath = sd + "/pending.json"
		v.load(bot)
	}

	if cfg.FeedChatID != 0 { // auto-post new Gentoo bugs + news to a channel/group
		interval := 5 * time.Minute
		if cfg.FeedIntervalSeconds >= 60 {
			interval = time.Duration(cfg.FeedIntervalSeconds) * time.Second
		}
		feedPath := ""
		if sd != "" {
			feedPath = sd + "/feed.json"
		}
		go runFeed(ctx, bot, cfg.FeedChatID, interval, feedPath)
	}

	go pkgC.refresh(context.Background()) // warm the package-search cache in the background

	if err := bh.Start(); err != nil {
		log.Fatalf("handler stopped: %v", err)
	}
}
