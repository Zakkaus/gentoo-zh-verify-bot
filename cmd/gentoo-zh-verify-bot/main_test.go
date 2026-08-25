package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

func TestStreamEndedUnexpectedly(t *testing.T) {
	if !streamEndedUnexpectedly(nil) {
		t.Error("nil ctx error should signal an unexpected end => restart")
	}
	if streamEndedUnexpectedly(context.Canceled) {
		t.Error("a cancelled ctx is a graceful shutdown => no restart")
	}
}

func TestPrepareUpdateHandlerRegistersBeforePolling(t *testing.T) {
	source := make(chan telego.Update, 1)
	source <- telego.Update{UpdateID: 1}
	close(source)
	handled := make(chan int, 1)
	var registeredHandler *th.BotHandler
	handler, handlerDone, err := prepareUpdateHandler(
		context.Background(),
		&telego.Bot{},
		func(handler *th.BotHandler) {
			registeredHandler = handler
			handler.Handle(func(_ *th.Context, update telego.Update) error {
				handled <- update.UpdateID
				return nil
			})
		},
		func() (<-chan telego.Update, error) {
			if registeredHandler == nil {
				t.Fatal("long polling started before handlers were registered")
			}
			if !registeredHandler.IsRunning() {
				t.Fatal("long polling started before the handler was consuming updates")
			}
			return source, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if handler == nil {
		t.Fatal("prepareUpdateHandler returned a nil handler")
	}
	if got := <-handled; got != 1 {
		t.Fatalf("handled update ID = %d, want 1", got)
	}
	if err := <-handlerDone; err != nil {
		t.Fatal(err)
	}
	if err := handler.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestForwardUpdatesDrainsConfirmedBufferedUpdateOnStartupCancellation(t *testing.T) {
	ctx := newBlockingCancelContext()
	update := telego.Update{UpdateID: 41}
	source := make(chan telego.Update, 1)
	source <- update
	laterPollConfirmed := make(chan struct{})
	close(laterPollConfirmed)
	handled := make(chan int, 1)
	var registeredHandler *th.BotHandler

	handler, _, err := prepareUpdateHandler(
		ctx,
		&telego.Bot{},
		func(handler *th.BotHandler) {
			registeredHandler = handler
			handler.Handle(func(_ *th.Context, update telego.Update) error {
				handled <- update.UpdateID
				return nil
			})
		},
		func() (<-chan telego.Update, error) {
			<-laterPollConfirmed
			return source, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	waitForBufferedSourceRead(source)
	ctx.cancel()
	close(source)

	var handlerDone <-chan error
	if !registeredHandler.IsRunning() {
		done := make(chan error, 1)
		handlerDone = done
		go func() {
			done <- handler.Start()
		}()
	}
	select {
	case got := <-handled:
		if got != update.UpdateID {
			t.Fatalf("handled update ID = %d, want %d", got, update.UpdateID)
		}
	case err := <-handlerDone:
		t.Fatalf("handler stopped before processing confirmed buffered update: %v", err)
	}
	if err := handler.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestForwardUpdatesDrainsConfirmedBufferedUpdateOnShutdown(t *testing.T) {
	ctx := newBlockingCancelContext()
	update := telego.Update{UpdateID: 73}
	source := make(chan telego.Update, 1)
	source <- update
	laterPollConfirmed := make(chan struct{})
	close(laterPollConfirmed)
	destination := make(chan telego.Update)
	inFlight := make(chan struct{}, 1)
	inFlight <- struct{}{}
	forwardDone := make(chan struct{})
	go func() {
		defer close(forwardDone)
		forwardUpdates(ctx, source, destination, inFlight)
	}()

	<-laterPollConfirmed
	waitForBufferedSourceRead(source)
	ctx.cancel()
	close(source)
	<-inFlight

	got, ok := <-destination
	if !ok {
		t.Fatal("confirmed buffered update was discarded during shutdown")
	}
	if got.UpdateID != update.UpdateID {
		t.Fatalf("forwarded update ID = %d, want %d", got.UpdateID, update.UpdateID)
	}
	<-inFlight
	<-forwardDone
}

type blockingCancelContext struct {
	done     chan struct{}
	canceled atomic.Bool
}

func newBlockingCancelContext() *blockingCancelContext {
	return &blockingCancelContext{done: make(chan struct{})}
}

func (c *blockingCancelContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (c *blockingCancelContext) Done() <-chan struct{} {
	return c.done
}

func (c *blockingCancelContext) Err() error {
	if c.canceled.Load() {
		return context.Canceled
	}
	return nil
}

func (c *blockingCancelContext) Value(any) any {
	return nil
}

func (c *blockingCancelContext) cancel() {
	c.canceled.Store(true)
	c.done <- struct{}{}
}

func waitForBufferedSourceRead(source <-chan telego.Update) {
	for len(source) != 0 {
		runtime.Gosched()
	}
}

func TestUpdateHandlerConcurrencyIsBounded(t *testing.T) {
	const (
		handlerCap = 64
		updateN    = handlerCap + 16
	)
	source := make(chan telego.Update, updateN)
	for id := range updateN {
		source <- telego.Update{UpdateID: id + 1}
	}
	close(source)

	started := make(chan struct{}, updateN)
	release := make(chan struct{})
	handler, handlerDone, err := prepareUpdateHandler(
		context.Background(),
		&telego.Bot{},
		func(handler *th.BotHandler) {
			handler.Handle(func(_ *th.Context, _ telego.Update) error {
				started <- struct{}{}
				<-release
				return nil
			})
		},
		func() (<-chan telego.Update, error) {
			return source, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	for range handlerCap {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("handler concurrency never reached its bound")
		}
	}
	select {
	case <-started:
		t.Fatalf("more than %d update handlers ran concurrently", handlerCap)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-handlerDone; err != nil {
		t.Fatal(err)
	}
	if err := handler.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionOutageObserverUsesDurableHeartbeatOncePerOutage(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "heartbeat.json")
	writeHeartbeatRecord(t, path, now.Add(-25*time.Hour))
	alerts := make(chan time.Duration, 2)
	observer := retentionOutageObserver{
		heartbeatPath: path,
		alert: func(outage time.Duration) {
			alerts <- outage
		},
	}

	observer.observe(now)
	if got := <-alerts; got != 25*time.Hour {
		t.Fatalf("outage = %v, want 25h", got)
	}
	observer.observe(now)
	select {
	case got := <-alerts:
		t.Fatalf("duplicate alert for the same outage: %v", got)
	default:
	}

	writeHeartbeatRecord(t, path, now.Add(-time.Hour))
	observer.observe(now)
	writeHeartbeatRecord(t, path, now.Add(-26*time.Hour))
	observer.observe(now)
	if got := <-alerts; got != 26*time.Hour {
		t.Fatalf("outage after recovery = %v, want 26h", got)
	}
}

func writeHeartbeatRecord(t *testing.T, path string, lastOnline time.Time) {
	t.Helper()
	data, err := json.Marshal(struct {
		LastOnline int64 `json:"last_online"`
	}{LastOnline: lastOnline.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
