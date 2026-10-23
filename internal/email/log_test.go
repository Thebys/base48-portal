package email

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// The metadata used to be formatted into a JSON string, so a quote in an
// address, a subject or an SMTP error ended the string and could add fields.
func TestEmailLogMetadataIsJSON(t *testing.T) {
	c, database := barTestClient(t)
	params := SendParams{
		Recipient:    `"x\",\"template\":\"forged"@base48.cz`,
		Subject:      `Dluh "bar" \ 100 Kč`,
		TemplateName: "bar_debt.html",
	}
	c.logEmail(context.Background(), params, errors.New(`550 "no such user"`))

	var raw string
	if err := database.QueryRow(`SELECT metadata FROM system_logs WHERE subsystem = 'email' ORDER BY id DESC LIMIT 1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("metadata is not JSON: %v\n%s", err, raw)
	}
	want := map[string]string{"recipient": params.Recipient, "subject": params.Subject, "template": "bar_debt.html", "error": `550 "no such user"`}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("metadata %s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("metadata has fields %v, want exactly %v", got, want)
	}
}
