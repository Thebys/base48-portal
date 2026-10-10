package access

import "testing"

func TestRules(t *testing.T) {
	workshop := map[Scope]bool{Workshop: true}

	tests := []struct {
		name    string
		subject Subject

		memberArea, fundraising, workshop, membershipSettings bool
	}{
		{"no portal record", Subject{}, false, false, false, false},
		{"applicant", Subject{State: "awaiting"}, false, false, false, false},
		{"applicant with workshop grant", Subject{State: "awaiting", Grants: workshop}, false, false, true, false},
		{"member", Subject{State: "accepted"}, true, true, true, true},
		{"member with a redundant grant", Subject{State: "accepted", Grants: workshop}, true, true, true, true},
		{"suspended", Subject{State: "suspended"}, false, false, false, false},
		{"ex-member", Subject{State: "exmember"}, false, false, false, false},
		{"ex-member keeping the workshop", Subject{State: "exmember", Grants: workshop}, false, false, true, false},
		{"rejected", Subject{State: "rejected"}, false, false, false, false},
		{"external", Subject{State: "external"}, false, true, false, false},
		{"external with workshop grant", Subject{State: "external", Grants: workshop}, false, true, true, false},
		{"admin without a record", Subject{Admin: true}, true, true, true, false},
		{"admin who is a member", Subject{State: "accepted", Admin: true}, true, true, true, true},
		{"admin who is suspended", Subject{State: "suspended", Admin: true}, true, true, true, false},
		{"unknown grant does nothing", Subject{State: "external", Grants: map[Scope]bool{"lift": true}}, false, true, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.subject
			if got := s.MemberArea(); got != tt.memberArea {
				t.Errorf("MemberArea() = %v, want %v", got, tt.memberArea)
			}
			if got := s.Fundraising(); got != tt.fundraising {
				t.Errorf("Fundraising() = %v, want %v", got, tt.fundraising)
			}
			if got := s.Workshop(); got != tt.workshop {
				t.Errorf("Workshop() = %v, want %v", got, tt.workshop)
			}
			if got := s.MembershipSettings(); got != tt.membershipSettings {
				t.Errorf("MembershipSettings() = %v, want %v", got, tt.membershipSettings)
			}
		})
	}
}

func TestKnownAndLabel(t *testing.T) {
	if !Known("workshop") {
		t.Error(`Known("workshop") = false, want true`)
	}
	for _, s := range []string{"", "Workshop", "lift"} {
		if Known(s) {
			t.Errorf("Known(%q) = true, want false", s)
		}
	}
	if got := Label(Workshop); got != "Autodílna" {
		t.Errorf("Label(Workshop) = %q, want %q", got, "Autodílna")
	}
	if got := Label("lift"); got != "lift" {
		t.Errorf(`Label("lift") = %q, want the raw value`, got)
	}
}

func TestGrantedFollowsScopesOrder(t *testing.T) {
	s := Subject{Grants: map[Scope]bool{Workshop: true, "unknown": true}}
	got := s.Granted()
	if len(got) != 1 || got[0].Scope != Workshop {
		t.Errorf("Granted() = %#v, want only the workshop", got)
	}
	if got := (Subject{}).Granted(); len(got) != 0 {
		t.Errorf("Granted() with no grants = %#v, want empty", got)
	}
}
