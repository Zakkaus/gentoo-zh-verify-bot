package verify

import (
	"testing"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
)

// A recovery snapshot must not deliver into a pending created after it. Without the owner
// binding the recovery writes its message ID into the replacement, then deletes that message
// as an orphan, leaving a fresh applicant with a full window and no challenge to answer.
func TestDeliveryBoundToItsOwnPending(t *testing.T) {
	v := newTestService(&config.Config{TimeoutSeconds: 240})
	gid, uid := int64(-100), int64(5)
	old := &pending{nonce: "old", lang: i18n.LangEN, deadline: time.Now().Add(time.Hour)}
	fresh := &pending{nonce: "fresh", lang: i18n.LangEN, deadline: time.Now().Add(time.Hour)}
	v.pend[pkey{gid, uid}] = fresh

	if _, ok := v.pendingDMChallenge(gid, uid, old); ok {
		t.Error("a delivery started for a replaced pending must not claim the current one")
	}
	if _, ok := v.pendingDMChallenge(gid, uid, fresh); !ok {
		t.Error("the owning pending must still deliver")
	}
	if _, ok := v.pendingDMChallenge(gid, uid, nil); !ok {
		t.Error("an unbound delivery follows whichever pending is current")
	}

	// A pending replaced by one carrying the same pointer but a new nonce is also stale.
	fresh.nonce = "rotated"
	if _, ok := v.pendingDMChallenge(gid, uid, &pending{nonce: "fresh"}); ok {
		t.Error("a rotated nonce means the challenge was replaced; the old delivery is stale")
	}
}
