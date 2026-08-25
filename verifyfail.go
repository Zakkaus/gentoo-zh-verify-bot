package main

import (
	"log"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
)

// vfailRec drives both retry cooldowns and automatic bans.
type vfailRec struct {
	count int
	last  time.Time
}

// JSON uses a slice because pkey cannot be an object key.
type vfailDisk struct {
	GroupID int64 `json:"group_id"`
	UserID  int64 `json:"user_id"`
	Count   int   `json:"count"`
	Last    int64 `json:"last"`
}

// Clear the bounded map wholesale under an exceptional ID flood.
const vfailMax = 50000

// Only sustained failures within this rolling window accumulate toward a ban.
const verifyFailWindow = 6 * time.Hour

func (v *Verifier) loadVerifyFails() {
	if v.vfailPath == "" {
		return
	}
	var recs []vfailDisk
	if err := store.Load(v.vfailPath, &recs); err != nil {
		if store.ReadFailed(err) {
			v.vfailPath = ""
		}
		return // corrupt files were backed up; unreadable files remain untouched and write-disabled
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
	_ = store.Save(v.vfailPath, func() any {
		v.mu.Lock()
		defer v.mu.Unlock()
		recs := make([]vfailDisk, 0, len(v.vfail))
		for k, r := range v.vfail {
			if r.count > 0 {
				recs = append(recs, vfailDisk{GroupID: k.gid, UserID: k.uid, Count: r.count, Last: r.last.Unix()})
			}
		}
		return recs
	})
}

// Strikes persist across restarts; a negative threshold disables automatic bans.
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
		r.count = 0 // Isolated old failures must not accumulate into a ban.
		// Only failures inside verifyFailWindow accumulate.
	}
	r.count++
	r.last = time.Now()
	count = r.count
	v.mu.Unlock()
	v.saveVerifyFails()
	max := v.cfg.VerifyMaxFails
	return count, max > 0 && count >= max
}

// Successful verification clears prior strikes.
func (v *Verifier) clearVerifyFails(gid, uid int64) {
	v.mu.Lock()
	_, had := v.vfail[pkey{gid, uid}]
	delete(v.vfail, pkey{gid, uid})
	v.mu.Unlock()
	if had {
		v.saveVerifyFails()
	}
}

// verifyCooldownRemaining returns zero when the applicant may reapply.
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
