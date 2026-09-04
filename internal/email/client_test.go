package email

import (
	"mime"
	"strings"
	"testing"

	"github.com/base48/member-portal/internal/config"
)

// Every subject the portal actually ships, so a new template with an unencoded
// character cannot pass unnoticed.
var templateSubjects = []string{
	"Tvůj účet v Base48 je připraven",
	"Vítej v Base48 — jsi členem!",
	"Záporná bilance členského příspěvku",
	"⚠️ Upozornění na dluh za členství",
	"Pozastavení členství v Base48",
	"Nezaplacené členské příspěvky v Base48",
	"Your Base48 account is ready",
	"Welcome to Base48 — you're a member!",
	"Negative membership fee balance",
	"⚠️ Membership debt warning",
	"Base48 membership suspended",
}

func testClient(fromName string) *Client {
	return &Client{config: &config.Config{
		SMTPFrom:     "noreply@base48.cz",
		SMTPFromName: fromName,
		SMTPReplyTo:  "rada@lists.base48.cz",
	}}
}

// headerBlock returns everything before the body separator.
func headerBlock(t *testing.T, msg string) string {
	t.Helper()
	head, _, found := strings.Cut(msg, "\r\n\r\n")
	if !found {
		t.Fatal("message has no header/body separator")
	}
	return head
}

// headerValue returns the value of a single unfolded header line.
func headerValue(t *testing.T, head, name string) string {
	t.Helper()
	for _, line := range strings.Split(head, "\r\n") {
		if after, ok := strings.CutPrefix(line, name+": "); ok {
			return after
		}
	}
	t.Fatalf("no %s header found in:\n%s", name, head)
	return ""
}

// A single non-ASCII byte in the headers makes Postfix demand SMTPUTF8, which
// Seznam and Protonmail refuse — a permanent bounce the portal reports as sent.
func TestFormatMessageHeadersAreASCII(t *testing.T) {
	c := testClient("Base48 Hackerspace")

	for _, subject := range templateSubjects {
		t.Run(subject, func(t *testing.T) {
			head := headerBlock(t, c.formatMessage("clen@seznam.cz", subject, "<p>tělo zprávy</p>"))
			for i, b := range []byte(head) {
				if b > 127 {
					t.Fatalf("byte %d in header block is non-ASCII (%#x); headers must be RFC 2047 encoded\n%s", i, b, head)
				}
			}
		})
	}
}

// RFC 5322 caps a line at 998 octets. Q-encoding inflates an accented subject
// to roughly three times its length, so a long one has room to overshoot.
func TestFormatMessageHeaderLinesFitRFC5322(t *testing.T) {
	c := testClient("Base48 Hackerspace")

	for _, subject := range templateSubjects {
		head := headerBlock(t, c.formatMessage("clen@seznam.cz", subject, "body"))
		for _, line := range strings.Split(head, "\r\n") {
			if len(line) > 998 {
				t.Errorf("header line is %d octets, over the 998 limit: %q", len(line), line)
			}
		}
	}
}

func TestFormatMessageSubjectSurvivesRoundTrip(t *testing.T) {
	c := testClient("Base48 Hackerspace")
	dec := new(mime.WordDecoder)

	for _, subject := range templateSubjects {
		t.Run(subject, func(t *testing.T) {
			head := headerBlock(t, c.formatMessage("clen@seznam.cz", subject, "body"))

			raw := headerValue(t, head, "Subject")

			got, err := dec.DecodeHeader(raw)
			if err != nil {
				t.Fatalf("decoding %q: %v", raw, err)
			}
			if got != subject {
				t.Errorf("subject did not survive encoding\n  want: %q\n  got:  %q", subject, got)
			}
		})
	}
}

// Q-encoding leaves plain ASCII alone, which keeps English subjects readable
// in the raw message and in mail logs.
func TestFormatMessageLeavesASCIISubjectUnencoded(t *testing.T) {
	c := testClient("Base48 Hackerspace")
	head := headerBlock(t, c.formatMessage("clen@example.com", "Your Base48 account is ready", "body"))

	if !strings.Contains(head, "Subject: Your Base48 account is ready\r\n") {
		t.Errorf("ASCII subject should pass through verbatim, got:\n%s", head)
	}
	if !strings.Contains(head, "From: Base48 Hackerspace <noreply@base48.cz>\r\n") {
		t.Errorf("ASCII display name should pass through verbatim, got:\n%s", head)
	}
}

func TestFormatMessageEncodesDisplayName(t *testing.T) {
	c := testClient("Base48 Hackerspace — Rada")
	head := headerBlock(t, c.formatMessage("clen@seznam.cz", "Ahoj", "body"))

	from := headerValue(t, head, "From")
	if !strings.HasSuffix(from, " <noreply@base48.cz>") {
		t.Fatalf("From lost its address: %q", from)
	}

	name := strings.TrimSuffix(from, " <noreply@base48.cz>")
	got, err := new(mime.WordDecoder).DecodeHeader(name)
	if err != nil {
		t.Fatalf("decoding display name %q: %v", name, err)
	}
	if got != "Base48 Hackerspace — Rada" {
		t.Errorf("display name did not survive encoding\n  want: %q\n  got:  %q", "Base48 Hackerspace — Rada", got)
	}
}

// The body carries UTF-8, so the message has to say so — otherwise it claims
// to be 7bit and Postfix cannot downgrade it for a non-8BITMIME server.
func TestFormatMessageDeclaresEncoding(t *testing.T) {
	c := testClient("Base48 Hackerspace")
	head := headerBlock(t, c.formatMessage("clen@seznam.cz", "Ahoj", "<p>tělo</p>"))

	for _, want := range []string{
		"MIME-Version: 1.0",
		"Content-Type: text/html; charset=UTF-8",
		"Content-Transfer-Encoding: 8bit",
		"Reply-To: rada@lists.base48.cz",
	} {
		if !strings.Contains(head, want) {
			t.Errorf("missing header %q in:\n%s", want, head)
		}
	}
}
