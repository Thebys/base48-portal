// Package qrpay implements Czech QR payment code generation (SPAYD format).
// See: https://qr-platba.cz/pro-vyvojare/specifikace-formatu/
package qrpay

import (
	"fmt"
	"math"
	"strings"
)

// PaymentParams holds the parameters for generating a SPAYD payment string.
type PaymentParams struct {
	// IBAN is the recipient's account number in IBAN format (required).
	IBAN string
	// BIC is the bank's SWIFT/BIC code (optional).
	BIC string
	// Amount is the payment amount. Zero means no amount specified.
	Amount float64
	// Currency is the ISO 4217 currency code. Defaults to "CZK".
	Currency string
	// VariableSymbol is the Czech-specific variable symbol (X-VS), max 10 digits.
	VariableSymbol string
	// SpecificSymbol is the Czech-specific specific symbol (X-SS), max 10 digits.
	SpecificSymbol string
	// ConstantSymbol is the Czech-specific constant symbol (X-KS), max 10 digits.
	ConstantSymbol string
	// Message is the payment message for recipient (MSG), max 60 chars.
	Message string
	// RecipientName is the recipient's name (RN), max 35 chars.
	RecipientName string
	// DueDate is the payment due date in YYYYMMDD format (DT).
	DueDate string
}

// GenerateSPAYD creates a SPAYD (Short Payment Descriptor) string from payment parameters.
// The string follows the Czech QR payment standard (SPD version 1.0).
//
// Example output:
//
//	SPD*1.0*ACC:CZ6508000000192000145399+GIBACZPX*AM:450.00*CC:CZK*X-VS:1234567890*MSG:CLENSKY PRISPEVEK*
func GenerateSPAYD(p PaymentParams) string {
	var parts []string

	// Header with version
	parts = append(parts, "SPD*1.0")

	// Account (required) - IBAN with optional BIC
	acc := p.IBAN
	if p.BIC != "" {
		acc += "+" + p.BIC
	}
	parts = append(parts, "ACC:"+acc)

	// Amount (optional). NaN is not > 0; +Inf is, and would print "+Inf".
	if p.Amount > 0 && !math.IsInf(p.Amount, 1) {
		parts = append(parts, fmt.Sprintf("AM:%.2f", p.Amount))
	}

	// Currency (optional, default CZK)
	currency := p.Currency
	if currency == "" {
		currency = "CZK"
	}
	parts = append(parts, "CC:"+currency)

	// Due date (optional)
	if p.DueDate != "" {
		parts = append(parts, "DT:"+p.DueDate)
	}

	// Message (optional, max 60 chars)
	if p.Message != "" {
		msg := sanitizeMessage(p.Message, 60)
		parts = append(parts, "MSG:"+msg)
	}

	// Recipient name (optional, max 35 chars)
	if p.RecipientName != "" {
		rn := sanitizeMessage(p.RecipientName, 35)
		parts = append(parts, "RN:"+rn)
	}

	// Czech-specific symbols (X- prefixed)
	if p.VariableSymbol != "" {
		parts = append(parts, "X-VS:"+sanitizeSymbol(p.VariableSymbol, 10))
	}
	if p.SpecificSymbol != "" {
		parts = append(parts, "X-SS:"+sanitizeSymbol(p.SpecificSymbol, 10))
	}
	if p.ConstantSymbol != "" {
		parts = append(parts, "X-KS:"+sanitizeSymbol(p.ConstantSymbol, 10))
	}

	// Join with asterisk separator and add trailing asterisk
	return strings.Join(parts, "*") + "*"
}

// sanitizeMessage prepares a message string for SPAYD format:
// - Converts to uppercase (for alphanumeric QR efficiency)
// - Removes/encodes asterisks (field delimiter)
// - Truncates to maxLen
func sanitizeMessage(s string, maxLen int) string {
	// Convert to uppercase for efficient alphanumeric QR encoding
	s = strings.ToUpper(s)

	// Remove diacritics for better compatibility
	s = removeDiacritics(s)

	// Encode asterisks (not allowed in values)
	s = strings.ReplaceAll(s, "*", "%2A")

	// Truncate to max length
	if len(s) > maxLen {
		s = s[:maxLen]
	}

	return s
}

// sanitizeSymbol ensures a symbol contains only digits and is within length limit.
func sanitizeSymbol(s string, maxLen int) string {
	// Keep only digits
	var result strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			result.WriteRune(r)
		}
	}
	s = result.String()

	// Truncate to max length
	if len(s) > maxLen {
		s = s[:maxLen]
	}

	return s
}

// removeDiacritics converts Czech diacritics to ASCII equivalents.
func removeDiacritics(s string) string {
	replacer := strings.NewReplacer(
		"Á", "A", "á", "a",
		"Č", "C", "č", "c",
		"Ď", "D", "ď", "d",
		"É", "E", "é", "e", "Ě", "E", "ě", "e",
		"Í", "I", "í", "i",
		"Ň", "N", "ň", "n",
		"Ó", "O", "ó", "o",
		"Ř", "R", "ř", "r",
		"Š", "S", "š", "s",
		"Ť", "T", "ť", "t",
		"Ú", "U", "ú", "u", "Ů", "U", "ů", "u",
		"Ý", "Y", "ý", "y",
		"Ž", "Z", "ž", "z",
	)
	return replacer.Replace(s)
}

// ParseSPAYD parses a SPAYD string back into PaymentParams.
// This is useful for validation and testing.
func ParseSPAYD(spayd string) (*PaymentParams, error) {
	if !strings.HasPrefix(spayd, "SPD*1.0*") {
		return nil, fmt.Errorf("invalid SPAYD header")
	}

	params := &PaymentParams{}

	// Remove header and split by asterisk
	content := strings.TrimPrefix(spayd, "SPD*1.0*")
	content = strings.TrimSuffix(content, "*")
	parts := strings.Split(content, "*")

	for _, part := range parts {
		if part == "" {
			continue
		}
		idx := strings.Index(part, ":")
		if idx == -1 {
			continue
		}
		key := part[:idx]
		value := part[idx+1:]

		// URL decode value for fields that may contain encoded chars (but not ACC which uses + as delimiter)
		decoded := strings.ReplaceAll(value, "%2A", "*") // Basic percent decoding for asterisk

		switch key {
		case "ACC":
			// Parse IBAN+BIC (+ is literal delimiter, not URL encoded space)
			if plus := strings.Index(value, "+"); plus != -1 {
				params.IBAN = value[:plus]
				params.BIC = value[plus+1:]
			} else {
				params.IBAN = value
			}
		case "AM":
			fmt.Sscanf(decoded, "%f", &params.Amount)
		case "CC":
			params.Currency = decoded
		case "DT":
			params.DueDate = decoded
		case "MSG":
			params.Message = decoded
		case "RN":
			params.RecipientName = decoded
		case "X-VS":
			params.VariableSymbol = decoded
		case "X-SS":
			params.SpecificSymbol = decoded
		case "X-KS":
			params.ConstantSymbol = decoded
		}
	}

	return params, nil
}
