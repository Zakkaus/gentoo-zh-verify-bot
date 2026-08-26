package bot

import (
	"context"
	"testing"

	"github.com/mymmrac/telego"
)

func privMsg(text string) telego.Update {
	return telego.Update{Message: &telego.Message{Chat: telego.Chat{Type: "private"}, Text: text}}
}

func TestPrivateNonStartPredicate(t *testing.T) {
	// The Gentoo lookups carry the edition prefix, so the generic build expects /gpkg here.
	g := "/" + gentooPrefix
	handled := []string{
		g + "pkg vim", g + "use vim", g + "bug 1", g + "news", "/wiki x", g + "bbs x",
		"/pkgs firefox", "/distro firefox", g + "arm htop", "/armpkgs htop",
		"/kernel", "/man ls", "/cve CVE-2024-3094", "/repology bash",
		"/help", "/ping", "/stats", "/start", "/start verify", g + "pkg@GentooZhVerifyBot vim",
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
	// Non-private messages never match the generic DM predicate.
	if privateNonStart(context.TODO(), telego.Update{Message: &telego.Message{Chat: telego.Chat{Type: "supergroup"}, Text: g + "pkg x"}}) {
		t.Errorf("group message should not match privateNonStart")
	}
}
