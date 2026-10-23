package qrpay

import (
	"math"
	"strings"
	"testing"
)

func TestGenerateSPAYD(t *testing.T) {
	tests := []struct {
		name     string
		params   PaymentParams
		contains []string
	}{
		{
			name: "basic payment",
			params: PaymentParams{
				IBAN:           "CZ6508000000192000145399",
				BIC:            "GIBACZPX",
				Amount:         450.00,
				VariableSymbol: "1234567890",
				Message:        "Clensky prispevek",
			},
			contains: []string{
				"SPD*1.0*",
				"ACC:CZ6508000000192000145399+GIBACZPX",
				"AM:450.00",
				"CC:CZK",
				"X-VS:1234567890",
				"MSG:CLENSKY PRISPEVEK",
			},
		},
		{
			name: "without BIC",
			params: PaymentParams{
				IBAN:           "CZ6508000000192000145399",
				Amount:         100.00,
				VariableSymbol: "123",
			},
			contains: []string{
				"ACC:CZ6508000000192000145399*",
				"AM:100.00",
			},
		},
		{
			name: "diacritics removal",
			params: PaymentParams{
				IBAN:    "CZ6508000000192000145399",
				Message: "Příspěvek členství",
			},
			contains: []string{
				"MSG:PRISPEVEK CLENSTVI",
			},
		},
		{
			name: "all symbols",
			params: PaymentParams{
				IBAN:           "CZ6508000000192000145399",
				VariableSymbol: "123456",
				SpecificSymbol: "789",
				ConstantSymbol: "0308",
			},
			contains: []string{
				"X-VS:123456",
				"X-SS:789",
				"X-KS:0308",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GenerateSPAYD(tt.params)

			for _, want := range tt.contains {
				if !strings.Contains(result, want) {
					t.Errorf("GenerateSPAYD() = %q, want to contain %q", result, want)
				}
			}

			// Must end with asterisk
			if !strings.HasSuffix(result, "*") {
				t.Errorf("GenerateSPAYD() = %q, should end with *", result)
			}
		})
	}
}

// A non-finite amount is no amount: never "AM:NaN" or "AM:+Inf".
func TestGenerateSPAYDSkipsNonFiniteAmount(t *testing.T) {
	for _, amount := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		got := GenerateSPAYD(PaymentParams{IBAN: "CZ6508000000192000145399", Amount: amount})
		if strings.Contains(got, "AM:") {
			t.Errorf("amount %v: %s", amount, got)
		}
	}
}

func TestParseSPAYD(t *testing.T) {
	// Test roundtrip: generate then parse
	// Using Base48 bank details
	original := PaymentParams{
		IBAN:           "CZ4220100000002900086515",
		BIC:            "FIOBCZPP",
		Amount:         450.00,
		VariableSymbol: "1234567890",
		Message:        "TEST",
	}

	spayd := GenerateSPAYD(original)
	t.Logf("Generated SPAYD: %s", spayd)

	params, err := ParseSPAYD(spayd)
	if err != nil {
		t.Fatalf("ParseSPAYD() error = %v", err)
	}

	if params.IBAN != original.IBAN {
		t.Errorf("IBAN = %q, want %q", params.IBAN, original.IBAN)
	}
	if params.BIC != original.BIC {
		t.Errorf("BIC = %q, want %q", params.BIC, original.BIC)
	}
	if params.Amount != original.Amount {
		t.Errorf("Amount = %f, want %f", params.Amount, original.Amount)
	}
	if params.VariableSymbol != original.VariableSymbol {
		t.Errorf("VariableSymbol = %q, want %q", params.VariableSymbol, original.VariableSymbol)
	}
}

func TestSanitizeSymbol(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"1234567890", 10, "1234567890"},
		{"12345678901234", 10, "1234567890"},
		{"abc123def", 10, "123"},
		{"", 10, ""},
	}

	for _, tt := range tests {
		got := sanitizeSymbol(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("sanitizeSymbol(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}

func TestRemoveDiacritics(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Příspěvek", "Prispevek"},
		{"členství", "clenstvi"},
		{"ŽLUŤOUČKÝ KŮŇ", "ZLUTOUCKY KUN"},
		{"Normal text", "Normal text"},
	}

	for _, tt := range tests {
		got := removeDiacritics(tt.input)
		if got != tt.want {
			t.Errorf("removeDiacritics(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestGenerateQRBase64(t *testing.T) {
	spayd := "SPD*1.0*ACC:CZ6508000000192000145399*AM:100.00*CC:CZK*"

	result, err := GenerateQRBase64(spayd, 100)
	if err != nil {
		t.Fatalf("GenerateQRBase64() error = %v", err)
	}

	if !strings.HasPrefix(result, "data:image/png;base64,") {
		t.Errorf("GenerateQRBase64() should return data URL, got %q", result[:50])
	}
}
