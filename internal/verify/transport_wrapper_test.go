package verify

import (
	"testing"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/mymmrac/telego"
)

// The heartbeat hands recovery an outage-observing wrapper, not a *telego.Bot. Asserting on the
// concrete type panicked there, which took the whole process down mid-recovery and left every
// pending applicant to time out on a challenge nobody re-sent.
type wrappedBot struct {
	*telego.Bot
	inner *telego.Bot
}

func (w wrappedBot) Unwrap() *telego.Bot { return w.inner }

func TestTransportAcceptsAWrappedClient(t *testing.T) {
	raw, err := telego.NewBot("1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", telego.WithDiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := botClient(wrappedBot{Bot: raw, inner: raw}); !ok || got != raw {
		t.Errorf("botClient did not unwrap the wrapper: got=%p ok=%v", got, ok)
	}
	if got, ok := botClient(raw); !ok || got != raw {
		t.Errorf("botClient rejected a plain client: ok=%v", ok)
	}
}

// A caller carrying neither a client nor an unwrap must not take the process down.
type opaqueBot struct{ verifyBot }

func TestTransportDoesNotPanicOnAnUnknownCaller(t *testing.T) {
	v := newTestService(&config.Config{GroupIDs: []int64{-100}, Lang: "en"})
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("resolving a transport panicked: %v", r)
		}
	}()
	if got, ok := botClient(opaqueBot{}); ok || got != nil {
		t.Errorf("botClient invented a client for an unknown caller: got=%p ok=%v", got, ok)
	}
	_ = v.verificationTransport(opaqueBot{})
	_ = i18n.LangEN
}
