package main

import (
	"reflect"
	"testing"
)

// TestPickWikiTitlesDedup verifies case-insensitive dedupe by base topic (so "NVIDIA" vs "NVidia"
// collapse to one entry) with the simplified-Chinese page preferred, other-language pages dropped,
// rank order preserved (zh first, then en), and the cap honoured.
func TestPickWikiTitlesDedup(t *testing.T) {
	g := wikiSource{classify: classifyGentoo}
	// NVIDIA/nvidia-drivers (en) and NVidia/nvidia-drivers/zh-cn (zh) are the same topic by a
	// case-insensitive base -> one entry, the zh page; the /fr translation is dropped.
	got := g.pickWikiTitles([]string{
		"NVIDIA/nvidia-drivers",
		"NVidia/nvidia-drivers/zh-cn",
		"NVIDIA/nvidia-drivers/fr",
	}, 4)
	if want := []string{"NVidia/nvidia-drivers/zh-cn"}; !reflect.DeepEqual(got, want) {
		t.Errorf("gentoo dedup = %v, want %v", got, want)
	}

	a := wikiSource{classify: classifyArch}
	// Arch "NVIDIA" (en) and "Nvidia (简体中文)" (zh) collapse case-insensitively -> the zh page.
	if got := a.pickWikiTitles([]string{"NVIDIA", "Nvidia (简体中文)"}, 4); !reflect.DeepEqual(got, []string{"Nvidia (简体中文)"}) {
		t.Errorf("arch dedup = %v, want [Nvidia (简体中文)]", got)
	}

	// distinct topics are all kept, in order, capped at max.
	if got := a.pickWikiTitles([]string{"A", "B", "C", "D", "E"}, 3); !reflect.DeepEqual(got, []string{"A", "B", "C"}) {
		t.Errorf("cap = %v, want [A B C]", got)
	}
}
