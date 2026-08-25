package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
)

const botAPITimeout = 45 * time.Second

type systemdNotifier struct {
	conn *net.UnixConn

	readyOnce    sync.Once
	readyErr     error
	stoppingOnce sync.Once
	stoppingErr  error
	closeOnce    sync.Once
	closeErr     error
}

func newSystemdNotifier() (*systemdNotifier, error) {
	socket := os.Getenv("NOTIFY_SOCKET")
	if socket == "" {
		return &systemdNotifier{}, nil
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		return nil, err
	}
	return &systemdNotifier{conn: conn}, nil
}

func (n *systemdNotifier) notify(state string) error {
	if n == nil || n.conn == nil {
		return nil
	}
	_, err := n.conn.Write([]byte(state))
	return err
}

func (n *systemdNotifier) ready() error {
	n.readyOnce.Do(func() {
		n.readyErr = n.notify("READY=1")
	})
	return n.readyErr
}

func (n *systemdNotifier) watchdog() error {
	return n.notify("WATCHDOG=1")
}

func (n *systemdNotifier) stopping() error {
	n.stoppingOnce.Do(func() {
		n.stoppingErr = n.notify("STOPPING=1")
	})
	return n.stoppingErr
}

func (n *systemdNotifier) close() error {
	if n == nil || n.conn == nil {
		return nil
	}
	n.closeOnce.Do(func() {
		n.closeErr = n.conn.Close()
	})
	return n.closeErr
}

// runSystemdLifecycle gates readiness on completed startup and watchdog pings on poll-loop progress.
func runSystemdLifecycle(
	ctx context.Context,
	notifier *systemdNotifier,
	startupComplete <-chan struct{},
	progress <-chan struct{},
) error {
	select {
	case <-ctx.Done():
		return notifier.stopping()
	case <-startupComplete:
		if err := notifier.ready(); err != nil {
			return err
		}
	}

	for {
		select {
		case <-ctx.Done():
			return notifier.stopping()
		case _, ok := <-progress:
			if !ok {
				progress = nil
				continue
			}
			if err := notifier.watchdog(); err != nil {
				log.Printf("systemd watchdog notification failed: %v", err)
			}
		}
	}
}

// pollingProgressCaller bounds a stuck getUpdates call and reports every completed attempt.
type pollingProgressCaller struct {
	next     ta.Caller
	timeout  time.Duration
	progress func()
}

func (c pollingProgressCaller) Call(ctx context.Context, url string, data *ta.RequestData) (*ta.Response, error) {
	if !strings.HasSuffix(url, "/getUpdates") {
		return c.next.Call(ctx, url, data)
	}
	timeout := c.timeout
	if timeout <= 0 {
		timeout = botAPITimeout
	}
	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	response, err := c.next.Call(pollCtx, url, data)
	cancel()
	if c.progress != nil {
		c.progress()
	}
	return response, err
}

func signalProgress(progress chan<- struct{}) {
	select {
	case progress <- struct{}{}:
	default:
	}
}

// The standard HTTP caller honors cancellation; 45 seconds also bounds deadline-free timer calls.
func withPollingProgress(progress chan<- struct{}) telego.BotOption {
	return telego.WithAPICaller(pollingProgressCaller{
		next:    ta.HTTPCaller{Client: &http.Client{Timeout: botAPITimeout}},
		timeout: botAPITimeout,
		progress: func() {
			signalProgress(progress)
		},
	})
}
