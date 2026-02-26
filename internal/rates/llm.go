package rates

// LLMCorrector is the Stage 2 interface for LLM-assisted rate correction.
type LLMCorrector interface {
	CorrectRates(rawEmail string, items []RateItem) ([]RateItem, error)
}

// NoopCorrector passes items through unchanged (default until an LLM is wired in).
type NoopCorrector struct{}

func (NoopCorrector) CorrectRates(_ string, items []RateItem) ([]RateItem, error) {
	return items, nil
}
