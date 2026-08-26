package bot

import (
	"reflect"
	"testing"

	"github.com/mymmrac/telego"
)

// The edition decides the command names, so the names it registers must be the ones that
// actually reach a handler. A prefix that is only half applied would advertise /gpkg and
// dispatch nothing.
func TestEditionCommandsReachTheirHandlers(t *testing.T) {
	fixture := newDispatchFixture(t, 0)
	for _, name := range []string{"pkg", "use", "bug", "news", "bbs", "arm"} {
		command := "/" + gentooPrefix + name
		t.Run(command, func(t *testing.T) {
			got := dispatchRouteNames(t, fixture, telego.Update{Message: &telego.Message{
				Chat: telego.Chat{ID: fixture.groupID, Type: telego.ChatTypeSupergroup},
				From: &telego.User{ID: 801},
				Text: command + " portage",
			}})
			if len(got) != 1 {
				t.Fatalf("%s reached %v, want exactly one handler", command, got)
			}
		})
	}
	// The unqualified names belong to the group in the general edition and must reach nothing.
	if gentooPrefix != "" {
		for _, name := range []string{"pkg", "use", "bug", "news", "bbs", "arm"} {
			command := "/" + name
			got := dispatchRouteNames(t, fixture, telego.Update{Message: &telego.Message{
				Chat: telego.Chat{ID: fixture.groupID, Type: telego.ChatTypeSupergroup},
				From: &telego.User{ID: 801},
				Text: command + " portage",
			}})
			if !reflect.DeepEqual(got, []string{}) && len(got) != 0 {
				t.Errorf("%s reached %v; this edition leaves that name to the group", command, got)
			}
		}
	}
	// Commands every Linux community shares keep their names in both editions.
	for _, command := range []string{"/pkgs", "/distro", "/armpkgs", "/wiki", "/kernel"} {
		got := dispatchRouteNames(t, fixture, telego.Update{Message: &telego.Message{
			Chat: telego.Chat{ID: fixture.groupID, Type: telego.ChatTypeSupergroup},
			From: &telego.User{ID: 801},
			Text: command + " bash",
		}})
		if len(got) != 1 {
			t.Errorf("%s reached %v, want exactly one handler in every edition", command, got)
		}
	}
}
