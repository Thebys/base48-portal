package qrpay

import (
	"fmt"
)

// Service provides high-level methods for generating payment QR codes.
type Service struct {
	bankIBAN string
	bankBIC  string
}

// NewService creates a new QR payment service with the organization's bank details.
func NewService(iban, bic string) *Service {
	return &Service{
		bankIBAN: iban,
		bankBIC:  bic,
	}
}

// GenerateParams holds parameters for generating a payment QR code.
type GenerateParams struct {
	// Amount is the payment amount in CZK.
	Amount float64
	// VariableSymbol is the variable symbol for payment identification.
	VariableSymbol string
	// Message is an optional message for the payment.
	Message string
	// Size is the QR code size in pixels. Defaults to 200.
	Size int
}

// GeneratePaymentQR generates a QR code for a payment to the organization's account.
// Returns a Base64 data URL ready to use in an HTML img tag.
func (s *Service) GeneratePaymentQR(params GenerateParams) (string, error) {
	if s.bankIBAN == "" {
		return "", fmt.Errorf("bank IBAN not configured")
	}

	spayd := GenerateSPAYD(PaymentParams{
		IBAN:           s.bankIBAN,
		BIC:            s.bankBIC,
		Amount:         params.Amount,
		Currency:       "CZK",
		VariableSymbol: params.VariableSymbol,
		Message:        params.Message,
	})

	size := params.Size
	if size <= 0 {
		size = DefaultQRSize
	}

	return GenerateQRBase64(spayd, size)
}

// GeneratePaymentPNG generates a QR code as raw PNG bytes.
// Useful for serving QR codes via HTTP endpoints.
func (s *Service) GeneratePaymentPNG(params GenerateParams) ([]byte, error) {
	if s.bankIBAN == "" {
		return nil, fmt.Errorf("bank IBAN not configured")
	}

	spayd := GenerateSPAYD(PaymentParams{
		IBAN:           s.bankIBAN,
		BIC:            s.bankBIC,
		Amount:         params.Amount,
		Currency:       "CZK",
		VariableSymbol: params.VariableSymbol,
		Message:        params.Message,
	})

	size := params.Size
	if size <= 0 {
		size = DefaultQRSize
	}

	return GenerateQRPNG(spayd, size)
}

// GenerateSPAYDString generates just the SPAYD string without QR code.
// Useful for debugging or alternative display methods.
func (s *Service) GenerateSPAYDString(params GenerateParams) string {
	return GenerateSPAYD(PaymentParams{
		IBAN:           s.bankIBAN,
		BIC:            s.bankBIC,
		Amount:         params.Amount,
		Currency:       "CZK",
		VariableSymbol: params.VariableSymbol,
		Message:        params.Message,
	})
}

// BankIBAN returns the configured bank IBAN.
func (s *Service) BankIBAN() string {
	return s.bankIBAN
}

// BankBIC returns the configured bank BIC/SWIFT code.
func (s *Service) BankBIC() string {
	return s.bankBIC
}

// IsConfigured returns true if the service has valid bank configuration.
func (s *Service) IsConfigured() bool {
	return s.bankIBAN != ""
}
