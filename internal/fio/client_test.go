package fio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// statement is a trimmed FIO /periods/ response in the documented format.
const statement = `{"accountStatement":{
  "info":{"accountId":"2003436319","bankId":"2010","currency":"CZK",
          "iban":"CZ6620100000002003436319","bic":"FIOBCZPPXXX",
          "openingBalance":42514.0,"closingBalance":43514.0,
          "dateStart":"2026-07-11+0200","dateEnd":"2026-10-04+0200",
          "yearList":null,"idList":null,"idFrom":26962199069,"idTo":26962199069,"idLastDownload":null},
  "transactionList":{"transaction":[
    {"column22":{"value":26962199069,"name":"ID pohybu","id":22},
     "column0":{"value":"2026-10-01+0200","name":"Datum","id":0},
     "column1":{"value":1000.0,"name":"Objem","id":1},
     "column14":{"value":"CZK","name":"Měna","id":14},
     "column2":{"value":"123456789","name":"Protiúčet","id":2},
     "column10":{"value":"NOVAK JAN","name":"Název protiúčtu","id":10},
     "column3":{"value":"0800","name":"Kód banky","id":3},
     "column5":{"value":"709","name":"VS","id":5}}
  ]}}}`

func TestFetchReturnsTheAccountTheTokenReads(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(statement))
	}))
	defer srv.Close()

	c := NewClient("secret-token")
	c.baseURL = srv.URL

	res, err := c.FetchTransactionsByPeriod(context.Background(), "2026-07-11", "2026-10-04")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(path, "/periods/secret-token/2026-07-11/2026-10-04/") {
		t.Errorf("requested %q", path)
	}

	want := AccountInfo{AccountID: "2003436319", BankID: "2010", IBAN: "CZ6620100000002003436319", BIC: "FIOBCZPPXXX"}
	if res.Account != want {
		t.Errorf("Account = %+v, want %+v", res.Account, want)
	}
	if res.ClosingBalance != 43514 {
		t.Errorf("ClosingBalance = %v", res.ClosingBalance)
	}
	if len(res.Transactions) != 1 || res.Transactions[0].VariableSymbol != "709" || res.Transactions[0].Amount != 1000 {
		t.Errorf("Transactions = %+v", res.Transactions)
	}
}
