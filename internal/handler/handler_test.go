package handler

import "testing"

func TestInitials(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"single word", "thebys", "TH"},
		{"two words", "Tomáš Biheler", "TB"},
		{"three words takes the first two", "Jan Amos Komenský", "JA"},
		{"already uppercase", "AB", "AB"},
		{"single letter", "x", "X"},
		// A byte slice would cut the two-byte 'á' in half and emit U+FFFD.
		{"multibyte first letter", "álfa", "ÁL"},
		{"multibyte across words", "Šárka Čermáková", "ŠČ"},
		{"surrounding whitespace", "  ludek  ", "LU"},
		{"inner whitespace collapses", "Eva   Malá", "EM"},
		{"empty", "", "?"},
		{"whitespace only", "   ", "?"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := initials(tt.in); got != tt.want {
				t.Errorf("initials(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestUserViewsAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, v := range userViews {
		if seen[v.Key] {
			t.Errorf("duplicate view key %q", v.Key)
		}
		seen[v.Key] = true
		if v.Label == "" || v.State == "" || v.Sort == "" {
			t.Errorf("view %q is missing a field: %+v", v.Key, v)
		}
	}
	// The no-query-string default has to name a real preset, or /admin/users
	// silently falls back to an unfiltered list.
	if _, ok := lookupUserView(defaultUserView); !ok {
		t.Errorf("defaultUserView %q is not a declared view", defaultUserView)
	}
}

func TestCurrentUserView(t *testing.T) {
	tests := []struct {
		name                 string
		state, balance, sort string
		search, keycloak     string
		want                 string
	}{
		{name: "debt preset", state: "accepted", balance: "negative", sort: "balance_asc", want: "debt"},
		{name: "active preset", state: "accepted", sort: "id_desc", want: "active"},
		{name: "awaiting preset", state: "awaiting", sort: "id_desc", want: "awaiting"},
		{name: "suspended preset", state: "suspended", sort: "id_desc", want: "suspended"},

		// Same combination arriving from the filter form still lights the tab.
		{name: "form-built debt", state: "accepted", balance: "negative", sort: "balance_asc", want: "debt"},

		// A narrowed list is no longer that preset.
		{name: "debt plus search", state: "accepted", balance: "negative", sort: "balance_asc", search: "novak", want: ""},
		{name: "debt plus keycloak", state: "accepted", balance: "negative", sort: "balance_asc", keycloak: "not_linked", want: ""},

		// Combinations no preset covers.
		{name: "accepted by balance desc", state: "accepted", sort: "balance_desc", want: ""},
		{name: "all states", want: ""},
		{name: "accepted positive", state: "accepted", balance: "positive", sort: "id_desc", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := currentUserView(tt.state, tt.balance, tt.sort, tt.search, tt.keycloak)
			if got != tt.want {
				t.Errorf("currentUserView(%q,%q,%q,%q,%q) = %q, want %q",
					tt.state, tt.balance, tt.sort, tt.search, tt.keycloak, got, tt.want)
			}
		})
	}
}

// Each preset must select a distinct list; two tabs showing the same rows in the
// same order would just be a broken tab.
func TestUserViewsAreDistinct(t *testing.T) {
	type combo struct{ state, balance, sort string }
	seen := map[combo]string{}
	for _, v := range userViews {
		c := combo{v.State, v.Balance, v.Sort}
		if prev, dup := seen[c]; dup {
			t.Errorf("views %q and %q select the same list %+v", prev, v.Key, c)
		}
		seen[c] = v.Key
	}
}
