package rates

import (
	"strings"
	"testing"
)

// findItem returns the RateItem for the given charge type, or zero value.
func findItem(items []RateItem, ct string) (RateItem, bool) {
	for _, it := range items {
		if it.ChargeType == ct {
			return it, true
		}
	}
	return RateItem{}, false
}

func TestExtractRates_PlainText(t *testing.T) {
	text := `
linehaul: $450
FSC: 18%
chassis: $25/day
`
	result := ExtractRates(text)

	cases := []struct {
		ct     string
		amount float64
	}{
		{"linehaul", 450},
		{"fuel", 18},
		{"chassis", 25},
	}
	for _, c := range cases {
		item, ok := findItem(result.Items, c.ct)
		if !ok {
			t.Errorf("expected %q to be extracted", c.ct)
			continue
		}
		if item.Amount != c.amount {
			t.Errorf("%q: got %v, want %v", c.ct, item.Amount, c.amount)
		}
	}
}

func TestExtractRates_HTMLStripped(t *testing.T) {
	html := `<html><body><p>linehaul: <strong>$375</strong></p><p>FSC: 15%</p></body></html>`
	result := ExtractRates(html)

	lh, ok := findItem(result.Items, "linehaul")
	if !ok {
		t.Fatal("linehaul not extracted from HTML input")
	}
	if lh.Amount != 375 {
		t.Errorf("linehaul: got %v, want 375", lh.Amount)
	}
	fuel, ok := findItem(result.Items, "fuel")
	if !ok {
		t.Fatal("fuel not extracted from HTML input")
	}
	if fuel.Amount != 15 {
		t.Errorf("fuel: got %v, want 15", fuel.Amount)
	}
}

func TestExtractRates_LinehaulFallback(t *testing.T) {
	// No labeled rate — should pick highest dollar amount in range.
	text := "Please see rates: $50, $320, $1200, $9999"
	result := ExtractRates(text)

	lh, ok := findItem(result.Items, "linehaul")
	if !ok {
		t.Fatal("linehaul fallback not triggered")
	}
	// $9999 is out of range, $1200 is highest in $100-$5000 range.
	if lh.Amount != 1200 {
		t.Errorf("fallback linehaul: got %v, want 1200", lh.Amount)
	}
	if !strings.Contains(lh.Note, "guessed") {
		t.Errorf("expected Note to say 'guessed', got %q", lh.Note)
	}

	found := false
	for _, n := range result.Notes {
		if strings.Contains(n, "1200") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected a note mentioning the guessed amount")
	}
}

func TestExtractRates_NoRates(t *testing.T) {
	result := ExtractRates("Hello, we will send rates tomorrow.")
	if len(result.Items) != 0 {
		t.Errorf("expected no items, got %d", len(result.Items))
	}
	found := false
	for _, n := range result.Notes {
		if strings.Contains(n, "No rates") {
			found = true
		}
	}
	if !found {
		t.Error("expected 'No rates detected' note")
	}
}

func TestExtractRates_ChassisMinRequiresDays(t *testing.T) {
	// "2 day minimum" → chassis_min matched.
	result := ExtractRates("chassis $30/day, 2 day minimum")
	_, ok := findItem(result.Items, "chassis_min")
	if !ok {
		t.Error("expected chassis_min to match '2 day minimum'")
	}

	// "30 minute" → chassis_min must NOT match (no days keyword).
	result2 := ExtractRates("detention $75/hr, 30 minute free time")
	_, ok2 := findItem(result2.Items, "chassis_min")
	if ok2 {
		t.Error("chassis_min should NOT match '30 minute' (no days keyword)")
	}
}

func TestExtractRates_ExtremeOverweightBeforeRegular(t *testing.T) {
	// Both extreme and regular overweight present — each should land in the right bucket.
	text := "overweight $250, extreme overweight $1000"
	result := ExtractRates(text)

	ew, okE := findItem(result.Items, "extreme_overweight")
	rw, okR := findItem(result.Items, "regular_overweight")

	if !okE {
		t.Error("extreme_overweight not extracted")
	} else if ew.Amount != 1000 {
		t.Errorf("extreme_overweight: got %v, want 1000", ew.Amount)
	}

	if !okR {
		t.Error("regular_overweight not extracted")
	} else if rw.Amount != 250 {
		t.Errorf("regular_overweight: got %v, want 250", rw.Amount)
	}
}

func TestExtractRates_Units(t *testing.T) {
	text := "linehaul $500, FSC 20%, chassis $35/day, detention $75/hr"
	result := ExtractRates(text)

	unitCases := map[string]string{
		"linehaul":  "$",
		"fuel":      "%",
		"chassis":   "$/day",
		"detention": "$/hour",
	}
	for ct, wantUnit := range unitCases {
		item, ok := findItem(result.Items, ct)
		if !ok {
			t.Errorf("missing charge type %q", ct)
			continue
		}
		if item.Unit != wantUnit {
			t.Errorf("%q unit: got %q, want %q", ct, item.Unit, wantUnit)
		}
	}
}

func TestStripHTML(t *testing.T) {
	in := `<p>Hello &amp; <strong>world</strong></p><br/>rate: $500`
	out := stripHTML(in)
	if strings.Contains(out, "<") {
		t.Errorf("HTML tags not stripped: %q", out)
	}
	if !strings.Contains(out, "&") {
		t.Errorf("&amp; not decoded in: %q", out)
	}
	if !strings.Contains(out, "500") {
		t.Errorf("dollar amount not preserved after strip: %q", out)
	}
}

func TestLooksLikeHTML(t *testing.T) {
	if !looksLikeHTML("<html><body>rates</body></html>") {
		t.Error("expected true for HTML string")
	}
	if looksLikeHTML("plain text email") {
		t.Error("expected false for plain text")
	}
}
