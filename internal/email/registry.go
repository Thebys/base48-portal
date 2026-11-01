package email

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// TemplateSpec describes one email for the admin: what it is called, when it
// goes out, and the text blocks an admin may edit, in the order they appear.
// The default texts are in defaultContentBlocks (cs) and
// defaultContentBlocksEN; tests keep the three in step with the HTML files.
type TemplateSpec struct {
	Key    string // content-block key; the file is Key + ".html"
	Label  string
	When   string
	Blocks []BlockSpec
	// Placeholders the blocks may use, filled in when the email is built.
	Placeholders []PlaceholderSpec
}

// BlockSpec is one editable text block.
type BlockSpec struct {
	Name  string
	Label string
}

// PlaceholderSpec is a {name} a block may contain.
type PlaceholderSpec struct {
	Name    string
	Meaning string
}

// File is the template's file and outbox template_name.
func (t TemplateSpec) File() string { return t.Key + ".html" }

// The blocks most emails share, in their usual order.
var (
	blockSubject   = BlockSpec{"subject", "Předmět"}
	blockPreheader = BlockSpec{"preheader", "Náhled v seznamu e-mailů"}
	blockHeading   = BlockSpec{"heading", "Nadpis"}
	blockIntro     = BlockSpec{"intro_text", "Úvod"}
	blockFooter    = BlockSpec{"footer", "Patička"}
	blockSignOff   = BlockSpec{"sign_off", "Podpis"}
)

// Templates lists every email, in the order the admin sees them.
var Templates = []TemplateSpec{
	{
		Key:   "registration",
		Label: "Potvrzení registrace",
		When:  "Den po prvním přihlášení, dokud člen čeká na schválení.",
		Blocks: []BlockSpec{blockSubject, blockPreheader, blockHeading, blockIntro,
			{"next_steps_text", "Co bude dál"}, {"wiki_text", "Odkaz na wiki"}, blockFooter, blockSignOff},
	},
	{
		Key:   "welcome",
		Label: "Uvítání nového člena",
		When:  "Den po přijetí za člena, pokud mezitím nezměnil stav.",
		Blocks: []BlockSpec{blockSubject, blockPreheader, blockHeading, blockIntro,
			{"resources_text", "Kde začít"}, {"membership_text", "Členství"}, blockFooter, blockSignOff},
	},
	{
		Key:   TierPrepaidEnding,
		Label: "Před koncem předplatného",
		When: "Po předpisu 1. v měsíci, když člen nemá zaplacený další měsíc (i když mu chybí jen část). " +
			"Odchází 4. v 10:00, jen když je zapnuté. Kdo mezitím zaplatí, nedostane ho.",
		Blocks: []BlockSpec{blockSubject, {"subject_short", "Předmět, když chybí část"},
			blockPreheader, {"preheader_short", "Náhled, když chybí část"},
			{"heading", "Nadpis"}, {"heading_short", "Nadpis, když chybí část"},
			blockIntro, {"intro_text_short", "Úvod, když chybí část"},
			{"prepay_text", "Prosba o platbu dopředu"}, blockFooter, blockSignOff},
		Placeholders: []PlaceholderSpec{
			{"zaplaceno_do", "měsíc, do jehož konce má člen zaplaceno: „listopadu 2026“"},
			{"mesic", "měsíc, na který chybí část: „listopad“"},
			{"chybi", "kolik na něj chybí: „300 Kč“"},
			{"mesice", "na kolik měsíců dopředu doporučujeme platit: „3 měsíce“"},
			{"castka", "doporučená platba: „3 000 Kč“"},
			{"pokryje_do", "do konce kterého měsíce ji pokryje: „února 2027“"},
			{"prispevek", "měsíční příspěvek člena: „1 000 Kč“"},
		},
	},
	{
		Key:   TierNegativeBalance,
		Label: "Záporná bilance",
		When:  "Po předpisu 1. v měsíci, když člen dluží aspoň jeden měsíční příspěvek. Odchází 4. v 10:00.",
		Blocks: []BlockSpec{blockSubject, blockPreheader, blockHeading, blockIntro,
			{"action_text", "Výzva k platbě"}, blockFooter, blockSignOff},
	},
	{
		Key:   TierDebtWarning,
		Label: "Upozornění na dluh",
		When:  "Po předpisu 1. v měsíci, když člen dluží aspoň dva měsíční příspěvky. Odchází 4. v 10:00.",
		Blocks: []BlockSpec{blockSubject, blockPreheader, blockHeading,
			{"warning_text", "Upozornění"}, {"consequences_text", "Co hrozí"}, {"steps_text", "Co udělat"},
			blockFooter, blockSignOff},
	},
	{
		Key:   "membership_suspended",
		Label: "Pozastavení členství",
		When:  "Den poté, co admin členství pozastaví, pokud mezitím nezměnil stav.",
		Blocks: []BlockSpec{blockSubject, blockPreheader, blockHeading,
			{"intro_text", "Oznámení"}, {"consequences_text", "Co to znamená"}, {"recovery_text", "Jak to napravit"},
			blockFooter, blockSignOff},
	},
	{
		Key:   TemplateBarDebt,
		Label: "Dluh na baru",
		When:  "Denně od 10:00, když dluh na barovém kiosku přesáhne hranici z nastavení. Jen když je zapnutý.",
		Blocks: []BlockSpec{blockSubject, blockPreheader, blockHeading, blockIntro,
			{"action_text", "Jak dluh vyrovnat"}, blockFooter, blockSignOff},
	},
}

// TemplateByKey finds a template's spec.
func TemplateByKey(key string) (TemplateSpec, bool) {
	for _, t := range Templates {
		if t.Key == key {
			return t, true
		}
	}
	return TemplateSpec{}, false
}

// CheckPlaceholders reports any {name} in text that the template does not
// fill in. Templates without placeholders take any text.
func (t TemplateSpec) CheckPlaceholders(text string) error {
	if len(t.Placeholders) == 0 {
		return nil
	}
	var unknown []string
	for _, m := range placeholder.FindAllStringSubmatch(text, -1) {
		known := slices.ContainsFunc(t.Placeholders, func(p PlaceholderSpec) bool { return p.Name == m[1] })
		if !known && !slices.Contains(unknown, m[0]) {
			unknown = append(unknown, m[0])
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	names := make([]string, len(t.Placeholders))
	for i, p := range t.Placeholders {
		names[i] = "{" + p.Name + "}"
	}
	sort.Strings(names)
	return fmt.Errorf("neznámé %s, šablona zná %s", strings.Join(unknown, ", "), strings.Join(names, ", "))
}

// TemplateLabel is the Czech name of the email an outbox template_name
// stands for, or the file name when it is none of the registry's.
func TemplateLabel(file string) string {
	for _, t := range Templates {
		if t.File() == file {
			return t.Label
		}
	}
	return file
}

// Scenario is a situation the admin can preview an email in.
type Scenario struct {
	Key   string
	Label string
	// Balance in monthly fees, for the membership reminders.
	Balance float64
}

// Scenarios lists what the admin can preview each email in; the first is the
// default. The membership reminders get standings their real rule would pick
// them for, so a preview cannot show a case the monthly run never sends.
func Scenarios(template string) []Scenario {
	switch template {
	case TierPrepaidEnding:
		return []Scenario{
			{"paid", "Zaplaceno do konce měsíce", 0},
			{"credit", "Zaplaceno a půl měsíce navíc", 0.5},
			{"short", "Na tento měsíc chybí část", -0.3},
		}
	case TierNegativeBalance:
		return []Scenario{
			{"one", "Dluží jeden příspěvek", -1},
			{"more", "Dluží jeden a půl příspěvku", -1.5},
		}
	case TierDebtWarning:
		return []Scenario{
			{"two", "Dluží dva příspěvky", -2},
			{"more", "Dluží čtyři a půl příspěvku", -4.5},
		}
	}
	return []Scenario{{"sample", "Ukázka", 0}}
}
