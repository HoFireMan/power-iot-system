package auth

import "testing"

func TestRoleMatrix(t *testing.T) {
	cases := []struct {
		r  Role
		a  string
		ok bool
	}{{RoleRunbook, "issue", true}, {RoleRunbook, "consume", false}, {RoleRunbook, "inspect", false}, {RoleRunner, "inspect", true}, {RoleRunner, "consume", true}, {RoleRunner, "resolve-issue", false}, {RoleRecovery, "inspect", false}, {RoleRecovery, "resolve-recovery", true}, {RoleRecovery, "resolve-owner", false}}
	for _, c := range cases {
		if got := Allowed(c.r, c.a); got != c.ok {
			t.Errorf("%s/%s=%v", c.r, c.a, got)
		}
	}
}
