package tg

import (
	"context"
	"errors"
	"testing"
)

func TestJoinRequestGone(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"hide requester missing", errors.New(`telego: declineChatJoinRequest: api: 400 "Bad Request: HIDE_REQUESTER_MISSING"`), true},
		{"already participant", errors.New(`api: 400 "Bad Request: USER_ALREADY_PARTICIPANT"`), true},
		{"participant id invalid", errors.New(`api: 400 "Bad Request: PARTICIPANT_ID_INVALID"`), true},
		{"missing rights is not gone", errors.New(`api: 400 "Bad Request: not enough rights"`), false},
		{"network error is not gone", errors.New("connection reset by peer"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := JoinRequestGone(tc.err); got != tc.want {
				t.Errorf("JoinRequestGone(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIdenticalAlertsAreCollapsed(t *testing.T) {
	caller := &scriptedCaller{}
	client := newTestClient(t, caller)
	for range 3 {
		client.FailAlert(context.Background(), -200, -100, "same failure")
	}
	client.FailAlert(context.Background(), -200, -100, "another failure")
	if calls := caller.methodCalls("sendMessage"); len(calls) != 2 {
		t.Fatalf("sendMessage calls = %d, want 2 (one per distinct alert)", len(calls))
	}
}
