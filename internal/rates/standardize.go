package rates

import "fmt"

// standardize validates that each item's unit matches the chargeTypeMeta expectation.
// Items with wrong units get a Note but are NOT dropped (mismatches only arise from LLM output).
func standardize(items []RateItem) []RateItem {
	out := make([]RateItem, len(items))
	for i, item := range items {
		expected, ok := chargeTypeMeta[item.ChargeType]
		if ok && item.Unit != expected {
			item.Note = fmt.Sprintf("unit mismatch: got %q, expected %q", item.Unit, expected)
		}
		out[i] = item
	}
	return out
}
