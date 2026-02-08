package fio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client represents a FIO Bank API client
type Client struct {
	token      string
	httpClient *http.Client
	baseURL    string
}

// NewClient creates a new FIO API client
func NewClient(token string) *Client {
	return &Client{
		token: token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		baseURL: "https://fioapi.fio.cz/v1/rest",
	}
}

// Transaction represents a single FIO bank transaction
type Transaction struct {
	ID                int64       `json:"column22"` // ID transakce
	Date              string      `json:"column0"`  // Datum (YYYY-MM-DD format)
	Amount            float64     `json:"column1"`  // Částka
	Currency          string      `json:"column14"` // Měna
	AccountNumber     string      `json:"column2"`  // Protiúčet
	AccountName       string      `json:"column10"` // Název protiúčtu
	BankCode          string      `json:"column3"`  // Kód banky
	BankName          string      `json:"column12"` // Název banky
	VariableSymbol    string      `json:"column5"`  // Variabilní symbol
	SpecificSymbol    string      `json:"column6"`  // Specifický symbol
	Message           string      `json:"column16"` // Zpráva pro příjemce
	Comment           string      `json:"column25"` // Komentář
	TransactionType   string      `json:"column8"`  // Typ transakce
	Identification    string      `json:"column7"`  // Identifikace transakce
}

// FetchResult contains transactions and account info from a FIO API response.
type FetchResult struct {
	Transactions   []Transaction
	ClosingBalance float64
}

// TransactionList represents the response from FIO API
type TransactionList struct {
	AccountStatement struct {
		Info struct {
			AccountID   string `json:"accountId"`
			BankID      string `json:"bankId"`
			Currency    string `json:"currency"`
			IBAN        string `json:"iban"`
			BIC         string `json:"bic"`
			OpeningBalance float64 `json:"openingBalance"`
			ClosingBalance float64 `json:"closingBalance"`
			DateStart   string `json:"dateStart"`
			DateEnd     string `json:"dateEnd"`
			YearList    int    `json:"yearList"`
			IDList      int    `json:"idList"`
			IDFrom      int64  `json:"idFrom"`
			IDTo        int64  `json:"idTo"`
			IDLastDownload int64 `json:"idLastDownload"`
		} `json:"info"`
		TransactionList struct {
			Transactions []map[string]interface{} `json:"transaction"`
		} `json:"transactionList"`
	} `json:"accountStatement"`
}

// FetchTransactionsByPeriod fetches transactions for a specific date range
// dateFrom and dateTo should be in format "YYYY-MM-DD"
func (c *Client) FetchTransactionsByPeriod(ctx context.Context, dateFrom, dateTo string) (*FetchResult, error) {
	url := fmt.Sprintf("%s/periods/%s/%s/%s/transactions.json",
		c.baseURL, c.token, dateFrom, dateTo)

	return c.fetchTransactions(ctx, url)
}

// FetchTransactionsSinceLastDownload fetches all new transactions since last download
func (c *Client) FetchTransactionsSinceLastDownload(ctx context.Context) (*FetchResult, error) {
	url := fmt.Sprintf("%s/last/%s/transactions.json", c.baseURL, c.token)
	return c.fetchTransactions(ctx, url)
}

// FetchTransactionsByID fetches transactions from a specific year and ID
func (c *Client) FetchTransactionsByID(ctx context.Context, year int, idFrom int64) (*FetchResult, error) {
	url := fmt.Sprintf("%s/by-id/%s/%d/%d/transactions.json",
		c.baseURL, c.token, year, idFrom)
	return c.fetchTransactions(ctx, url)
}

// SetLastDownloadDate sets a checkpoint for future "since last download" calls
// date should be in format "YYYY-MM-DD"
func (c *Client) SetLastDownloadDate(ctx context.Context, date string) error {
	url := fmt.Sprintf("%s/set-last-date/%s/%s/", c.baseURL, c.token, date)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	return nil
}

// fetchTransactions is a helper that performs the actual HTTP request and parsing
func (c *Client) fetchTransactions(ctx context.Context, url string) (*FetchResult, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var result TransactionList
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Parse transactions from the raw map structure
	transactions := make([]Transaction, 0, len(result.AccountStatement.TransactionList.Transactions))

	for _, rawTx := range result.AccountStatement.TransactionList.Transactions {
		tx := Transaction{}

		// Extract fields from the nested structure
		if v, ok := rawTx["column22"].(map[string]interface{}); ok {
			if id, ok := v["value"].(float64); ok {
				tx.ID = int64(id)
			}
		}
		if v, ok := rawTx["column0"].(map[string]interface{}); ok {
			if date, ok := v["value"].(string); ok {
				tx.Date = date
			}
		}
		if v, ok := rawTx["column1"].(map[string]interface{}); ok {
			if amount, ok := v["value"].(float64); ok {
				tx.Amount = amount
			}
		}
		if v, ok := rawTx["column14"].(map[string]interface{}); ok {
			if curr, ok := v["value"].(string); ok {
				tx.Currency = curr
			}
		}
		if v, ok := rawTx["column2"].(map[string]interface{}); ok {
			if acc, ok := v["value"].(string); ok {
				tx.AccountNumber = acc
			}
		}
		if v, ok := rawTx["column10"].(map[string]interface{}); ok {
			if name, ok := v["value"].(string); ok {
				tx.AccountName = name
			}
		}
		if v, ok := rawTx["column3"].(map[string]interface{}); ok {
			if code, ok := v["value"].(string); ok {
				tx.BankCode = code
			}
		}
		if v, ok := rawTx["column12"].(map[string]interface{}); ok {
			if bank, ok := v["value"].(string); ok {
				tx.BankName = bank
			}
		}
		if v, ok := rawTx["column5"].(map[string]interface{}); ok {
			if vs, ok := v["value"].(string); ok {
				tx.VariableSymbol = vs
			} else if vs, ok := v["value"].(float64); ok {
				tx.VariableSymbol = fmt.Sprintf("%.0f", vs)
			}
		}
		if v, ok := rawTx["column6"].(map[string]interface{}); ok {
			if ss, ok := v["value"].(string); ok {
				tx.SpecificSymbol = ss
			}
		}
		if v, ok := rawTx["column16"].(map[string]interface{}); ok {
			if msg, ok := v["value"].(string); ok {
				tx.Message = msg
			}
		}
		if v, ok := rawTx["column25"].(map[string]interface{}); ok {
			if cmt, ok := v["value"].(string); ok {
				tx.Comment = cmt
			}
		}
		if v, ok := rawTx["column8"].(map[string]interface{}); ok {
			if typ, ok := v["value"].(string); ok {
				tx.TransactionType = typ
			}
		}
		if v, ok := rawTx["column7"].(map[string]interface{}); ok {
			if id, ok := v["value"].(string); ok {
				tx.Identification = id
			}
		}

		transactions = append(transactions, tx)
	}

	return &FetchResult{
		Transactions:   transactions,
		ClosingBalance: result.AccountStatement.Info.ClosingBalance,
	}, nil
}

// FormatDate converts time.Time to FIO API date format (YYYY-MM-DD)
func FormatDate(t time.Time) string {
	return t.Format("2006-01-02")
}

// ParseDate parses FIO API date format (YYYY-MM-DD+0100 or YYYY-MM-DD) to time.Time
func ParseDate(dateStr string) (time.Time, error) {
	// Try parsing with timezone first
	t, err := time.Parse("2006-01-02-0700", dateStr)
	if err != nil {
		// Fallback to simple date format
		t, err = time.Parse("2006-01-02", dateStr)
	}
	return t, err
}
