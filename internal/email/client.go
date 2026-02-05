package email

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"html/template"
	"log"
	"math"
	"net/smtp"
	"path/filepath"

	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/qrpay"
)

// Client handles email sending with templates and logging
type Client struct {
	config       *config.Config
	queries      *db.Queries
	qrpayService *qrpay.Service
}

// SendParams contains parameters for sending a templated email
type SendParams struct {
	UserID       sql.NullInt64
	Recipient    string
	Subject      string
	TemplateName string
	Data         interface{}
}

// New creates a new email client
func New(cfg *config.Config, queries *db.Queries, qrService *qrpay.Service) *Client {
	return &Client{
		config:       cfg,
		queries:      queries,
		qrpayService: qrService,
	}
}

// SendTemplated sends an email using an HTML template
// This is the main DRY method - all other methods use this internally
func (c *Client) SendTemplated(ctx context.Context, params SendParams) error {
	// Skip if SMTP not configured
	if c.config.SMTPHost == "" {
		log.Printf("[Email] SMTP not configured, skipping email to %s (template: %s)", params.Recipient, params.TemplateName)
		return nil
	}

	// Load and parse template
	templatePath := filepath.Join(c.config.WebRoot, "templates", "email", params.TemplateName)
	tmpl, err := template.ParseFiles(templatePath)
	if err != nil {
		return c.logEmail(ctx, params, fmt.Errorf("template parse error: %w", err))
	}

	// Execute template
	var body bytes.Buffer
	if err := tmpl.Execute(&body, params.Data); err != nil {
		return c.logEmail(ctx, params, fmt.Errorf("template execution error: %w", err))
	}

	// Prepare email message
	message := c.formatMessage(params.Recipient, params.Subject, body.String())

	// Send email
	auth := smtp.PlainAuth("", c.config.SMTPUsername, c.config.SMTPPassword, c.config.SMTPHost)
	addr := fmt.Sprintf("%s:%d", c.config.SMTPHost, c.config.SMTPPort)

	err = smtp.SendMail(
		addr,
		auth,
		c.config.SMTPFrom,
		[]string{params.Recipient},
		[]byte(message),
	)

	// Log result (success or failure)
	return c.logEmail(ctx, params, err)
}

// formatMessage creates RFC 2822 compliant email message
func (c *Client) formatMessage(to, subject, body string) string {
	return fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s",
		c.config.SMTPFrom,
		to,
		subject,
		body,
	)
}

// logEmail logs the email attempt to database
func (c *Client) logEmail(ctx context.Context, params SendParams, err error) error {
	level := "success"
	message := fmt.Sprintf("Email sent to %s: %s", params.Recipient, params.Subject)
	metadata := fmt.Sprintf(`{"recipient":"%s","subject":"%s","template":"%s"}`,
		params.Recipient, params.Subject, params.TemplateName)

	if err != nil {
		level = "error"
		message = fmt.Sprintf("Failed to send email to %s: %v", params.Recipient, err)
		metadata = fmt.Sprintf(`{"recipient":"%s","subject":"%s","template":"%s","error":"%s"}`,
			params.Recipient, params.Subject, params.TemplateName, err.Error())
		log.Printf("[Email] %s", message)
	} else {
		log.Printf("[Email] %s", message)
	}

	// Log to database (don't fail if this errors)
	if c.queries != nil {
		if _, dbErr := c.queries.CreateLog(ctx, db.CreateLogParams{
			Subsystem: "email",
			Level:     level,
			UserID:    params.UserID,
			Message:   message,
			Metadata:  sql.NullString{String: metadata, Valid: true},
		}); dbErr != nil {
			log.Printf("[Email] Warning: failed to log to database: %v", dbErr)
		}
	}

	return err
}

// SendWelcome sends welcome email to newly accepted member
func (c *Client) SendWelcome(ctx context.Context, user *db.User) error {
	data := map[string]interface{}{
		"Name":      user.Realname.String,
		"Username":  user.Username.String,
		"PortalURL": c.config.BaseURL,
	}

	return c.SendTemplated(ctx, SendParams{
		UserID:       sql.NullInt64{Int64: user.ID, Valid: true},
		Recipient:    user.Email,
		Subject:      "Vítej v Base48!",
		TemplateName: "welcome.html",
		Data:         data,
	})
}

// SendNegativeBalance sends notification about negative membership balance
func (c *Client) SendNegativeBalance(ctx context.Context, user *db.User, balance float64) error {
	data := map[string]interface{}{
		"Name":       user.Realname.String,
		"Balance":    balance,
		"PaymentsID": user.PaymentsID.String,
		"PortalURL":  c.config.BaseURL,
	}

	// Generate QR payment code if possible
	if c.qrpayService != nil && c.qrpayService.IsConfigured() && user.PaymentsID.Valid && user.PaymentsID.String != "" {
		qrCode, err := c.qrpayService.GeneratePaymentQR(qrpay.GenerateParams{
			Amount:         math.Abs(balance),
			VariableSymbol: user.PaymentsID.String,
			Message:        "CLENSKY PRISPEVEK BASE48",
			Size:           200,
		})
		if err == nil {
			data["PaymentQRCode"] = template.URL(qrCode)
		}
	}

	return c.SendTemplated(ctx, SendParams{
		UserID:       sql.NullInt64{Int64: user.ID, Valid: true},
		Recipient:    user.Email,
		Subject:      "Záporná bilance členského příspěvku",
		TemplateName: "negative_balance.html",
		Data:         data,
	})
}

// SendDebtWarning sends warning about significant debt (>2x monthly fee)
func (c *Client) SendDebtWarning(ctx context.Context, user *db.User, balance float64, monthlyFee float64) error {
	data := map[string]interface{}{
		"Name":       user.Realname.String,
		"Balance":    balance,
		"MonthlyFee": monthlyFee,
		"PaymentsID": user.PaymentsID.String,
		"PortalURL":  c.config.BaseURL,
	}

	// Generate QR payment code if possible
	if c.qrpayService != nil && c.qrpayService.IsConfigured() && user.PaymentsID.Valid && user.PaymentsID.String != "" {
		qrCode, err := c.qrpayService.GeneratePaymentQR(qrpay.GenerateParams{
			Amount:         math.Abs(balance),
			VariableSymbol: user.PaymentsID.String,
			Message:        "CLENSKY PRISPEVEK BASE48",
			Size:           200,
		})
		if err == nil {
			data["PaymentQRCode"] = template.URL(qrCode)
		}
	}

	return c.SendTemplated(ctx, SendParams{
		UserID:       sql.NullInt64{Int64: user.ID, Valid: true},
		Recipient:    user.Email,
		Subject:      "⚠️ Upozornění na dluh za členství",
		TemplateName: "debt_warning.html",
		Data:         data,
	})
}

// SendMembershipSuspended sends notification about membership suspension
func (c *Client) SendMembershipSuspended(ctx context.Context, user *db.User, reason string) error {
	data := map[string]interface{}{
		"Name":      user.Realname.String,
		"Reason":    reason,
		"PortalURL": c.config.BaseURL,
	}

	return c.SendTemplated(ctx, SendParams{
		UserID:       sql.NullInt64{Int64: user.ID, Valid: true},
		Recipient:    user.Email,
		Subject:      "Pozastavení členství v Base48",
		TemplateName: "membership_suspended.html",
		Data:         data,
	})
}
