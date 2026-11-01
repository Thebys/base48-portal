package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/dues"
	"github.com/base48/member-portal/internal/email"
)

// The e-mail admin, /admin/emails: the outbox (what is waiting and what went
// out), the templates (texts, preview, test email) and the reminders
// (settings and the manual picker). Everything about email used to live in
// the settings page and the finance page.

// emailTab is one tab of the e-mail admin.
type emailTab struct {
	Path, Label string
	Active      bool
}

func emailTabs(active string) []emailTab {
	tabs := []emailTab{
		{Path: "/admin/emails", Label: "Fronta"},
		{Path: "/admin/emails/templates", Label: "Šablony"},
		{Path: "/admin/emails/reminders", Label: "Upozornění"},
	}
	for i := range tabs {
		tabs[i].Active = tabs[i].Path == active
	}
	return tabs
}

// emailPage is the data every e-mail admin page starts from: the tabs and
// whether mail can leave at all.
func (h *Handler) emailPage(r *http.Request, title, tab string) map[string]any {
	smtpConfigured := h.config.SMTPHost != "" && h.config.SMTPPort != 0
	smtpReachable := false
	if smtpConfigured {
		addr := net.JoinHostPort(h.config.SMTPHost, strconv.Itoa(h.config.SMTPPort))
		if conn, err := net.DialTimeout("tcp", addr, 3*time.Second); err == nil {
			conn.Close()
			smtpReachable = true
		}
	}
	return map[string]any{
		"Title":          title,
		"User":           h.auth.GetUser(r),
		"Tabs":           emailTabs(tab),
		"EmailEnabled":   h.config.EmailEnabled,
		"SMTPConfigured": smtpConfigured,
		"SMTPReachable":  smtpReachable,
		"SMTPSkipTLS":    h.config.SMTPSkipTLS,
	}
}

// outboxRow is an outbox entry as the admin sees it.
type outboxRow struct {
	db.EmailOutbox
	Type        string // Czech name of the email
	StatusLabel string
	StatusClass string
	When        string // when it leaves, left or was given up
	Busy        bool   // a worker is sending it right now
}

func newOutboxRow(e db.EmailOutbox, now time.Time) outboxRow {
	row := outboxRow{EmailOutbox: e, Type: email.TemplateLabel(e.TemplateName)}
	local := func(t time.Time) string { return t.Local().Format("2. 1. 2006 15:04") }
	row.Busy = e.Status == "pending" && e.LeasedUntil.Valid && e.LeasedUntil.Time.After(now)
	switch e.Status {
	case "pending":
		row.StatusLabel, row.StatusClass = "čeká", "badge-warning"
		switch {
		case row.Busy:
			row.StatusLabel = "odesílá se"
			row.When = "teď"
		case e.NextRetryAt.Valid && e.NextRetryAt.Time.After(now):
			row.When = "odejde " + local(e.NextRetryAt.Time)
		case e.NextRetryAt.Valid:
			row.When = "odejde při příštím průchodu"
		default:
			row.When = "nenaplánováno"
		}
	case "sent":
		row.StatusLabel, row.StatusClass = "odesláno", "badge-success"
		if e.SentAt.Valid {
			row.When = local(e.SentAt.Time)
		}
	case "failed":
		row.StatusLabel, row.StatusClass = "selhalo", "badge-danger"
		row.When = "zařazeno " + local(e.CreatedAt)
	case "cancelled":
		row.StatusLabel, row.StatusClass = "zrušeno", "badge-gray"
		row.When = "zařazeno " + local(e.CreatedAt)
	default:
		row.StatusLabel, row.StatusClass = e.Status, "badge-gray"
	}
	return row
}

// outboxFilters are the states the finished list can be narrowed to.
var outboxFilters = []struct{ Status, Label string }{
	{"", "Vše"},
	{"sent", "Odeslané"},
	{"failed", "Selhané"},
	{"cancelled", "Zrušené"},
}

// AdminEmailsHandler shows the outbox: every email still waiting, then the
// last hundred that went out, failed or were cancelled.
// GET /admin/emails?status=sent|failed|cancelled
func (h *Handler) AdminEmailsHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := time.Now()
	filter := r.URL.Query().Get("status")

	pending, err := h.queries.ListPendingEmailOutbox(ctx)
	if err != nil {
		http.Error(w, "Frontu nejde načíst", http.StatusInternalServerError)
		return
	}
	var finished []db.EmailOutbox
	switch filter {
	case "sent", "failed", "cancelled":
		finished, err = h.queries.ListEmailOutboxWithStatus(ctx, db.ListEmailOutboxWithStatusParams{Status: filter, Limit: 100})
	default:
		filter = ""
		finished, err = h.queries.ListFinishedEmailOutbox(ctx, 100)
	}
	if err != nil {
		http.Error(w, "Frontu nejde načíst", http.StatusInternalServerError)
		return
	}

	counts := map[string]int64{"pending": 0, "sent": 0, "failed": 0, "cancelled": 0}
	if rows, err := h.queries.CountEmailOutboxByStatus(ctx); err == nil {
		for _, c := range rows {
			counts[c.Status] = c.Count
		}
	}
	sentToday, _ := h.queries.CountEmailOutboxSentToday(ctx)

	rows := func(entries []db.EmailOutbox) []outboxRow {
		out := make([]outboxRow, len(entries))
		for i, e := range entries {
			out[i] = newOutboxRow(h.emailClient.AsItWouldLeave(ctx, e), now)
		}
		return out
	}

	data := h.emailPage(r, "E-maily", "/admin/emails")
	data["Pending"] = rows(pending)
	data["Finished"] = rows(finished)
	data["Filter"] = filter
	data["Filters"] = outboxFilters
	data["Counts"] = counts
	data["SentToday"] = sentToday
	h.render(w, "admin_emails.html", data)
}

// AdminEmailDetailHandler shows one email from the outbox as it was (or will
// be) sent.
// GET /admin/emails/{id}
func (h *Handler) AdminEmailDetailHandler(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	entry, err := h.queries.GetEmailOutbox(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "E-mail nejde načíst", http.StatusInternalServerError)
		return
	}
	data := h.emailPage(r, "E-mail #"+strconv.FormatInt(id, 10), "/admin/emails")
	entry = h.emailClient.AsItWouldLeave(r.Context(), entry)
	data["Email"] = newOutboxRow(entry, time.Now())
	// A string, not template.HTML: html/template escapes it into the srcdoc
	// attribute, and the sandboxed frame shows it as the mail client would.
	data["HTML"] = entry.RenderedHtml.String
	h.render(w, "admin_email.html", data)
}

// emailSample is an email built for the admin to look at.
type emailSample struct {
	File, Subject, Preheader string
	Data                     any
}

// buildEmailSample builds template key as user would get it in lang, in the
// scenario (see email.Scenarios), with client's texts: the saved ones, or a
// draft. The membership reminders are built by the very code that builds the
// real ones, at the user's own fee (1000 Kč without one).
func (h *Handler) buildEmailSample(ctx context.Context, client *email.Client, key, scenario, lang string, user *db.User) (emailSample, error) {
	if email.IsMembershipTemplate(email.TemplateFileForTier(key)) {
		sc := email.Scenarios(key)[0]
		for _, s := range email.Scenarios(key) {
			if s.Key == scenario {
				sc = s
			}
		}
		fee := 1000.0
		if level, err := h.queries.GetLevel(ctx, user.LevelID); err == nil {
			if f := dues.MonthlyFee(user.LevelActualAmount, level.Amount); f > 0 {
				fee = f
			}
		}
		st := dues.Standing{Fee: fee, Balance: math.Round(sc.Balance * fee), Anchor: dues.MonthOf(time.Now())}
		e, err := client.MembershipEmailFor(ctx, user, lang, key, st)
		if err != nil {
			return emailSample{}, err
		}
		return emailSample{File: email.TemplateFileForTier(key), Subject: e.Content["subject"], Preheader: e.Content["preheader"], Data: e}, nil
	}

	content := client.LoadContentBlocks(ctx, key, lang)
	fields := map[string]any{
		"Name":      user.DisplayName(),
		"Username":  user.Username.String,
		"PortalURL": h.config.BaseURL,
		"Content":   content,
		"Labels":    email.EmailLabels(lang),
	}
	switch key {
	case "registration", "welcome", "membership_suspended":
	case email.TemplateBarDebt:
		threshold := email.LoadBarDebtSettings(ctx, h.queries).ThresholdCZK
		balance := -(threshold + 150) * 100
		fields["BalanceCents"] = balance
		fields["BalanceText"] = email.FormatCZKCents(balance)
		fields["ThresholdText"] = strconv.FormatInt(threshold, 10)
	default:
		return emailSample{}, fmt.Errorf("neznámá šablona: %s", key)
	}
	return emailSample{File: key + ".html", Subject: content["subject"], Preheader: content["preheader"], Data: fields}, nil
}

// previewMember is who the template preview is addressed to.
func previewMember(lang string) *db.User {
	return &db.User{
		Username:   sql.NullString{String: "jan.novak", Valid: true},
		Realname:   sql.NullString{String: "Jan Novák", Valid: true},
		PaymentsID: sql.NullString{String: "12345", Valid: true},
		Locale:     lang,
	}
}

// templateBlock is one text block in the editor.
type templateBlock struct {
	Name, Label   string
	Text, Default string
	Modified      bool // Text differs from Default
	Saved         bool // Text is what is saved
	Rows          int
}

// templateListItem is a template in the editor's list.
type templateListItem struct {
	Key, Label string
	Modified   int // blocks whose saved text is not the default
	Active     bool
}

// AdminEmailTemplatesHandler is the template editor. The form holds every
// block of one template in one language; Náhled shows it unsaved, Uložit
// saves it, Poslat test mails it to the admin as it stands, and Obnovit
// puts one block back to its default text.
// GET, POST /admin/emails/templates?t=prepaid_ending&lang=cs&s=short
func (h *Handler) AdminEmailTemplatesHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Formulář nejde přečíst", http.StatusBadRequest)
		return
	}
	spec, ok := email.TemplateByKey(r.FormValue("t"))
	if !ok {
		spec = email.Templates[0]
	}
	lang := r.FormValue("lang")
	if lang != "en" {
		lang = "cs"
	}
	scenario := r.FormValue("s")
	here := func(extra url.Values) string {
		q := url.Values{"t": {spec.Key}, "lang": {lang}}
		if scenario != "" {
			q.Set("s", scenario)
		}
		for k, v := range extra {
			q[k] = v
		}
		return "/admin/emails/templates?" + q.Encode()
	}

	defaults := email.GetDefaultContentBlocks(lang)[spec.Key]
	saved := h.emailClient.LoadRawContentBlocks(ctx, spec.Key, lang)
	var notice, problem string

	// What the form holds; on a plain visit, what is saved.
	text := saved
	draft := false
	if r.Method == http.MethodPost {
		text = make(map[string]string, len(spec.Blocks))
		for _, b := range spec.Blocks {
			text[b.Name] = email.NormalizeBlockText(strings.ReplaceAll(r.PostFormValue("block."+b.Name), "\r\n", "\n"))
		}
		action := r.PostFormValue("action")
		admin := h.adminEmail(r)
		switch {
		case action == "save":
			if err := h.saveTemplateBlocks(ctx, spec.Key, lang, text, admin); err != nil {
				problem = err.Error()
				break
			}
			http.Redirect(w, r, here(url.Values{"saved": {"1"}}), http.StatusSeeOther)
			return
		case strings.HasPrefix(action, "reset:"):
			block := strings.TrimPrefix(action, "reset:")
			if _, known := defaults[block]; !known {
				http.Error(w, "Neznámý blok", http.StatusBadRequest)
				return
			}
			if err := h.saveTemplateBlocks(ctx, spec.Key, lang, map[string]string{block: defaults[block]}, admin); err != nil {
				problem = err.Error()
				break
			}
			http.Redirect(w, r, here(url.Values{"reset": {block}}), http.StatusSeeOther)
			return
		case action == "test", action == "preview":
			draft = true
		default:
			http.Error(w, "Neznámá akce", http.StatusBadRequest)
			return
		}
	}
	switch {
	case r.URL.Query().Get("saved") != "":
		notice = "Uloženo."
	case r.URL.Query().Get("reset") != "":
		notice = "Blok vrácen na výchozí text."
	}

	for name, t := range text {
		if err := spec.CheckPlaceholders(t); err != nil && problem == "" {
			problem = "Blok " + name + ": " + err.Error()
		}
	}

	client := h.emailClient
	if draft {
		client = client.WithDraft(spec.Key, lang, text)
	}
	user := previewMember(lang)
	var adminUser db.User
	if r.PostFormValue("action") == "test" {
		var err error
		adminUser, err = h.queries.GetUserByKeycloakID(ctx, sql.NullString{String: h.auth.GetUser(r).ID, Valid: true})
		if err != nil {
			http.Error(w, "Tvůj účet v portálu nejde načíst", http.StatusInternalServerError)
			return
		}
		user = &adminUser
	}

	sample, sampleErr := h.buildEmailSample(ctx, client, spec.Key, scenario, lang, user)
	var html string
	if sampleErr == nil {
		html, sampleErr = client.RenderPreview(email.SendParams{TemplateName: sample.File, Data: sample.Data})
	}
	if sampleErr != nil && problem == "" {
		problem = "Náhled nejde vykreslit: " + sampleErr.Error()
	}

	if r.PostFormValue("action") == "test" && problem == "" {
		sendErr := client.SendTemplated(ctx, email.SendParams{
			UserID:       sql.NullInt64{Int64: adminUser.ID, Valid: true},
			Recipient:    adminUser.Email,
			Subject:      "[TEST] " + sample.Subject,
			TemplateName: sample.File,
			Data:         sample.Data,
		})
		level, message := "success", fmt.Sprintf("Testovací e-mail %s (%s) odeslán na %s", spec.Key, lang, adminUser.Email)
		if sendErr != nil {
			level, message = "error", fmt.Sprintf("Testovací e-mail %s (%s) na %s se nepodařilo odeslat: %v", spec.Key, lang, adminUser.Email, sendErr)
			problem = "Testovací e-mail se nepodařilo odeslat: " + sendErr.Error()
		} else {
			notice = "Testovací e-mail odešel na " + adminUser.Email + ", tak jak ho vidíš v náhledu (i s neuloženými změnami)."
		}
		h.queries.CreateLog(ctx, db.CreateLogParams{
			Subsystem: "email", Level: level,
			UserID:  sql.NullInt64{Int64: adminUser.ID, Valid: true},
			Message: message,
		})
	}

	blocks := make([]templateBlock, len(spec.Blocks))
	unsaved := false
	for i, b := range spec.Blocks {
		blocks[i] = templateBlock{
			Name: b.Name, Label: b.Label,
			Text: text[b.Name], Default: defaults[b.Name],
			Modified: text[b.Name] != defaults[b.Name],
			Saved:    text[b.Name] == saved[b.Name],
			Rows:     max(1, min(8, strings.Count(text[b.Name], "\n")+1+len(text[b.Name])/90)),
		}
		if strings.HasPrefix(b.Name, "subject") {
			blocks[i].Rows = 1
		}
		if !blocks[i].Saved {
			unsaved = true
		}
	}

	list := make([]templateListItem, len(email.Templates))
	for i, t := range email.Templates {
		list[i] = templateListItem{Key: t.Key, Label: t.Label, Active: t.Key == spec.Key}
		defs := email.GetDefaultContentBlocks(lang)[t.Key]
		for name, v := range h.emailClient.LoadRawContentBlocks(ctx, t.Key, lang) {
			if v != defs[name] {
				list[i].Modified++
			}
		}
	}

	pending, _ := h.queries.CountPendingEmailsByTemplate(ctx, spec.File())

	data := h.emailPage(r, "Šablony e-mailů", "/admin/emails/templates")
	data["Templates"] = list
	data["Spec"] = spec
	data["Lang"] = lang
	data["Blocks"] = blocks
	data["Scenarios"] = email.Scenarios(spec.Key)
	data["Scenario"] = scenario
	data["Sample"] = sample
	data["HTML"] = html
	data["Unsaved"] = unsaved
	data["Pending"] = pending
	data["Notice"] = notice
	data["Problem"] = problem
	if problem != "" && r.Method == http.MethodPost {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}
	h.render(w, "admin_email_templates.html", data)
}

// AdminEmailRemindersHandler shows the reminder settings and the manual
// picker: who a membership reminder is due for now.
// GET /admin/emails/reminders
func (h *Handler) AdminEmailRemindersHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	membership := email.LoadMembershipSettings(ctx, h.queries)
	barDebt := email.LoadBarDebtSettings(ctx, h.queries)
	barDebtors, err := h.queries.ListAcceptedBarDebtors(ctx, barDebt.MaxBalanceCents())
	if err != nil {
		log.Printf("[Handler] failed to list bar debtors: %v", err)
	}
	candidates, err := h.buildDebtCandidates(ctx)
	if err != nil {
		http.Error(w, "Bilance nejdou načíst", http.StatusInternalServerError)
		return
	}
	sendable := 0
	for _, c := range candidates {
		if !c.Blocked {
			sendable++
		}
	}

	data := h.emailPage(r, "Upozornění", "/admin/emails/reminders")
	data["Membership"] = membership
	data["PrepaidNow"] = h.prepaidNow(ctx, membership)
	data["BarDebt"] = barDebt
	data["BarDebtorsNow"] = len(barDebtors)
	data["DebtCandidates"] = candidates
	data["DebtSendable"] = sendable
	h.render(w, "admin_email_reminders.html", data)
}

// AdminEmailMemberPreviewHandler shows the membership reminder a member is
// due now, exactly as it would go out, or says they are due none.
// GET /admin/emails/member/{id}
func (h *Handler) AdminEmailMemberPreviewHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	user, err := h.queries.GetUserByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "Člena nejde načíst", http.StatusInternalServerError)
		return
	}
	level, err := h.queries.GetLevel(ctx, user.LevelID)
	if err != nil {
		http.Error(w, "Úroveň člena nejde načíst", http.StatusInternalServerError)
		return
	}
	now := time.Now()
	st, err := dues.Load(ctx, h.queries, &user, level.Amount, now)
	if err != nil {
		http.Error(w, "Bilanci nejde načíst", http.StatusInternalServerError)
		return
	}
	settings := email.LoadMembershipSettings(ctx, h.queries)
	tier := dues.Classify(st, dues.MonthOf(now), settings.Policy())
	switchedOff := false
	if tier == dues.None && !settings.PrepaidEnabled {
		policy := settings.Policy()
		policy.Prepaid = true
		if t := dues.Classify(st, dues.MonthOf(now), policy); t != dues.None {
			tier, switchedOff = t, true
		}
	}

	data := h.emailPage(r, "Upozornění pro "+user.DisplayName(), "/admin/emails/reminders")
	data["Member"] = user
	data["Accepted"] = user.State == "accepted"
	data["Standing"] = st
	data["PaidThrough"] = email.MonthUntil(st.PaidThrough(), "cs")
	data["Tier"] = tier
	data["SwitchedOff"] = switchedOff
	if tier != dues.None && user.State == "accepted" {
		lang := email.NormalizeLocale(user.Locale)
		if lang == "" {
			lang = "cs"
		}
		e, err := h.emailClient.MembershipEmailFor(ctx, &user, lang, tier, st)
		if err == nil {
			var html string
			html, err = h.emailClient.RenderPreview(email.SendParams{TemplateName: email.TemplateFileForTier(tier), Data: e})
			data["Subject"] = e.Content["subject"]
			data["HTML"] = html
		}
		if err != nil {
			data["Problem"] = "Náhled nejde vykreslit: " + err.Error()
		}
	}
	h.render(w, "admin_email_member.html", data)
}

// saveTemplateBlocks stores the editor's blocks as overrides. A block equal to
// the default is stored as no override at all (and any old one removed), so a
// later change to the default in code still reaches it.
func (h *Handler) saveTemplateBlocks(ctx context.Context, templateName, lang string, blocks map[string]string, adminEmail string) error {
	if lang == "" {
		lang = "cs"
	}
	templateDefaults, ok := email.GetDefaultContentBlocks(lang)[templateName]
	if !ok {
		return fmt.Errorf("unknown template: %s", templateName)
	}
	spec, _ := email.TemplateByKey(templateName)
	for blockName, content := range blocks {
		if _, ok := templateDefaults[blockName]; !ok {
			return fmt.Errorf("unknown block: %s", blockName)
		}
		if err := spec.CheckPlaceholders(content); err != nil {
			return fmt.Errorf("blok %s: %w", blockName, err)
		}
	}
	current := h.emailClient.LoadRawContentBlocks(ctx, templateName, lang)

	for blockName, content := range blocks {
		content = email.NormalizeBlockText(content)

		if content == templateDefaults[blockName] {
			err := h.queries.DeleteEmailTemplateContent(ctx, db.DeleteEmailTemplateContentParams{
				TemplateName: templateName,
				BlockName:    blockName,
				Lang:         lang,
			})
			if err != nil {
				return fmt.Errorf("failed to reset block %s: %w", blockName, err)
			}
			continue
		}

		_, err := h.queries.UpsertEmailTemplateContent(ctx, db.UpsertEmailTemplateContentParams{
			TemplateName: templateName,
			BlockName:    blockName,
			Lang:         lang,
			Content:      content,
			UpdatedBy:    sql.NullString{String: adminEmail, Valid: adminEmail != ""},
		})
		if err != nil {
			return fmt.Errorf("failed to save block %s: %w", blockName, err)
		}
	}

	// What changed, and the text it replaced, so an edit can be undone.
	var changed []string
	before := map[string]string{}
	for blockName, content := range blocks {
		if content = email.NormalizeBlockText(content); content != current[blockName] {
			changed = append(changed, blockName)
			before[blockName] = current[blockName]
		}
	}
	if len(changed) > 0 {
		sort.Strings(changed)
		metadata, _ := json.Marshal(map[string]any{"template": templateName, "lang": lang, "admin": adminEmail, "before": before})
		h.queries.CreateLog(ctx, db.CreateLogParams{
			Subsystem: "email",
			Level:     "info",
			Message:   fmt.Sprintf("Šablona %s (%s): upraveno %s (%s)", templateName, lang, strings.Join(changed, ", "), adminEmail),
			Metadata:  sql.NullString{String: string(metadata), Valid: true},
		})
	}
	return nil
}

// outboxID reads the outbox entry an admin acts on, from a JSON body
// {"id": N} or a form field id.
func outboxID(r *http.Request) (int64, error) {
	var req struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.ID > 0 {
		return req.ID, nil
	}
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("chybí číslo e-mailu")
	}
	return id, nil
}

// adminEmail is the signed-in admin, for audit lines and test emails.
// RequireAdmin has let them in.
func (h *Handler) adminEmail(r *http.Request) string {
	if admin := h.auth.GetUser(r); admin != nil {
		return admin.Email
	}
	return ""
}

// auditOutbox records an admin's action on an outbox entry.
func (h *Handler) auditOutbox(r *http.Request, id int64, action string) {
	h.queries.CreateLog(context.WithoutCancel(r.Context()), db.CreateLogParams{
		Subsystem: "email",
		Level:     "info",
		Message:   fmt.Sprintf("E-mail #%d: %s (%s)", id, action, h.adminEmail(r)),
	})
}

// outboxActionError answers a send-now or retry that did not go out.
func (h *Handler) outboxActionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, email.ErrBusy):
		h.jsonError(w, "E-mail se právě odesílá, zkus to za pár minut", http.StatusConflict)
	case errors.Is(err, email.ErrEmailDisabled):
		h.jsonError(w, "E-maily jsou vypnuté (EMAIL_ENABLED=false)", http.StatusConflict)
	default:
		h.jsonError(w, "Neodesláno: "+err.Error(), http.StatusUnprocessableEntity)
	}
}

// AdminRetryEmailHandler sends a failed outbox email again.
// POST /api/admin/email/retry
func (h *Handler) AdminRetryEmailHandler(w http.ResponseWriter, r *http.Request) {
	id, err := outboxID(r)
	if err != nil {
		h.jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	sendErr := h.emailClient.RetryEmail(r.Context(), id)
	h.auditOutbox(r, id, "znovu odeslat: "+outcomeText(sendErr))
	if sendErr != nil {
		h.outboxActionError(w, sendErr)
		return
	}
	h.jsonSuccess(w, "E-mail odeslán")
}

// AdminCancelEmailHandler cancels a pending outbox email.
// POST /api/admin/email/cancel
func (h *Handler) AdminCancelEmailHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := outboxID(r)
	if err != nil {
		h.jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	_, err = h.queries.CancelEmailOutbox(ctx, db.CancelEmailOutboxParams{
		Reason: sql.NullString{String: "Zrušil(a) " + h.adminEmail(r), Valid: true},
		ID:     id,
	})
	if errors.Is(err, sql.ErrNoRows) {
		h.jsonError(w, "E-mail už nečeká na odeslání, nebo se právě odesílá", http.StatusConflict)
		return
	}
	if err != nil {
		h.jsonError(w, "Zrušení selhalo: "+err.Error(), http.StatusInternalServerError)
		return
	}
	h.auditOutbox(r, id, "zrušen")
	h.jsonSuccess(w, "E-mail zrušen")
}

// AdminSendNowEmailHandler delivers a pending outbox email without waiting
// for its time, after the same last look as the worker gives it.
// POST /api/admin/email/send-now
func (h *Handler) AdminSendNowEmailHandler(w http.ResponseWriter, r *http.Request) {
	id, err := outboxID(r)
	if err != nil {
		h.jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	sendErr := h.emailClient.SendNow(r.Context(), id)
	h.auditOutbox(r, id, "odeslat hned: "+outcomeText(sendErr))
	if sendErr != nil {
		h.outboxActionError(w, sendErr)
		return
	}
	h.jsonSuccess(w, "E-mail odeslán")
}

func outcomeText(err error) string {
	if err != nil {
		return err.Error()
	}
	return "odesláno"
}

// prepaidNow counts the members the reminder before any debt would go to now,
// switched on or not, so the admin knows what turning it on means.
func (h *Handler) prepaidNow(ctx context.Context, s email.MembershipSettings) int {
	users, err := h.queries.ListAcceptedUsersForFees(ctx)
	if err != nil {
		log.Printf("[Handler] failed to list members: %v", err)
		return 0
	}
	now := time.Now()
	standings, err := dues.LoadAll(ctx, h.queries, users, now)
	if err != nil {
		log.Printf("[Handler] failed to load standings: %v", err)
		return 0
	}
	policy := s.Policy()
	policy.Prepaid = true
	n := 0
	for _, st := range standings {
		if dues.Classify(st, dues.MonthOf(now), policy) == email.TierPrepaidEnding {
			n++
		}
	}
	return n
}

// AdminSaveMembershipEmailSettingsHandler saves the membership reminder settings.
// POST /api/admin/membership-email-settings
func (h *Handler) AdminSaveMembershipEmailSettingsHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		PrepaidEnabled bool `json:"prepaid_enabled"`
		MonthsAhead    int  `json:"months_ahead"`
		SuggestMonths  int  `json:"suggest_months"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	settings := email.MembershipSettings{
		PrepaidEnabled: req.PrepaidEnabled,
		MonthsAhead:    req.MonthsAhead,
		SuggestMonths:  req.SuggestMonths,
	}
	if err := settings.Validate(); err != nil {
		h.jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := email.SaveMembershipSettings(ctx, h.queries, settings); err != nil {
		h.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	state := "vypnuto"
	if settings.PrepaidEnabled {
		state = "zapnuto"
	}
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "email",
		Level:     "info",
		Message: fmt.Sprintf("Upozornění na příspěvky: před koncem předplatného %s, %d měsíc(e) předem, doporučená platba na %d měsíců (%s)",
			state, settings.MonthsAhead, settings.SuggestMonths, h.adminEmail(r)),
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Nastavení uloženo",
		"prepaid": h.prepaidNow(ctx, settings),
	})
}

// AdminSaveBarDebtSettingsHandler saves the bar debt reminder settings.
// POST /api/admin/bar-debt-settings
func (h *Handler) AdminSaveBarDebtSettingsHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req struct {
		Enabled      bool  `json:"enabled"`
		ThresholdCZK int64 `json:"threshold_czk"`
		IntervalDays int   `json:"interval_days"`
		DelayHours   int   `json:"delay_hours"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	settings := email.BarDebtSettings{
		Enabled:      req.Enabled,
		ThresholdCZK: req.ThresholdCZK,
		IntervalDays: req.IntervalDays,
		DelayHours:   req.DelayHours,
	}
	if err := settings.Validate(); err != nil {
		h.jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := email.SaveBarDebtSettings(ctx, h.queries, settings); err != nil {
		h.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	adminEmail := ""
	if admin := h.auth.GetUser(r); admin != nil {
		adminEmail = admin.Email
	}
	state := "disabled"
	if settings.Enabled {
		state = "enabled"
	}
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "email",
		Level:     "info",
		Message: fmt.Sprintf("Bar debt reminders %s: debt above %d Kč, every %d days, delay %dh (by %s)",
			state, settings.ThresholdCZK, settings.IntervalDays, settings.DelayHours, adminEmail),
	})

	debtors, err := h.queries.ListAcceptedBarDebtors(ctx, settings.MaxBalanceCents())
	if err != nil {
		log.Printf("[Handler] failed to list bar debtors: %v", err)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Nastavení uloženo",
		"debtors": len(debtors),
	})
}
