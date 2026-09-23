package email

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/migrate"
	"github.com/base48/member-portal/migrations"
)

// barTestClient wires a client against a migrated throwaway database and the
// shipped templates. No SMTP host, so nothing leaves the machine.
func barTestClient(t *testing.T) (*Client, *sql.DB) {
	t.Helper()
	database, err := sql.Open("sqlite", "file:"+t.TempDir()+"/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := migrate.Run(database, migrations.FS); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	cfg := &config.Config{
		EmailEnabled: true,
		WebRoot:      "../../web",
		BaseURL:      "https://portal.base48.cz",
	}
	return New(cfg, db.New(database), nil), database
}

// seedBarMember inserts a user and, unless linked is false, a bar account with
// the given balance.
func seedBarMember(t *testing.T, database *sql.DB, username, state string, balanceCents int64, linked bool) int64 {
	t.Helper()
	var levelID int64
	if err := database.QueryRow(`SELECT id FROM levels ORDER BY id LIMIT 1`).Scan(&levelID); err != nil {
		t.Fatalf("the schema should seed at least one membership level: %v", err)
	}
	var userID int64
	err := database.QueryRow(
		`INSERT INTO users (email, username, state, level_id) VALUES (?, ?, ?, ?) RETURNING id`,
		username+"@base48.cz", username, state, levelID,
	).Scan(&userID)
	if err != nil {
		t.Fatalf("insert user %s: %v", username, err)
	}
	var link sql.NullInt64
	if linked {
		link = sql.NullInt64{Int64: userID, Valid: true}
	}
	if _, err := database.Exec(
		`INSERT INTO revbank_accounts (username, user_id, balance_cents) VALUES (?, ?, ?)`,
		username, link, balanceCents,
	); err != nil {
		t.Fatalf("insert bar account %s: %v", username, err)
	}
	return userID
}

func setBarBalance(t *testing.T, database *sql.DB, userID, cents int64) {
	t.Helper()
	if _, err := database.Exec(`UPDATE revbank_accounts SET balance_cents = ? WHERE user_id = ?`, cents, userID); err != nil {
		t.Fatal(err)
	}
}

func enabledBarSettings() BarDebtSettings {
	s := DefaultBarDebtSettings()
	s.Enabled = true
	s.ThresholdCZK = 200
	s.IntervalDays = 14
	s.DelayHours = 24
	return s
}

func TestBarDebtSettingsRoundTrip(t *testing.T) {
	c, _ := barTestClient(t)
	ctx := context.Background()

	if got := LoadBarDebtSettings(ctx, c.queries); got != DefaultBarDebtSettings() {
		t.Fatalf("fresh database should yield the defaults, got %+v", got)
	}
	if DefaultBarDebtSettings().Enabled {
		t.Error("a new kind of email must start switched off")
	}

	want := BarDebtSettings{Enabled: true, ThresholdCZK: 350, IntervalDays: 7, DelayHours: 0}
	if err := SaveBarDebtSettings(ctx, c.queries, want); err != nil {
		t.Fatal(err)
	}
	if got := LoadBarDebtSettings(ctx, c.queries); got != want {
		t.Errorf("round trip: got %+v, want %+v", got, want)
	}
}

func TestBarDebtSettingsValidate(t *testing.T) {
	bad := []BarDebtSettings{
		{ThresholdCZK: 0, IntervalDays: 14, DelayHours: 24},
		{ThresholdCZK: 200, IntervalDays: 0, DelayHours: 24},
		{ThresholdCZK: 200, IntervalDays: 14, DelayHours: -1},
		{ThresholdCZK: 200, IntervalDays: 14, DelayHours: 169},
	}
	for _, s := range bad {
		if s.Validate() == nil {
			t.Errorf("%+v should be rejected", s)
		}
	}
	if err := enabledBarSettings().Validate(); err != nil {
		t.Errorf("defaults rejected: %v", err)
	}
}

// The email says "above", so a debt of exactly the threshold does not count.
func TestBarDebtWarrantedIsStrict(t *testing.T) {
	s := enabledBarSettings() // threshold 200 Kč
	cases := map[int64]bool{-19999: false, -20000: false, -20001: true, -50000: true, 0: false, 100: false}
	for cents, want := range cases {
		if got := s.Warranted(cents); got != want {
			t.Errorf("Warranted(%d) = %v, want %v", cents, got, want)
		}
	}
}

func TestFormatCZKCents(t *testing.T) {
	cases := map[int64]string{-35000: "-350", -35050: "-350,50", -5: "-0,05", 0: "0", 1200: "12"}
	for cents, want := range cases {
		if got := FormatCZKCents(cents); got != want {
			t.Errorf("FormatCZKCents(%d) = %q, want %q", cents, got, want)
		}
	}
}

func TestQueueBarDebtRemindersDisabledDoesNothing(t *testing.T) {
	c, database := barTestClient(t)
	seedBarMember(t, database, "deep", "accepted", -100000, true)

	s := enabledBarSettings()
	s.Enabled = false
	res, err := c.QueueBarDebtReminders(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if res.Queued != 0 {
		t.Errorf("queued %d while switched off", res.Queued)
	}
}

func TestQueueBarDebtRemindersPicksTheRightMembers(t *testing.T) {
	c, database := barTestClient(t)
	ctx := context.Background()

	deep := seedBarMember(t, database, "deep", "accepted", -35050, true)
	justOver := seedBarMember(t, database, "justover", "accepted", -20050, true)
	seedBarMember(t, database, "atline", "accepted", -20000, true)        // exactly the threshold
	seedBarMember(t, database, "formerdebtor", "suspended", -90000, true) // not active
	seedBarMember(t, database, "unlinked", "accepted", -90000, false)     // no portal user

	res, err := c.QueueBarDebtReminders(ctx, enabledBarSettings())
	if err != nil {
		t.Fatal(err)
	}
	if res.Debtors != 2 || res.Queued != 2 || res.Errors != 0 {
		t.Fatalf("result = %+v, want the two accepted members over the line", res)
	}

	rows, err := database.Query(`SELECT user_id, template_name, status, next_retry_at IS NOT NULL, rendered_html FROM email_outbox ORDER BY user_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []int64
	for rows.Next() {
		var (
			userID          int64
			tmpl, status, h string
			scheduled       bool
		)
		if err := rows.Scan(&userID, &tmpl, &status, &scheduled, &h); err != nil {
			t.Fatal(err)
		}
		got = append(got, userID)
		if tmpl != TemplateFileBarDebt || status != "pending" || !scheduled {
			t.Errorf("user %d: template=%s status=%s scheduled=%v, want a pending, timed bar_debt.html", userID, tmpl, status, scheduled)
		}
		// No bank details: a bar tab is settled in cash at the kiosk.
		if strings.Contains(h, "/api/qr") {
			t.Errorf("user %d: bar reminder must not carry a bank QR code", userID)
		}
		if userID == deep && !strings.Contains(h, "-350,50&nbsp;Kč") {
			t.Errorf("reminder does not quote the balance; html:\n%s", h)
		}
	}
	if len(got) != 2 || got[0] != deep || got[1] != justOver {
		t.Errorf("outbox users = %v, want [%d %d]", got, deep, justOver)
	}

	// A second pass inside the interval must not queue again.
	res, err = c.QueueBarDebtReminders(ctx, enabledBarSettings())
	if err != nil {
		t.Fatal(err)
	}
	if res.Queued != 0 || res.Skipped != 2 {
		t.Errorf("second pass = %+v, want both skipped", res)
	}
}

// A reminder sent long enough ago no longer blocks the next one.
func TestQueueBarDebtRemindersRespectsInterval(t *testing.T) {
	c, database := barTestClient(t)
	ctx := context.Background()

	user := seedBarMember(t, database, "deep", "accepted", -50000, true)
	if _, err := database.Exec(
		`INSERT INTO email_outbox (user_id, recipient, subject, template_name, status, created_at)
		 VALUES (?, 'deep@base48.cz', 'x', ?, 'sent', datetime('now', '-10 days'))`,
		user, TemplateFileBarDebt,
	); err != nil {
		t.Fatal(err)
	}

	s := enabledBarSettings()
	s.IntervalDays = 14
	if res, _ := c.QueueBarDebtReminders(ctx, s); res.Queued != 0 {
		t.Errorf("reminded 10 days ago with a 14-day interval, yet queued again")
	}
	s.IntervalDays = 7
	if res, _ := c.QueueBarDebtReminders(ctx, s); res.Queued != 1 {
		t.Errorf("reminded 10 days ago with a 7-day interval, should queue again (got %+v)", res)
	}
}

// queueOne puts one bar reminder in the outbox and returns it as the worker
// would see it.
func queueOne(t *testing.T, c *Client, database *sql.DB, username string, balance int64) (int64, db.EmailOutbox) {
	t.Helper()
	ctx := context.Background()
	s := enabledBarSettings()
	if err := SaveBarDebtSettings(ctx, c.queries, s); err != nil {
		t.Fatal(err)
	}
	user := seedBarMember(t, database, username, "accepted", balance, true)
	if res, err := c.QueueBarDebtReminders(ctx, s); err != nil || res.Queued != 1 {
		t.Fatalf("queue: %+v, %v", res, err)
	}
	var id int64
	if err := database.QueryRow(`SELECT id FROM email_outbox WHERE user_id = ?`, user).Scan(&id); err != nil {
		t.Fatal(err)
	}
	entry, err := c.queries.GetEmailOutbox(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return user, entry
}

func outboxState(t *testing.T, database *sql.DB, id int64) (status, lastError, html string) {
	t.Helper()
	var le, h sql.NullString
	if err := database.QueryRow(`SELECT status, last_error, rendered_html FROM email_outbox WHERE id = ?`, id).Scan(&status, &le, &h); err != nil {
		t.Fatal(err)
	}
	return status, le.String, h.String
}

func TestRefreshCancelsWhenMemberToppedUp(t *testing.T) {
	c, database := barTestClient(t)
	user, entry := queueOne(t, c, database, "payer", -30000)

	setBarBalance(t, database, user, 5000) // topped up at the kiosk meanwhile

	if c.refreshBeforeSend(context.Background(), &entry) {
		t.Fatal("a member back above the line must not be mailed")
	}
	status, lastErr, _ := outboxState(t, database, entry.ID)
	if status != "cancelled" || !strings.Contains(lastErr, "nepřesahuje hranici") {
		t.Errorf("status=%q last_error=%q, want cancelled with the reason", status, lastErr)
	}
}

func TestRefreshCancelsWhenSwitchedOff(t *testing.T) {
	c, database := barTestClient(t)
	ctx := context.Background()
	_, entry := queueOne(t, c, database, "owing", -30000)

	s := enabledBarSettings()
	s.Enabled = false
	if err := SaveBarDebtSettings(ctx, c.queries, s); err != nil {
		t.Fatal(err)
	}

	if c.refreshBeforeSend(ctx, &entry) {
		t.Fatal("switching the reminder off must stop reminders already queued")
	}
	if status, _, _ := outboxState(t, database, entry.ID); status != "cancelled" {
		t.Errorf("status = %q, want cancelled", status)
	}
}

func TestRefreshCancelsWhenNoLongerAMember(t *testing.T) {
	c, database := barTestClient(t)
	user, entry := queueOne(t, c, database, "leaver", -30000)

	if _, err := database.Exec(`UPDATE users SET state = 'suspended' WHERE id = ?`, user); err != nil {
		t.Fatal(err)
	}
	if c.refreshBeforeSend(context.Background(), &entry) {
		t.Fatal("a suspended member must not get the bar reminder")
	}
}

func TestRefreshRerendersWithCurrentBalance(t *testing.T) {
	c, database := barTestClient(t)
	user, entry := queueOne(t, c, database, "stillowing", -30000)

	setBarBalance(t, database, user, -25000) // paid a little, still below the line

	if !c.refreshBeforeSend(context.Background(), &entry) {
		t.Fatal("still below the line, should be sent")
	}
	if !strings.Contains(entry.RenderedHtml.String, "-250&nbsp;Kč") {
		t.Error("the entry handed to delivery does not quote the current balance")
	}
	status, _, stored := outboxState(t, database, entry.ID)
	if status != "pending" || !strings.Contains(stored, "-250&nbsp;Kč") || strings.Contains(stored, "-300&nbsp;Kč") {
		t.Errorf("status=%q; stored html should quote -250, not -300", status)
	}
}

// Other templates pass through untouched.
func TestRefreshIgnoresOtherTemplates(t *testing.T) {
	c, _ := barTestClient(t)
	entry := db.EmailOutbox{ID: 1, TemplateName: "welcome.html"}
	if !c.refreshBeforeSend(context.Background(), &entry) {
		t.Error("non-bar emails must not be held back")
	}
}

func TestBarDebtRendersInBothLanguages(t *testing.T) {
	c, _ := barTestClient(t)
	ctx := context.Background()
	for _, lang := range []string{"cs", "en"} {
		user := &db.User{ID: 1, Email: "x@base48.cz", Locale: lang, Username: sql.NullString{String: "x", Valid: true}}
		html, err := c.renderTemplate(SendParams{
			TemplateName: TemplateFileBarDebt,
			Data:         c.barDebtData(ctx, user, -35050, 200),
		})
		if err != nil {
			t.Fatalf("%s: %v", lang, err)
		}
		if strings.Contains(html, "%!") {
			t.Errorf("%s: printf artefact in output", lang)
		}
		if !strings.Contains(html, "-350,50&nbsp;Kč") || !strings.Contains(html, "200") {
			t.Errorf("%s: balance or threshold missing", lang)
		}
	}
}
