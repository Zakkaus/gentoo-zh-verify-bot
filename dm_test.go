package main

import (
	"context"
	"testing"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/mymmrac/telego"
)

func privMsg(text string) telego.Update {
	return telego.Update{Message: &telego.Message{Chat: telego.Chat{Type: "private"}, Text: text}}
}

func TestDMRouting(t *testing.T) {
	handled := []string{
		"/pkg vim", "/use vim", "/bug 1", "/news", "/wiki x", "/bbs x",
		"/pkgs firefox", "/distro firefox", "/arm htop", "/armpkgs htop",
		"/help", "/ping", "/stats", "/start", "/start verify", "/pkg@GentooZhVerifyBot vim",
	}
	for _, m := range handled {
		if privateNonStart(context.TODO(), privMsg(m)) {
			t.Errorf("%q should reach its handler, not the auto-reply", m)
		}
	}
	autoReply := []string{"/sb", "/ban", "/warn", "/clearwarn", "/bc", "/rich", "/autodel", "/stop", "hello", "随便聊聊"}
	for _, m := range autoReply {
		if !privateNonStart(context.TODO(), privMsg(m)) {
			t.Errorf("%q should get the auto-reply", m)
		}
	}
	// a non-private chat never matches privateNonStart
	if privateNonStart(context.TODO(), telego.Update{Message: &telego.Message{Chat: telego.Chat{Type: "supergroup"}, Text: "/pkg x"}}) {
		t.Errorf("group message should not match privateNonStart")
	}
}

func TestPrivateQueryRate(t *testing.T) {
	v := NewVerifier(&config.Config{PrivateQueryPerMin: 3})
	pass := 0
	for range 5 {
		if v.queryRateOK(7) {
			pass++
		}
	}
	if pass != 3 {
		t.Errorf("user 7: %d/5 allowed, want 3", pass)
	}
	if !v.queryRateOK(8) {
		t.Error("user 8 should be allowed")
	}
}
