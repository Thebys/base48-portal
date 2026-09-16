package handler

import (
	"context"
	"database/sql"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/migrate"
	"github.com/base48/member-portal/migrations"
)

func TestHistoryWindowStart(t *testing.T) {
	tests := []struct {
		name string
		now  time.Time
		want string
	}{
		{"mid-month", time.Date(2026, 9, 16, 14, 30, 0, 0, time.UTC), "2026-08-01 00:00"},
		{"first of the month", time.Date(2026, 9, 1, 0, 5, 0, 0, time.UTC), "2026-08-01 00:00"},
		{"last minute of the month", time.Date(2026, 9, 30, 23, 59, 0, 0, time.UTC), "2026-08-01 00:00"},
		{"january reaches into last year", time.Date(2026, 1, 3, 9, 0, 0, 0, time.UTC), "2025-12-01 00:00"},
		// The 31st is the classic AddDate trap: Date(2026,3,31).AddDate(0,-1,0)
		// would normalise to 3 March. Anchoring on the 1st first avoids it.
		{"march anchors before february", time.Date(2026, 3, 31, 23, 0, 0, 0, time.UTC), "2026-02-01 00:00"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := historyWindowStart(tt.now); got != tt.want {
				t.Errorf("historyWindowStart(%s) = %q, want %q", tt.now.Format(time.RFC3339), got, tt.want)
			}
		})
	}
}

func TestWorkshopCapabilities(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty array", "[]", []string{}},
		{"one capability", `["lift"]`, []string{"lift"}},
		{"several", `["lift","air"]`, []string{"lift", "air"}},
		{"empty string column", "", []string{}},
		{"json null", "null", []string{}},
		{"malformed json degrades quietly", "{oops", []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := workshopCapabilities(tt.in)
			if got == nil {
				t.Fatal("workshopCapabilities returned nil, want an empty slice (it is marshalled to JSON)")
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("workshopCapabilities(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

// testQueries opens a throwaway database with the real migrations applied.
func testQueries(t *testing.T) (*db.Queries, *sql.DB) {
	t.Helper()

	database, err := sql.Open("sqlite", "file:"+t.TempDir()+"/test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	if err := migrate.Run(database, migrations.FS); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	return db.New(database), database
}

func TestListReservationHistory(t *testing.T) {
	queries, database := testQueries(t)
	ctx := context.Background()

	var level int64
	if err := database.QueryRow(`SELECT id FROM levels ORDER BY id LIMIT 1`).Scan(&level); err != nil {
		t.Fatalf("the schema should seed at least one membership level: %v", err)
	}

	// Two members so the listing has to join the right names.
	for _, email := range []string{"driver@base48.cz", "other@base48.cz"} {
		if _, err := database.Exec(
			`INSERT INTO users (email, username, level_id, state) VALUES (?, ?, ?, 'accepted')`,
			email, email[:strings.IndexByte(email, '@')], level,
		); err != nil {
			t.Fatalf("insert user %s: %v", email, err)
		}
	}

	var bay int64
	if err := database.QueryRow(`SELECT id FROM resources WHERE slug = 'bay-1'`).Scan(&bay); err != nil {
		t.Fatalf("migration 016 should have seeded bay-1: %v", err)
	}

	now := "2026-09-16 14:00"
	since := "2026-08-01 00:00"

	rows := []struct {
		name     string
		user     int64
		starts   string
		ends     string
		state    string
		included bool
	}{
		{"finished last month", 1, "2026-08-03 09:00", "2026-08-03 17:00", "active", true},
		{"finished this month", 2, "2026-09-02 10:00", "2026-09-02 12:00", "active", true},
		{"bumped, booked end still ahead", 1, "2026-09-15 08:00", "2026-09-20 08:00", "bumped", true},
		{"explicitly ended", 2, "2026-09-10 08:00", "2026-09-10 09:00", "ended", true},
		{"still running", 1, "2026-09-16 08:00", "2026-09-16 20:00", "active", false},
		{"upcoming", 2, "2026-09-18 08:00", "2026-09-18 12:00", "active", false},
		{"cancelled, never used", 1, "2026-09-04 08:00", "2026-09-04 12:00", "cancelled", false},
		{"too old", 2, "2026-07-20 08:00", "2026-07-20 12:00", "active", false},
	}
	for _, r := range rows {
		if _, err := database.Exec(
			`INSERT INTO reservations (resource_id, user_id, starts_at, ends_at, note, state) VALUES (?, ?, ?, ?, ?, ?)`,
			bay, r.user, r.starts, r.ends, r.name, r.state,
		); err != nil {
			t.Fatalf("insert reservation %q: %v", r.name, err)
		}
	}

	got, err := queries.ListReservationHistory(ctx, db.ListReservationHistoryParams{Since: since, Now: now})
	if err != nil {
		t.Fatalf("ListReservationHistory: %v", err)
	}

	want := []string{}
	for _, r := range rows {
		if r.included {
			want = append(want, r.name)
		}
	}

	gotNames := []string{}
	for _, row := range got {
		gotNames = append(gotNames, row.Note.String)
	}

	sort.Strings(want)
	sortedGot := append([]string{}, gotNames...)
	sort.Strings(sortedGot)
	if !reflect.DeepEqual(sortedGot, want) {
		t.Errorf("history contains %v, want %v", sortedGot, want)
	}

	// Newest first, so the history table reads top-down.
	for i := 1; i < len(got); i++ {
		if got[i-1].StartsAt < got[i].StartsAt {
			t.Errorf("history is not ordered newest first: %q before %q", got[i-1].StartsAt, got[i].StartsAt)
		}
	}
}
