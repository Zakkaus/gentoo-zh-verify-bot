package main

import (
	"testing"

	"github.com/mymmrac/telego"
)

// TestMissingModRights covers the startup rights-preflight classifier: a fully-privileged admin
// (and the owner) are missing nothing; an admin lacking a right is reported; a rights-less admin is
// missing all three (approve / ban / delete).
func TestMissingModRights(t *testing.T) {
	if m := missingModRights(&telego.ChatMemberAdministrator{CanInviteUsers: true, CanRestrictMembers: true, CanDeleteMessages: true}); len(m) != 0 {
		t.Errorf("a fully-privileged admin should be missing nothing, got %v", m)
	}
	if m := missingModRights(&telego.ChatMemberAdministrator{}); len(m) != 3 {
		t.Errorf("an admin with no rights should be missing all 3, got %v", m)
	}
	if m := missingModRights(&telego.ChatMemberAdministrator{CanInviteUsers: true, CanDeleteMessages: true}); len(m) != 1 {
		t.Errorf("an admin missing only can_restrict_members should report 1, got %v", m)
	}
	if m := missingModRights(&telego.ChatMemberOwner{}); len(m) != 0 {
		t.Errorf("the owner implicitly has all rights — should be missing nothing, got %v", m)
	}
}
