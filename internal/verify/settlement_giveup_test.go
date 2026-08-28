package verify

import (
	"errors"
	"testing"
)

// A deactivated applicant produced ten declines a minute apart, each one impossible, ending in a
// warning asking an administrator to settle a request nobody can settle.
func TestGiveUpSettlingOnAnImpossibleFailure(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "deactivated applicant", err: errors.New(`telego: declineChatJoinRequest: api: 403 "Forbidden: user is deactivated"`), want: true},
		{name: "bot removed from the group", err: errors.New("Forbidden: bot is not a member of the supergroup chat"), want: true},
		{name: "transient network failure", err: errors.New("unexpected EOF"), want: false},
		{name: "missing rights are repairable", err: errors.New(`api: 400 "Bad Request: not enough rights"`), want: false},
	}
	for _, tt := range tests {
		if got := giveUpSettling(tt.err); got != tt.want {
			t.Errorf("%s: giveUpSettling = %v, want %v", tt.name, got, tt.want)
		}
	}
}
