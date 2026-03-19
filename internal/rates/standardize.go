package rates

import (
	"fmt"
	"math"
)

// standardize validates that each item's unit matches the chargeTypeMeta expectation
// and rounds dollar-denominated amounts up to the nearest whole dollar.
// Items with wrong units get a Note but are NOT dropped (mismatches only arise from LLM output).
func standardize(items []RateItem) []RateItem {
	out := make([]RateItem, len(items))
	for i, item := range items {
		expected, ok := chargeTypeMeta[item.ChargeType]
		if ok && item.Unit != expected {
			item.Note = fmt.Sprintf("unit mismatch: got %q, expected %q", item.Unit, expected)
		}
		switch item.Unit {
		case "$", "$/day", "$/hour":
			item.Amount = math.Ceil(item.Amount)
		}
		out[i] = item
	}
	return out
}
