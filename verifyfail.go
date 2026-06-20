package main

import (
	"encoding/json"
	"log"
	"os"
	"time"
)

// vfailRec tracks an applicant's failed-verification strikes and the time of the last one,
// so re-applying too soon can be throttled and a repeat-offender can be auto-banned.
type vfailRec struct {
	count int
	last  time.Time
}

// vfailDisk is the on-disk form (a struct key isn't JSON-friendly, so we store a slice).
type vfailDisk struct {
	GroupID int64 `json:"group_id"`
	UserID  int64 `json:"user_id"`
	Count   int   `json:"count"`
	Last    int64 `json:"last"`
}

// vfailMax bounds the strike map; cleared wholesale past the cap (entries are cheap and the
// real anti-spam teeth is the auto-ban, so a wholesale reset under a flood is acceptable).
const vfailMax = 50000

// verifyFailWindow is the rolling window over which verification failures count toward the
// auto-ban: a failure older than this is forgotten, so only sustained failures (a spammer
// retrying) reach the threshold, while a genuine user's occasional mistakes age out.
const verifyFailWindow = 6 * time.Hour

func (v *Verifier) loadVerifyFails() {
	if v.vfailPath == "" {
		return
	}
	data, err := os.ReadFile(v.vfailPath)
	if err != nil {
		return
	}
	var recs []vfailDisk
	if err := json.Unmarshal(data, &recs); err != nil {
		log.Printf("verifyfail load: %v", err)
		return
	}
	v.mu.Lock()
	for _, r := range recs {
		if r.Count > 0 {
			v.vfail[pkey{r.GroupID, r.UserID}] = &vfailRec{count: r.Count, last: time.Unix(r.Last, 0)}
		}
	}
	n := len(v.vfail)
	v.mu.Unlock()
	if n > 0 {
		log.Printf("restored %d verification-strike record(s)", n)
	}
}

func (v *Verifier) saveVerifyFails() {
	if v.vfailPath == "" {
		return
	}
	v.mu.Lock()
	recs := make([]vfailDisk, 0, len(v.vfail))
	for k, r := range v.vfail {
		if r.count > 0 {
			recs = append(recs, vfailDisk{GroupID: k.gid, UserID: k.uid, Count: r.count, Last: r.last.Unix()})
		}
	}
	v.mu.Unlock()
	writeJSONFile(v.vfailPath, recs)
}

// recordVerifyFail registers a failed verification for (gid,uid) and reports the new strike
// count and whether it has reached the auto-ban threshold (cfg.VerifyMaxFails; negative =>
// never). Persisted so a restart doesn't wipe a spammer's strikes.
func (v *Verifier) recordVerifyFail(gid, uid int64) (count int, ban bool) {
	v.mu.Lock()
	key := pkey{gid, uid}
	r := v.vfail[key]
	if r == nil {
		r = &vfailRec{}
		if len(v.vfail) >= vfailMax {
			v.vfail = map[pkey]*vfailRec{} // bound the map (see vfailMax)
		}
		v.vfail[key] = r
	}
	if r.count > 0 && time.Since(r.last) > verifyFailWindow {
		r.count = 0 // strikes age out: isolated failures long ago don't count toward the ban,
		// so a genuine user's occasional timeouts/mistakes spread over time aren't auto-banned —
		// only sustained failures within the window reach the threshold.
	}
	r.count++
	r.last = time.Now()
	count = r.count
	v.mu.Unlock()
	v.saveVerifyFails()
	max := v.cfg.VerifyMaxFails
	return count, max > 0 && count >= max
}

// clearVerifyFails drops an applicant's strikes — called on a successful approval so a member
// who eventually verifies starts fresh next time.
func (v *Verifier) clearVerifyFails(gid, uid int64) {
	v.mu.Lock()
	_, had := v.vfail[pkey{gid, uid}]
	delete(v.vfail, pkey{gid, uid})
	v.mu.Unlock()
	if had {
		v.saveVerifyFails()
	}
}

// verifyCooldownRemaining returns how long an applicant must still wait before re-applying
// (cfg.VerifyRetrySeconds since their last failure), or 0 if they may apply now.
func (v *Verifier) verifyCooldownRemaining(gid, uid int64) time.Duration {
	secs := v.cfg.VerifyRetrySeconds
	if secs <= 0 {
		return 0
	}
	v.mu.Lock()
	var count int
	var last time.Time
	if r := v.vfail[pkey{gid, uid}]; r != nil {
		count, last = r.count, r.last // copy under the lock — r is a pointer shared with recordVerifyFail
	}
	v.mu.Unlock()
	if count == 0 {
		return 0
	}
	if elapsed := time.Since(last); elapsed < time.Duration(secs)*time.Second {
		return time.Duration(secs)*time.Second - elapsed
	}
	return 0
}
