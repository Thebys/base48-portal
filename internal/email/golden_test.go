package email

import (
	"context"
	"database/sql"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/base48/member-portal/internal/db"
)

// updateGolden rewrites the files under testdata/golden from what the emails
// render now: go test ./internal/email -run Golden -update. Review the diff.
var updateGolden = flag.Bool("update", false, "rewrite the golden emails")

// goldenMember is the member every golden email goes to.
func goldenMember(t *testing.T, database *sql.DB, lang string) int64 {
	t.Helper()
	var levelID, userID int64
	if err := database.QueryRow(`INSERT INTO levels (name, amount, active) VALUES (?, 1000, 1) RETURNING id`, "golden-"+lang).Scan(&levelID); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(
		`INSERT INTO users (email, username, realname, level_id, payments_id, state, locale)
		 VALUES (?, 'jana', 'Jana Nováková', ?, '4848', 'accepted', ?) RETURNING id`,
		"jana-"+lang+"@base48.cz", levelID, lang,
	).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	return userID
}

// queuedHTML queues one email with send and returns how it rendered.
func queuedHTML(t *testing.T, c *Client, database *sql.DB, userID int64, send func(*Client, *db.User) error) string {
	t.Helper()
	ctx := context.Background()
	user, err := c.queries.GetUserByID(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if err := send(c.Scheduled(0), &user); err != nil {
		t.Fatal(err)
	}
	var html string
	if err := database.QueryRow(`SELECT rendered_html FROM email_outbox WHERE user_id = ? ORDER BY id DESC LIMIT 1`, userID).Scan(&html); err != nil {
		t.Fatal(err)
	}
	return html
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name+".html")
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if string(want) != got {
		t.Errorf("%s renders differently from %s; run with -update and review the diff", name, path)
	}
}

// Every email, in both languages, as a member gets it. The files are the
// reference: a change to a template or a builder shows up as a diff of them.
func TestGoldenEmails(t *testing.T) {
	for _, lang := range []string{"cs", "en"} {
		c, database := barTestClient(t)
		user := goldenMember(t, database, lang)
		for name, send := range goldenSends {
			checkGolden(t, name+"."+lang, queuedHTML(t, c, database, user, send))
		}
	}
}
