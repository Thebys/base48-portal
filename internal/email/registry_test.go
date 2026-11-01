package email

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

var (
	contentKey = regexp.MustCompile(`index \.Content "([a-z_]+)"`)
	labelKey   = regexp.MustCompile(`index \.Labels "([a-z_]+)"`)
)

func keysIn(t *testing.T, re *regexp.Regexp, files ...string) []string {
	t.Helper()
	var keys []string
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join("..", "..", "web", "templates", "email", f))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			if !slices.Contains(keys, m[1]) {
				keys = append(keys, m[1])
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// The registry, the default texts in both languages and the HTML files must
// name the same blocks: a block the HTML uses but nobody wrote renders as
// nothing, and one written but not used is an editor field that does nothing.
func TestRegistryMatchesTemplatesAndDefaults(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "web", "templates", "email", "*.html"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, spec := range Templates {
		seen[spec.File()] = true
		var blocks []string
		for _, b := range spec.Blocks {
			if slices.Contains(blocks, b.Name) {
				t.Errorf("%s: block %s listed twice", spec.Key, b.Name)
			}
			blocks = append(blocks, b.Name)
		}
		sort.Strings(blocks)

		// Variants for a member a little short replace their base block
		// before rendering, so the HTML names only the base.
		var used []string
		for _, b := range blocks {
			if base, ok := cutShort(b); ok && slices.Contains(blocks, base) {
				continue
			}
			used = append(used, b)
		}
		if html := keysIn(t, contentKey, spec.File(), layoutFile); !slices.Equal(html, used) {
			t.Errorf("%s: the HTML and layout use blocks %v, the registry lists %v", spec.Key, html, used)
		}

		for lang, defaults := range allDefaultContentBlocks {
			var have []string
			for name := range defaults[spec.Key] {
				have = append(have, name)
			}
			sort.Strings(have)
			if !slices.Equal(have, blocks) {
				t.Errorf("%s/%s: default texts for %v, registry lists %v", spec.Key, lang, have, blocks)
			}
			for name, text := range defaults[spec.Key] {
				if text == "" {
					t.Errorf("%s/%s: block %s has no default text", spec.Key, lang, name)
				}
				if err := spec.CheckPlaceholders(text); err != nil {
					t.Errorf("%s/%s/%s: %v", spec.Key, lang, name, err)
				}
			}
		}

		for _, label := range keysIn(t, labelKey, spec.File(), layoutFile) {
			for _, lang := range []string{"cs", "en"} {
				if EmailLabels(lang)[label] == "" {
					t.Errorf("%s uses label %s, which %s lacks", spec.File(), label, lang)
				}
			}
		}
	}
	for _, f := range files {
		if name := filepath.Base(f); name != layoutFile && !seen[name] {
			t.Errorf("%s is not in the registry", name)
		}
	}
	for lang, defaults := range allDefaultContentBlocks {
		for key := range defaults {
			if _, ok := TemplateByKey(key); !ok {
				t.Errorf("default texts (%s) for %s, which the registry does not know", lang, key)
			}
		}
	}
}

func cutShort(block string) (string, bool) { return strings.CutSuffix(block, "_short") }

func TestCheckPlaceholders(t *testing.T) {
	prepaid, _ := TemplateByKey(TierPrepaidEnding)
	if err := prepaid.CheckPlaceholders("Zaplaceno do konce {zaplaceno_do}, chybí {chybi}"); err != nil {
		t.Error(err)
	}
	if err := prepaid.CheckPlaceholders("do konce {zaplacenodo}"); err == nil {
		t.Error("a misspelt placeholder was accepted")
	}
	welcome, _ := TemplateByKey("welcome")
	if err := welcome.CheckPlaceholders("{cokoli}"); err != nil {
		t.Errorf("a template without placeholders refused text: %v", err)
	}
}
