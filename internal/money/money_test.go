package money

import (
	"strings"
	"testing"
)

func TestParseAmount(t *testing.T) {
	valid := []struct {
		in   string
		want float64
	}{
		{"1000", 1000},
		{"0", 0},
		{" 1500 ", 1500},
		{"1000.5", 1000.5},
		{"255000", 255000},
	}
	for _, tt := range valid {
		got, err := ParseAmount(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("ParseAmount(%q) = %v, %v; want %v", tt.in, got, err, tt.want)
		}
	}

	invalid := []string{
		// ParseFloat accepts all of these; the pentest stored "NaN" as a fee.
		"NaN", "nan", "Inf", "+Inf", "-Inf", "Infinity", "1e3", "1e308", "0x1p10",
		// fmt.Sscanf("%f") read these as 1000 and 1.
		"1000abc", "1 000",
		"", " ", "-1", "-0", "+1000", "1,5", "1_000", ".5", "5.",
		"255001",
		// Far too many digits: ParseFloat gives +Inf and ErrRange.
		strings.Repeat("9", 400),
	}
	for _, in := range invalid {
		if got, err := ParseAmount(in); err == nil {
			t.Errorf("ParseAmount(%q) = %v, want an error", in, got)
		}
	}
}

func TestFormatAmount(t *testing.T) {
	for in, want := range map[float64]string{1000: "1000", 0: "0", 1000.5: "1000.5", 255000: "255000"} {
		if got := FormatAmount(in); got != want {
			t.Errorf("FormatAmount(%v) = %q, want %q", in, got, want)
		}
	}
}
