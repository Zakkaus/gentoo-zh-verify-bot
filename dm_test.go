package main

import (
	"testing"

	"github.com/mymmrac/telego"
)

func privMsg(text string) telego.Update {
	return telego.Update{Message: &telego.Message{Chat: telego.Chat{Type: "private"}, Text: text}}
}

// TestDMRouting verifies which private-chat messages reach a command handler (privateNonStart
// returns false) versus get the unified auto-reply (true): every member command and the
// /start deep link are handled; admin/moderation commands and plain text get the auto-reply.
func TestDMRouting(t *testing.T) {
	handled := []string{
		"/pkg vim", "/use vim", "/bug 1", "/news", "/wiki x", "/bbs x",
		"/pkgs firefox", "/distro firefox", "/arm htop", "/armpkgs htop",
		"/help", "/ping", "/stats", "/start", "/start verify", "/pkg@GentooZhVerifyBot vim",
	}
	for _, m := range handled {
		if privateNonStart(nil, privMsg(m)) {
			t.Errorf("%q should reach its handler, not the auto-reply", m)
		}
	}
	autoReply := []string{"/sb", "/ban", "/warn", "/clearwarn", "/bc", "/rich", "/autodel", "/stop", "hello", "随便聊聊"}
	for _, m := range autoReply {
		if !privateNonStart(nil, privMsg(m)) {
			t.Errorf("%q should get the auto-reply", m)
		}
	}
	// a non-private chat never matches privateNonStart
	if privateNonStart(nil, telego.Update{Message: &telego.Message{Chat: telego.Chat{Type: "supergroup"}, Text: "/pkg x"}}) {
		t.Errorf("group message should not match privateNonStart")
	}
}
