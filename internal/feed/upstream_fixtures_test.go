package feed

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func feedUpstreamFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("../../testdata/upstream/" + name)
	if err != nil {
		t.Fatalf("read upstream fixture %q: %v", name, err)
	}
	return body
}

func TestBugzillaUserFieldsLiveFixture(t *testing.T) {
	fields := map[string]bool{}
	for _, field := range strings.Split(bugFields, ",") {
		fields[field] = true
	}
	for _, field := range []string{"assigned_to", "creator", "assigned_to_detail", "creator_detail"} {
		if !fields[field] {
			t.Errorf("bugFields does not request %q", field)
		}
	}

	bugs, ok := fetchRecentBugsWith(context.Background(), 0, func(_ context.Context, u string, dst any) error {
		if !strings.Contains(u, "&order=bug_id%20DESC&limit=1") {
			return fmt.Errorf("unexpected newest-bug URL: %s", u)
		}
		return json.Unmarshal(feedUpstreamFixture(t, "bug-newest-users.json"), dst)
	})
	if !ok || len(bugs) != 1 {
		t.Fatalf("newest Bugzilla fixture = ok %v bugs %+v", ok, bugs)
	}
	if bugs[0].ID != 981378 || bugs[0].AssignedTo.RealName != "Gentoo Toolchain Maintainers" ||
		bugs[0].Creator.RealName != "Nick Bowler" {
		t.Fatalf("Bugzilla user details decoded incorrectly: %+v", bugs[0])
	}
}

func TestBugzillaPaginationLiveFixture(t *testing.T) {
	bugs, ok := fetchRecentBugsWith(context.Background(), 981278, func(_ context.Context, u string, dst any) error {
		if !strings.Contains(u, "&f1=bug_id&o1=greaterthan&v1=981278&order=bug_id%20ASC&limit=100") {
			return fmt.Errorf("unexpected catch-up URL: %s", u)
		}
		return json.Unmarshal(feedUpstreamFixture(t, "bug-batch.json"), dst)
	})
	if !ok || len(bugs) != 100 || bugs[0].ID != 981279 || bugs[len(bugs)-1].ID != 981378 {
		t.Fatalf("Bugzilla catch-up fixture = ok %v count %d range %d..%d",
			ok, len(bugs), bugs[0].ID, bugs[len(bugs)-1].ID)
	}
	for i := 1; i < len(bugs); i++ {
		if bugs[i].ID <= bugs[i-1].ID {
			t.Fatalf("Bugzilla batch is not ascending at %d: %d then %d", i, bugs[i-1].ID, bugs[i].ID)
		}
	}
}

func TestBugzillaAuthoritativeEmptyLiveFixture(t *testing.T) {
	bugs, ok := fetchRecentBugsWith(context.Background(), 999999999, func(_ context.Context, u string, dst any) error {
		if !strings.Contains(u, "&f1=bug_id&o1=greaterthan&v1=999999999&order=bug_id%20ASC&limit=100") {
			return fmt.Errorf("unexpected empty-batch URL: %s", u)
		}
		return json.Unmarshal(feedUpstreamFixture(t, "bug-zero-users.json"), dst)
	})
	if !ok || len(bugs) != 0 {
		t.Fatalf("empty Bugzilla fixture = ok %v bugs %+v", ok, bugs)
	}
}

func TestBugzillaTrackedQueryLiveFixture(t *testing.T) {
	bugs, ok := fetchBugsByIDWith(context.Background(), []int{981377, 981378}, func(_ context.Context, u string, dst any) error {
		if !strings.HasSuffix(u, "&id=981377,981378") {
			return fmt.Errorf("unexpected tracked-bug URL: %s", u)
		}
		return json.Unmarshal(feedUpstreamFixture(t, "bug-by-id-users.json"), dst)
	})
	if !ok || len(bugs) != 2 || bugs[0].AssignedTo.display() == "" || bugs[1].Creator.display() == "" {
		t.Fatalf("tracked Bugzilla fixture decoded incorrectly: ok=%v bugs=%+v", ok, bugs)
	}
}
