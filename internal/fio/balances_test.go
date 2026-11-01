package fio

import (
	"reflect"
	"strings"
	"testing"
)

func TestDailyBalancesFollowTheStatement(t *testing.T) {
	res := &FetchResult{
		DateStart:      "2026-01-01+0100",
		DateEnd:        "2026-01-31+0100",
		OpeningBalance: 100,
		ClosingBalance: 1150.3,
		Transactions: []Transaction{
			{ID: 3, Date: "2026-01-20+0100", Amount: -0.1},
			{ID: 1, Date: "2026-01-05+0100", Amount: 1000},
			{ID: 2, Date: "2026-01-05+0100", Amount: 50.4},
		},
	}
	got, err := res.DailyBalances()
	if err != nil {
		t.Fatal(err)
	}
	want := []DayBalance{
		{"2025-12-31", 100},    // opening: the end of the day before
		{"2026-01-05", 1150.4}, // two payments on one day
		{"2026-01-20", 1150.3},
		{"2026-01-31", 1150.3}, // the statement's last day
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DailyBalances =\n%v\nwant\n%v", got, want)
	}
}

func TestDailyBalancesOfAQuietStatement(t *testing.T) {
	res := &FetchResult{DateStart: "2025-01-01+0100", DateEnd: "2025-12-31+0100"}
	got, err := res.DailyBalances()
	if err != nil {
		t.Fatal(err)
	}
	want := []DayBalance{{"2024-12-31", 0}, {"2025-12-31", 0}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DailyBalances = %v, want %v", got, want)
	}
}

func TestDailyBalancesRefuseAnIncompleteStatement(t *testing.T) {
	res := &FetchResult{
		DateStart: "2026-01-01+0100", DateEnd: "2026-01-31+0100",
		OpeningBalance: 0, ClosingBalance: 500,
		Transactions: []Transaction{{ID: 1, Date: "2026-01-05+0100", Amount: 400}},
	}
	if _, err := res.DailyBalances(); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("DailyBalances error = %v, want incomplete", err)
	}
}

func TestParseStatementReadsTheBalances(t *testing.T) {
	res, err := ParseStatement(strings.NewReader(statement))
	if err != nil {
		t.Fatal(err)
	}
	got, err := res.DailyBalances()
	if err != nil {
		t.Fatal(err)
	}
	want := []DayBalance{{"2026-07-10", 42514}, {"2026-10-01", 43514}, {"2026-10-04", 43514}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DailyBalances = %v, want %v", got, want)
	}
}
