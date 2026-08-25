package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
	registered := false
	source := make(chan telego.Update)
	close(source)
	handler, err := prepareUpdateHandler(
		context.Background(),
		&telego.Bot{},
		func(_ *th.BotHandler) {
			registered = true
		},
		func() (<-chan telego.Update, error) {
			if !registered {
				t.Fatal("long polling started before handlers were registered")
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
	handler, err := prepareUpdateHandler(
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
	handlerDone := make(chan error, 1)
	go func() {
		handlerDone <- handler.Start()
	}()
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
