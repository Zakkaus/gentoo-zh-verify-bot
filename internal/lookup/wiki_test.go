package lookup

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
)

func TestSearchTransientNotDefinitive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := searchTitles(ctx, wikiSources[0], "anything", 4); ok {
		t.Error("searchTitles must return ok=false on a fetch failure (not a false 'no entries')")
	}
	if _, ok := searchArchcn(ctx, "anything", 5); ok {
		t.Error("searchArchcn must return ok=false on a fetch failure (not a false 'no results')")
	}
}

func TestPickWikiTitlesDedup(t *testing.T) {
	g := wikiSource{classify: classifyGentoo}
	// Case-insensitive topics prefer zh-cn and drop unsupported translations.
	got := g.pickWikiTitles(i18n.LangZH, []string{
		"NVIDIA/nvidia-drivers",
		"NVidia/nvidia-drivers/zh-cn",
		"NVIDIA/nvidia-drivers/fr",
	}, 4)
	if want := []string{"NVidia/nvidia-drivers/zh-cn"}; !reflect.DeepEqual(got, want) {
		t.Errorf("gentoo dedup = %v, want %v", got, want)
	}

	a := wikiSource{classify: classifyArch}
	// The localized Arch title must replace its English base topic.
	if got := a.pickWikiTitles(i18n.LangZH, []string{"NVIDIA", "Nvidia (简体中文)"}, 4); !reflect.DeepEqual(got, []string{"Nvidia (简体中文)"}) {
		t.Errorf("arch dedup = %v, want [Nvidia (简体中文)]", got)
	}

	if got := a.pickWikiTitles(i18n.LangZH, []string{"A", "B", "C", "D", "E"}, 3); !reflect.DeepEqual(got, []string{"A", "B", "C"}) {
		t.Errorf("cap = %v, want [A B C]", got)
	}
}

func TestWikiResultNotice(t *testing.T) {
	tests := []struct {
		name  string
		found bool
		srcOK []bool
		want  string
	}{
		{
			name:  "complete miss",
			srcOK: []bool{true, true},
			want:  "\n\n没找到相关条目，换个关键词试试？",
		},
		{
			name:  "Gentoo unavailable",
			srcOK: []bool{false, true},
			want:  "\n\n以下来源暂时无法查询，结果可能不完整：Gentoo Wiki。请稍后重试。",
		},
		{
			name:  "Arch unavailable with a hit",
			found: true,
			srcOK: []bool{true, false},
			want:  "\n\n以下来源暂时无法查询，结果可能不完整：Arch Wiki。请稍后重试。",
		},
		{
			name:  "all unavailable",
			srcOK: []bool{false, false},
			want:  "\n\n以下来源暂时无法查询，结果可能不完整：Gentoo Wiki、Arch Wiki。请稍后重试。",
		},
		{
			name:  "complete hit",
			found: true,
			srcOK: []bool{true, true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wikiResultNotice(i18n.LangZH, tt.found, tt.srcOK)
			if got != tt.want {
				t.Errorf("wikiResultNotice() = %q, want %q", got, tt.want)
			}
			if strings.Contains(got, "没找到") && (!tt.srcOK[0] || !tt.srcOK[1]) {
				t.Errorf("unavailable source produced a definitive miss: %q", got)
			}
		})
	}
}
