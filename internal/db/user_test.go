package db

import (
	"database/sql"
	"testing"
)

func TestDisplayNameSkipsBlankNames(t *testing.T) {
	null := sql.NullString{}
	str := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	for _, tc := range []struct {
		user User
		want string
	}{
		{User{Username: str("thebys"), Realname: str("Tomáš"), Email: "t@base48.cz"}, "thebys"},
		{User{Username: str(""), Realname: str("Tomáš"), Email: "t@base48.cz"}, "Tomáš"},
		{User{Username: null, Realname: str(" "), Email: "t@base48.cz"}, "t@base48.cz"}, // imported records
		{User{Username: str("  "), Realname: null, Email: "t@base48.cz"}, "t@base48.cz"},
	} {
		if got := tc.user.DisplayName(); got != tc.want {
			t.Errorf("DisplayName(%+v) = %q, want %q", tc.user, got, tc.want)
		}
	}
}
