package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	th "github.com/mymmrac/telego/telegohandler"
)

type fakeAdminBot struct {
	member       telego.ChatMember
	memberErr    error
	lastSendText string
}

func newFakeAdminBot() *fakeAdminBot { return &fakeAdminBot{} }

func fakeAdminResponse(value any, err error) (*ta.Response, error) {
	if err != nil {
		return nil, err
	}
	if value == nil {
		return &ta.Response{Ok: true}, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &ta.Response{Ok: true, Result: raw}, nil
}

func (b *fakeAdminBot) Call(_ context.Context, url string, data *ta.RequestData) (*ta.Response, error) {
	method := url[strings.LastIndexByte(url, '/')+1:]
	switch method {
	case "getChatMember":
		if b.memberErr != nil {
			return nil, b.memberErr
		}
		return fakeAdminResponse(b.member, nil)
	case "sendMessage":
		var wire struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(data.BodyRaw, &wire); err != nil {
			return nil, err
		}
		b.lastSendText = wire.Text
		return fakeAdminResponse(&telego.Message{MessageID: 1}, nil)
	case "deleteMessage":
		return fakeAdminResponse(nil, nil)
	default:
		return nil, fmt.Errorf("unexpected Telegram method %q", method)
	}
}

func newAPITestBot(t *testing.T, caller ta.Caller) *telego.Bot {
	t.Helper()
	bot, err := telego.NewBot("1:"+strings.Repeat("a", 35), telego.WithAPICaller(caller), telego.WithDiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	return bot
}

func runFakeHandler(t *testing.T, bot *telego.Bot, handler th.Handler, update telego.Update) {
	t.Helper()
	updates := make(chan telego.Update, 1)
	botHandler, err := th.NewBotHandler(bot, updates)
	if err != nil {
		t.Fatal(err)
	}
	handled := make(chan error, 1)
	botHandler.Handle(func(ctx *th.Context, update telego.Update) error {
		err := handler(ctx, update)
		handled <- err
		return err
	})
	started := make(chan error, 1)
	go func() { started <- botHandler.Start() }()
	updates <- update
	close(updates)
	if err := <-handled; err != nil {
		t.Fatalf("handler returned %v", err)
	}
	if err := <-started; err != nil {
		t.Fatalf("bot handler returned %v", err)
	}
}

func startFakeHandler(t *testing.T, bot *telego.Bot, handler th.Handler, update telego.Update) <-chan error {
	t.Helper()
	updates := make(chan telego.Update, 1)
	botHandler, err := th.NewBotHandler(bot, updates)
	if err != nil {
		t.Fatal(err)
	}
	handled := make(chan error, 1)
	botHandler.Handle(func(ctx *th.Context, update telego.Update) error {
		err := handler(ctx, update)
		handled <- err
		return err
	})
	done := make(chan error, 1)
	go func() {
		startErr := botHandler.Start()
		if handlerErr := <-handled; handlerErr != nil {
			done <- handlerErr
			return
		}
		done <- startErr
	}()
	updates <- update
	close(updates)
	return done
}
