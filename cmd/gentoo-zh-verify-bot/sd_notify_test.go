package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	th "github.com/mymmrac/telego/telegohandler"
)

type callerFunc func(context.Context, string, *ta.RequestData) (*ta.Response, error)

func (f callerFunc) Call(ctx context.Context, url string, data *ta.RequestData) (*ta.Response, error) {
	return f(ctx, url, data)
}

func TestSystemdNotifierUnsetIsNoop(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	notifier, err := newSystemdNotifier()
	if err != nil {
		t.Fatal(err)
	}
	if err := notifier.ready(); err != nil {
		t.Fatalf("ready without NOTIFY_SOCKET: %v", err)
	}
	if err := notifier.watchdog(); err != nil {
		t.Fatalf("watchdog without NOTIFY_SOCKET: %v", err)
	}
	if err := notifier.stopping(); err != nil {
		t.Fatalf("stopping without NOTIFY_SOCKET: %v", err)
	}
}

func TestSystemdLifecycleFollowsStartupAndProgress(t *testing.T) {
	path := t.TempDir() + "/notify.sock"
	listener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("NOTIFY_SOCKET", path)

	notifier, err := newSystemdNotifier()
	if err != nil {
		t.Fatal(err)
	}
	defer notifier.close()

	ctx, cancel := context.WithCancel(context.Background())
	startupComplete := make(chan struct{})
	progress := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runSystemdLifecycle(ctx, notifier, startupComplete, progress)
	}()

	expectNoNotify(t, listener)
	close(startupComplete)
	expectNotify(t, listener, "READY=1")
	if err := notifier.ready(); err != nil {
		t.Fatal(err)
	}
	expectNoNotify(t, listener)

	progress <- struct{}{}
	expectNotify(t, listener, "WATCHDOG=1")
	expectNoNotify(t, listener)

	cancel()
	expectNotify(t, listener, "STOPPING=1")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	expectNoNotify(t, listener)
}

func TestPollingProgressCallerSignalsOnlyCompletedPolls(t *testing.T) {
	progress := make(chan struct{}, 1)
	calls := 0
	caller := pollingProgressCaller{
		next: callerFunc(func(context.Context, string, *ta.RequestData) (*ta.Response, error) {
			calls++
			return &ta.Response{Ok: true}, nil
		}),
		timeout: time.Second,
		progress: func() {
			progress <- struct{}{}
		},
	}
	data := &ta.RequestData{BodyRaw: []byte("{}")}
	if _, err := caller.Call(context.Background(), "https://api.telegram.org/botTOKEN/getMe", data); err != nil {
		t.Fatal(err)
	}
	select {
	case <-progress:
		t.Fatal("a non-poll API call signaled update-loop progress")
	default:
	}
	if _, err := caller.Call(context.Background(), "https://api.telegram.org/botTOKEN/getUpdates", data); err != nil {
		t.Fatal(err)
	}
	select {
	case <-progress:
	default:
		t.Fatal("a completed getUpdates call did not signal progress")
	}
	if calls != 2 {
		t.Fatalf("underlying calls = %d, want 2", calls)
	}
}

func TestBlockedBotAPICallDoesNotPreventHandlerShutdown(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-release
	}))
	defer server.Close()
	defer close(release)

	progress := make(chan struct{}, 1)
	bot, err := telego.NewBot(
		"123456:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		withPollingProgress(progress),
		telego.WithAPIServer(server.URL),
	)
	if err != nil {
		t.Fatal(err)
	}
	updates := make(chan telego.Update, 1)
	handler, err := th.NewBotHandler(bot, updates)
	if err != nil {
		t.Fatal(err)
	}
	handler.Handle(func(ctx *th.Context, _ telego.Update) error {
		_, callErr := bot.GetMe(ctx.Context())
		return callErr
	})
	handlerDone := make(chan error, 1)
	go func() {
		handlerDone <- handler.Start()
	}()
	updates <- telego.Update{UpdateID: 1}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler did not enter the blocked Bot API call")
	}
	close(updates)

	stopCtx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := handler.StopWithContext(stopCtx); err != nil {
		t.Fatalf("handler shutdown waited on the blocked Bot API call: %v", err)
	}
	if err := <-handlerDone; err != nil {
		t.Fatal(err)
	}
}

func expectNotify(t *testing.T, listener *net.UnixConn, want string) {
	t.Helper()
	if err := listener.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 128)
	n, _, err := listener.ReadFromUnix(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(buf[:n]); got != want {
		t.Fatalf("notification = %q, want %q", got, want)
	}
}

func expectNoNotify(t *testing.T, listener *net.UnixConn) {
	t.Helper()
	if err := listener.SetReadDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 128)
	_, _, err := listener.ReadFromUnix(buf)
	if err == nil {
		t.Fatal("unexpected systemd notification")
	}
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			t.Fatalf("read notification: %v", err)
		}
	}
}
