package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/email"
)

type templateBlocksResponse struct {
	Blocks []struct {
		Name       string `json:"name"`
		Current    string `json:"current"`
		IsModified bool   `json:"is_modified"`
	} `json:"blocks"`
}

func getTemplateBlocks(t *testing.T, h *Handler, template, lang string) templateBlocksResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/email/templates?template="+template+"&lang="+lang, nil)
	rec := httptest.NewRecorder()
	h.AdminGetTemplateContentHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp templateBlocksResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func countOverrides(t *testing.T, h *Handler, template string) int {
	t.Helper()
	rows, err := h.queries.ListEmailTemplateContentByTemplateLang(context.Background(), db.ListEmailTemplateContentByTemplateLangParams{TemplateName: template, Lang: "cs"})
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

// Czech defaults get non-breaking spaces at render time; the editor used to
// compare those against the plain defaults and flag every block as edited.
func TestTemplateEditorShowsUntouchedCzechBlocksAsDefault(t *testing.T) {
	h, _ := testHandlerWithEmail(t)
	for _, b := range getTemplateBlocks(t, h, "debt_warning", "cs").Blocks {
		if b.IsModified {
			t.Errorf("block %s flagged as modified on a fresh database", b.Name)
		}
	}
}

// Saving the editor as-is must not freeze the defaults into the database, and
// overrides left by older saves (with non-breaking spaces) must be cleared.
func TestTemplateSaveStoresOnlyRealEdits(t *testing.T) {
	h, database := testHandlerWithEmail(t)
	ctx := context.Background()

	// A legacy override: the default text, frozen with typography applied.
	def := email.GetDefaultContentBlocks("cs")["debt_warning"]["footer"]
	legacy := "Potřebuješ pomoc? " + def[len("Potřebuješ pomoc? "):]
	if _, err := database.Exec(
		`INSERT INTO email_template_content (template_name, block_name, lang, content) VALUES ('debt_warning', 'footer', 'cs', ?)`, legacy,
	); err != nil {
		t.Fatal(err)
	}

	blocks := map[string]string{}
	for _, b := range getTemplateBlocks(t, h, "debt_warning", "cs").Blocks {
		if b.IsModified {
			t.Errorf("legacy override of %s equal to the default shows as modified", b.Name)
		}
		blocks[b.Name] = b.Current
	}
	blocks["heading"] = "Vlastní nadpis"

	if err := h.saveTemplateBlocks(ctx, "debt_warning", "cs", blocks, "admin@base48.cz"); err != nil {
		t.Fatal(err)
	}
	if n := countOverrides(t, h, "debt_warning"); n != 1 {
		t.Errorf("%d overrides stored, want only the edited heading", n)
	}

	if err := h.saveTemplateBlocks(ctx, "debt_warning", "cs", map[string]string{"nonsense": "x"}, ""); err == nil {
		t.Error("an unknown block name must be rejected")
	}
}
