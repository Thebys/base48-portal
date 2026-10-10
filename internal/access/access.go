// Package access decides what a signed-in user may use in the portal.
//
// Two things feed into it, and they are kept apart on purpose:
//   - the membership state (users.state) says who someone is to the
//     association: applicant, member, ex-member, external user…
//   - grants (access_grants) give access to a part of the portal regardless
//     of that state, e.g. the workshop for a non-member.
//
// Members get everything from their state and need no grants. A grant only
// ever adds access. Fees, Keycloak roles, reminders and the member export are
// about membership, not access, and stay on state = 'accepted' elsewhere.
package access

// Scope names something a user can be granted. Stored in access_grants.scope.
type Scope string

// Workshop is the car workshop: its page, the calendar and reservations.
const Workshop Scope = "workshop"

// ScopeInfo describes a scope for the admin UI.
type ScopeInfo struct {
	Scope Scope
	Label string
}

// Scopes lists every scope the portal understands, in display order. A grant
// row with a scope missing here is ignored.
var Scopes = []ScopeInfo{
	{Scope: Workshop, Label: "Autodílna"},
}

// Known reports whether s is a scope from Scopes.
func Known(s string) bool {
	for _, info := range Scopes {
		if string(info.Scope) == s {
			return true
		}
	}
	return false
}

// Label is the human name of a scope, or the raw value for an unknown one.
func Label(s Scope) string {
	for _, info := range Scopes {
		if info.Scope == s {
			return info.Label
		}
	}
	return string(s)
}

// Subject is one user as the access rules see them.
type Subject struct {
	State  string         // users.state; "" when the user has no portal record
	Grants map[Scope]bool // granted scopes
	Admin  bool           // memberportal_admin
}

func (s Subject) member() bool {
	return s.State == "accepted"
}

// Has reports whether the user holds a grant for scope.
func (s Subject) Has(scope Scope) bool {
	return s.Grants[scope]
}

// MemberArea covers the member-only pages: finance overview, services and the
// recent bank payments list.
func (s Subject) MemberArea() bool {
	return s.Admin || s.member()
}

// Fundraising covers the fundraising projects. External users see them too:
// they are the one thing a non-member can join in on.
func (s Subject) Fundraising() bool {
	return s.Admin || s.member() || s.State == "external"
}

// Workshop covers the workshop page, its calendar and making reservations.
func (s Subject) Workshop() bool {
	return s.Admin || s.member() || s.Has(Workshop)
}

// MembershipSettings covers choosing a membership level and fee amount. It
// follows the user's own state even for admins: it changes their own fees.
func (s Subject) MembershipSettings() bool {
	return s.member()
}

// Granted lists the user's grants in Scopes order, for display.
func (s Subject) Granted() []ScopeInfo {
	var out []ScopeInfo
	for _, info := range Scopes {
		if s.Has(info.Scope) {
			out = append(out, info)
		}
	}
	return out
}
