package email

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeSMTP is just enough of an SMTP server to receive mail from sendSMTP: no
// STARTTLS, no AUTH, like the local relay in production. While failing is set
// it refuses every recipient.
type fakeSMTP struct {
	mu       sync.Mutex
	received []string // raw messages
	failing  bool
}

func startFakeSMTP(t *testing.T) (*fakeSMTP, string, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fakeSMTP{}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	n, _ := strconv.Atoi(port)
	return f, host, n
}

func (f *fakeSMTP) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	reply := func(s string) { fmt.Fprintf(conn, "%s\r\n", s) }
	reply("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			reply("250 fake")
		case strings.HasPrefix(cmd, "MAIL FROM"):
			reply("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO"):
			f.mu.Lock()
			failing := f.failing
			f.mu.Unlock()
			if failing {
				reply("451 try again later")
			} else {
				reply("250 ok")
			}
		case cmd == "DATA":
			reply("354 go ahead")
			var msg strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				msg.WriteString(l)
			}
			f.mu.Lock()
			f.received = append(f.received, msg.String())
			f.mu.Unlock()
			reply("250 queued")
		case cmd == "QUIT":
			reply("221 bye")
			return
		default:
			reply("250 ok")
		}
	}
}

func (f *fakeSMTP) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.received)
}

func (f *fakeSMTP) setFailing(v bool) {
	f.mu.Lock()
	f.failing = v
	f.mu.Unlock()
}

// deliveryClient is barTestClient wired to a fake SMTP server.
func deliveryClient(t *testing.T) (*Client, *sql.DB, *fakeSMTP) {
	t.Helper()
	c, database := barTestClient(t)
	smtp, host, port := startFakeSMTP(t)
	c.config.SMTPHost = host
	c.config.SMTPPort = port
	c.config.SMTPSkipTLS = true
	c.config.SMTPFrom = "portal@base48.cz"
	return c, database, smtp
}

func outboxRow(t *testing.T, database *sql.DB, id int64) (status string, attempts int64, lastError string) {
	t.Helper()
	var le sql.NullString
	if err := database.QueryRow(`SELECT status, attempts, last_error FROM email_outbox WHERE id = ?`, id).
		Scan(&status, &attempts, &le); err != nil {
		t.Fatal(err)
	}
	return status, attempts, le.String
}

func TestWorkerDeliversDueEmails(t *testing.T) {
	c, database, smtp := deliveryClient(t)
	user := seedDebtor(t, database, "owing", 2)
	entry := queueDebt(t, c, database, user, TierNegativeBalance, -2000)

	if sent := c.ProcessPendingEmails(context.Background()); sent != 1 {
		t.Fatalf("sent %d, want 1", sent)
	}
	if smtp.count() != 1 {
		t.Errorf("the SMTP server got %d messages", smtp.count())
	}
	if status, attempts, _ := outboxRow(t, database, entry.ID); status != "sent" || attempts != 1 {
		t.Errorf("status %q after %d tries, want sent after 1", status, attempts)
	}
	if again := c.ProcessPendingEmails(context.Background()); again != 0 || smtp.count() != 1 {
		t.Errorf("a sent email went out again")
	}
}

// A test instance running on a copy of the production database must not mail
// members what was waiting in the copied outbox.
func TestWorkerIdlesWhileEmailIsOff(t *testing.T) {
	c, database, smtp := deliveryClient(t)
	user := seedDebtor(t, database, "owing", 2)
	entry := queueDebt(t, c, database, user, TierNegativeBalance, -2000)

	c.config.EmailEnabled = false
	if sent := c.ProcessPendingEmails(context.Background()); sent != 0 || smtp.count() != 0 {
		t.Fatalf("sent %d with EMAIL_ENABLED off", sent)
	}
	if status, _, _ := outboxRow(t, database, entry.ID); status != "pending" {
		t.Errorf("status %q, want it still pending", status)
	}
	if err := c.SendNow(context.Background(), entry.ID); err != ErrEmailDisabled {
		t.Errorf("send-now with email off: %v", err)
	}
	if err := c.QueueEmail(context.Background(), SendParams{TemplateName: "welcome.html"}); err != ErrEmailDisabled {
		t.Errorf("QueueEmail with email off: %v, want ErrEmailDisabled", err)
	}
}

// A relay that is down gets two more chances, ten minutes and an hour later,
// before the email is marked failed.
func TestFailedTriesAreRescheduledThenFail(t *testing.T) {
	c, database, smtp := deliveryClient(t)
	user := seedDebtor(t, database, "owing", 2)
	entry := queueDebt(t, c, database, user, TierNegativeBalance, -2000)
	smtp.setFailing(true)

	for try := int64(1); try <= 3; try++ {
		if sent := c.ProcessPendingEmails(context.Background()); sent != 0 {
			t.Fatalf("try %d: sent %d", try, sent)
		}
		status, attempts, lastErr := outboxRow(t, database, entry.ID)
		want := "pending"
		if try == 3 {
			want = "failed"
		}
		if status != want || attempts != try || !strings.Contains(lastErr, "451") {
			t.Fatalf("after try %d: status %q, attempts %d, last_error %q", try, status, attempts, lastErr)
		}
		// Make the next try due without waiting for it.
		if _, err := database.Exec(`UPDATE email_outbox SET next_retry_at = datetime('now', '-1 second') WHERE id = ?`, entry.ID); err != nil {
			t.Fatal(err)
		}
	}

	smtp.setFailing(false)
	if err := c.RetryEmail(context.Background(), entry.ID); err != nil {
		t.Fatalf("retry by hand: %v", err)
	}
	if status, _, _ := outboxRow(t, database, entry.ID); status != "sent" || smtp.count() != 1 {
		t.Errorf("after the retry: status %q, %d messages", status, smtp.count())
	}
}

// "Send now" used to mail the reminder as it was queued. It now takes the same
// last look as the worker: the member who paid meanwhile is not dunned.
func TestSendNowWithdrawsAReminderThatIsNoLongerDue(t *testing.T) {
	c, database, smtp := deliveryClient(t)
	user := seedDebtor(t, database, "payer", 2)
	entry := queueDebt(t, c, database, user, TierNegativeBalance, -2000)
	pay(t, database, user, "payer", 2000)

	err := c.SendNow(context.Background(), entry.ID)
	if err == nil || !strings.Contains(err.Error(), "dluh mezitím uhrazen") {
		t.Fatalf("send-now: %v, want the withdrawal reason", err)
	}
	if status, _, _ := outboxRow(t, database, entry.ID); status != "cancelled" || smtp.count() != 0 {
		t.Errorf("status %q, %d messages; want cancelled and nothing sent", status, smtp.count())
	}
	if err := c.SendNow(context.Background(), entry.ID); err == nil {
		t.Error("send-now of a cancelled email succeeded")
	}
}

// An admin who accepts a member and suspends them within the day must not
// have the welcome go out.
func TestStateEmailsAreCancelledWhenTheStateChanged(t *testing.T) {
	c, database, smtp := deliveryClient(t)
	ctx := context.Background()
	userID := seedDebtor(t, database, "flipped", 0)
	user, err := c.queries.GetUserByID(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Scheduled(0).SendWelcome(ctx, &user); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE users SET state = 'suspended' WHERE id = ?`, userID); err != nil {
		t.Fatal(err)
	}

	c.ProcessPendingEmails(ctx)
	var status, lastErr string
	if err := database.QueryRow(`SELECT status, last_error FROM email_outbox WHERE user_id = ?`, userID).Scan(&status, &lastErr); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" || !strings.Contains(lastErr, "už není přijatý člen") || smtp.count() != 0 {
		t.Errorf("welcome to a suspended member: status %q, reason %q, %d sent", status, lastErr, smtp.count())
	}
}

func TestNormalizeLocale(t *testing.T) {
	for in, want := range map[string]string{
		"cs": "cs", "en": "en", "en-US": "en", "EN_gb": "en", "cs-CZ": "cs", " en ": "en",
		"": "", "de": "", "sk": "",
	} {
		if got := NormalizeLocale(in); got != want {
			t.Errorf("NormalizeLocale(%q) = %q, want %q", in, got, want)
		}
	}
}
