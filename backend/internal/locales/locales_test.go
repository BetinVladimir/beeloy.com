package locales

import "testing"

func TestLocales(t *testing.T) {
	if len(Codes()) != 26 {
		t.Fatalf("want 26 locales, got %d", len(Codes()))
	}
	for _, c := range []string{"bg", "en", "ru", "uk", "ga", "mt"} {
		if !Known(c) {
			t.Errorf("%s must be known", c)
		}
	}
	if Known("tr") || Known("zh") {
		t.Error("non-EU languages must not be present")
	}
	if Default() != "bg" || Fallback() != "en" || Source() != "en" {
		t.Error("bad default/fallback/source")
	}
	for _, l := range All() {
		if l.EnglishName == "" || l.Dir == "" {
			t.Errorf("locale %s incomplete", l.Code)
		}
	}
}
