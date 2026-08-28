package tg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mymmrac/telego/telegoapi"
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

// A private chat keeps what the bot said there: nothing in it is deleted on a timer.
func TestPrivateChatMessagesAreNeverScheduledForDeletion(t *testing.T) {
	caller := &scriptedCaller{}
	client := newTestClient(t, caller)
	client.ScheduleCleanup(4242, 1, 2, time.Minute)
	client.scheduleDelete(4242, 2, 0, time.Minute)
	if got := client.cleanupTimers.Load(); got != 0 {
		t.Fatalf("cleanup timers armed for a private chat = %d, want 0", got)
	}
	client.scheduleDelete(-100, 2, 0, time.Minute)
	if got := client.cleanupTimers.Load(); got != 1 {
		t.Fatalf("cleanup timers armed for a group = %d, want 1", got)
	}
}

// Two identical moderation actions are two facts, so the audit channel never collapses them.
func TestAuditLogKeepsIdenticalRecords(t *testing.T) {
	caller := &scriptedCaller{}
	client := newTestClient(t, caller)
	for range 3 {
		client.AuditLog(context.Background(), -200, "banned user 7")
	}
	if calls := caller.methodCalls("sendMessage"); len(calls) != 3 {
		t.Fatalf("sendMessage calls = %d, want 3: an audit record must never be deduplicated", len(calls))
	}
}

func TestGroupUnreachable(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New(`api: 403 "Forbidden: bot is not a member of the supergroup chat"`), true},
		{errors.New(`api: 403 "Forbidden: bot was kicked from the supergroup chat"`), true},
		{errors.New(`api: 400 "Bad Request: chat not found"`), true},
		{errors.New(`api: 400 "Bad Request: not enough rights"`), false},
		{errors.New("connection reset by peer"), false},
		{nil, false},
	}
	for _, tc := range cases {
		if got := GroupUnreachable(tc.err); got != tc.want {
			t.Errorf("GroupUnreachable(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

// A chat the bot cannot write in says nothing about the message it was asked to edit, so those
// failures must never count toward giving up on tracking one bug.
func TestDestinationFailuresAreNotPermanentPerMessage(t *testing.T) {
	destinations := []string{
		"Bad Request: not enough rights to send text messages to the chat",
		"Bad Request: have no rights to send a message",
		"Bad Request: CHAT_WRITE_FORBIDDEN",
		"Bad Request: CHAT_SEND_PLAIN_FORBIDDEN",
		"Bad Request: TOPIC_CLOSED",
		"Bad Request: chat not found",
		"Bad Request: group chat was upgraded to a supergroup chat, migrate to chat id",
	}
	for _, description := range destinations {
		err := &telegoapi.Error{ErrorCode: 400, Description: description}
		if CountablePermanentEditError(err) {
			t.Errorf("CountablePermanentEditError(%q) = true, want false", description)
		}
		if PermanentPostError(err) {
			t.Errorf("PermanentPostError(%q) = true, want false", description)
		}
	}
	// An unclassified 400 is still the message's own fault and still counts.
	own := &telegoapi.Error{ErrorCode: 400, Description: "Bad Request: message is too long"}
	if !CountablePermanentEditError(own) || !PermanentPostError(own) {
		t.Error("an unclassified 400 is a problem with the message itself and must still count")
	}
}

func TestApplicantGone(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "deactivated account", err: errors.New(`telego: declineChatJoinRequest: api: 403 "Forbidden: user is deactivated"`), want: true},
		{name: "raw api constant", err: errors.New("USER_DEACTIVATED"), want: true},
		{name: "blocked is not gone", err: errors.New(`api: 403 "Forbidden: bot was blocked by the user"`), want: false},
		{name: "unrelated", err: errors.New("unexpected EOF"), want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tt := range tests {
		if got := ApplicantGone(tt.err); got != tt.want {
			t.Errorf("%s: ApplicantGone = %v, want %v", tt.name, got, tt.want)
		}
	}
}
