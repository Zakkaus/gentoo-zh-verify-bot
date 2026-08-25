package lookup

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
)

func TestGetNewsAvailability(t *testing.T) {
	newsC.mu.Lock()
	oldItems, oldFetched, oldLoading := newsC.items, newsC.fetched, newsC.loading
	newsC.mu.Unlock()
	oldURL := newsURL
	t.Cleanup(func() {
		newsC.mu.Lock()
		newsC.items, newsC.fetched, newsC.loading = oldItems, oldFetched, oldLoading
		newsC.mu.Unlock()
		newsURL = oldURL
	})

	tests := []struct {
		name          string
		status        int
		body          string
		seed          []NewsItem
		wantAvailable bool
		wantItems     int
	}{
		{
			name:          "empty index answered",
			status:        http.StatusOK,
			wantAvailable: true,
		},
		{
			name:          "index answered with news",
			status:        http.StatusOK,
			body:          `<a href="/support/news-items/2026-08-24-test.html">Test news<`,
			wantAvailable: true,
			wantItems:     1,
		},
		{
			name:      "HTTP failure with stale data",
			status:    http.StatusServiceUnavailable,
			seed:      []NewsItem{{Date: "2026-08-23", Title: "Cached", URL: "https://example.test/cached"}},
			wantItems: 1,
		},
		{
			name:   "markup drift",
			status: http.StatusOK,
			body:   `<html><body>layout changed</body></html>`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			newsURL = srv.URL
			newsC.mu.Lock()
			newsC.items, newsC.fetched, newsC.loading = tt.seed, time.Time{}, false
			newsC.mu.Unlock()

			items, available := GetNews(context.Background())
			if available != tt.wantAvailable {
				t.Errorf("GetNews() availability = %v, want %v", available, tt.wantAvailable)
			}
			if len(items) != tt.wantItems {
				t.Errorf("GetNews() returned %d items, want %d", len(items), tt.wantItems)
			}
		})
	}
}

func TestRenderNewsAvailability(t *testing.T) {
	item := NewsItem{Date: "2026-08-24", Title: "Kernel update", URL: "https://example.test/kernel"}
	tests := []struct {
		name      string
		arg       string
		items     []NewsItem
		available bool
		want      []string
		notWant   string
	}{
		{
			name:      "authoritative empty result",
			arg:       "missing",
			available: true,
			want:      []string{"没找到匹配的新闻。"},
			notWant:   "暂时无法",
		},
		{
			name:    "index unavailable",
			arg:     "missing",
			want:    []string{"暂时无法获取新闻列表", "请稍后重试"},
			notWant: "没找到匹配的新闻",
		},
		{
			name:      "available filtered miss",
			arg:       "missing",
			items:     []NewsItem{item},
			available: true,
			want:      []string{"没找到匹配的新闻。"},
		},
		{
			name:    "stale hit is incomplete",
			arg:     "kernel",
			items:   []NewsItem{item},
			want:    []string{"Kernel update", "以上结果可能不完整", "请稍后重试"},
			notWant: "没找到匹配的新闻",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderNews(i18n.LangZH, tt.arg, tt.items, tt.available)
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("renderNews() = %q, want substring %q", got, want)
				}
			}
			if tt.notWant != "" && strings.Contains(got, tt.notWant) {
				t.Errorf("renderNews() = %q, unwanted substring %q", got, tt.notWant)
			}
		})
	}
}
