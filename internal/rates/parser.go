package rates

import (
	"regexp"
	"strconv"
	"strings"
)

// RateItem represents a single parsed charge from a vendor rate email.
type RateItem struct {
	ChargeType string
	Amount     float64
	Unit       string // "$", "%", "$/day", "hours", "days", "$/hour"
	Source     string // "regex" | "llm" | "manual"
	Note       string // populated for guesses/warnings
}

// ParseResult holds the output of the three-stage parse pipeline.
type ParseResult struct {
	Items []RateItem
	Notes []string
}

// chargeTypeMeta maps each canonical charge type to its expected unit.
var chargeTypeMeta = map[string]string{
	"linehaul":           "$",
	"fuel":               "%",
	"chassis":            "$/day",
	"chassis_min":        "days",
	"detention":          "$/hour",
	"detention_free":     "hours",
	"storage":            "$/day",
	"yard_pull":          "$",
	"chassis_split":      "$",
	"mount":              "$",
	"lift":               "$",
	"redelivery":         "$",
	"dry_run":            "$",
	"toll":               "$",
	"triaxle":            "$/day",
	"extreme_overweight": "$",
	"regular_overweight": "$",
	"reefer":             "$",
	"genset":             "$/day",
	"hazmat":             "$",
	"stop_off":           "$",
	"layover":            "$",
	"drop":               "$",
	"scale":              "$",
	"congestion":         "$/hour",
	"congestion_free":    "hours",
	"gate":               "$",
}

// FieldOrder defines the canonical charge type ordering for display and extraction.
// extreme_overweight must precede regular_overweight to prevent misclassification.
var FieldOrder = []string{
	"linehaul",
	"fuel",
	"chassis",
	"chassis_min",
	"detention",
	"detention_free",
	"storage",
	"yard_pull",
	"chassis_split",
	"mount",
	"lift",
	"redelivery",
	"dry_run",
	"toll",
	"triaxle",
	"extreme_overweight",
	"regular_overweight",
	"reefer",
	"genset",
	"hazmat",
	"stop_off",
	"layover",
	"drop",
	"scale",
	"congestion",
	"congestion_free",
	"gate",
}

// patternSet maps charge type to compiled patterns, built once in init().
var patternSet map[string][]*regexp.Regexp

// rawPatterns holds the source pattern strings per charge type.
var rawPatterns = map[string][]string{
	"linehaul": {
		`(?i)linehaul[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)line\s*haul[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)base\s*rate[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)dray\s*rate[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)dray[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)all[- ]?in[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*(?:linehaul|line\s*haul)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*(?:base\s*rate)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*(?:dray\s*rate|dray(?:\s*age)?)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*(?:all[- ]?in)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*\+\s*(?:fsc|fuel)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*(?:\+\s*)?(?:fsc|fuel)\s*(?:included|incl)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*plus\s*(?:fsc|fuel)`,
	},
	"fuel": {
		`(?i)fsc[-,=:\s]*(\d+(?:\.\d+)?)\s*%`,
		`(?i)fuel[-,=:\s]*(\d+(?:\.\d+)?)\s*%`,
		`(?i)fuel\s*surcharge[-,=:\s]*(\d+(?:\.\d+)?)\s*%`,
		`(?i)(\d+(?:\.\d+)?)\s*%\s*fsc`,
		`(?i)(\d+(?:\.\d+)?)\s*%\s*fuel`,
		`(?i)\+\s*(\d+(?:\.\d+)?)\s*%\s*(?:fsc|fuel)`,
		`(?i)\+\s*(\d+(?:\.\d+)?)\s*%`,
	},
	"chassis": {
		`(?i)chassis[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)\s*(?:/|per)?\s*day`,
		`(?i)chassis\s*rentals?[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)chassis[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)regular\s*chassis?[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*(?:/|per|a)?\s*day\s*chassis(?:rental)?`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*chassis`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*regular\s*chassis`,
	},
	// Tightened: require explicit "day/days" word (JS had overly broad "min" suffix).
	"chassis_min": {
		`(?i)(?:min|minimum)[-,=:\s]\s*(\d+(?:\.\d+)?)\s*(?:days?)\b`,
		`(?i)(\d+(?:\.\d+)?)\s*(?:days?)\s*(?:min|minimum)\b`,
	},
	"detention": {
		`(?i)detention[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)\s*(?:/|per)?\s*(?:hour|hr)?`,
		`(?i)wait\s*time[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)\s*(?:/|per)?\s*(?:hour|hr)?`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*(?:/|per)?\s*(?:hour|hr)?\s*detention`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*(?:/|per)?\s*(?:hour|hr)?\s*wait\s*time`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*(?:/|per)\s*(?:hour|hr)`,
	},
	"detention_free": {
		`(?i)after\s*(\d+)\s*(?:hours?|hrs?)`,
		`(?i)(\d+)\s*(?:hours?|hrs?)\s*free`,
		`(?i)free\s*time[-,=:\s]*(\d+)`,
		`(?i)(\d+)\s*(?:hours?|hrs?)\s*(?:free\s*time|included)`,
	},
	"storage": {
		`(?i)(?:yard\s*)?storage[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)\s*(?:/|per|a)?\s*day`,
		`(?i)storage[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*(?:/|per|a)?\s*day\s*(?:yard\s*)?storage`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*storage`,
	},
	// Absorbs JS prePull patterns, renamed to yard_pull.
	"yard_pull": {
		`(?i)pre[- ]?pulls?[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)yard[- ]?pulls?[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)yard[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*(?:pre[- ]?pull|yard[- ]?pull)`,
	},
	"chassis_split": {
		`(?i)chassis\s*split[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)split[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*(?:each\s*)?chassis\s*split`,
	},
	"mount": {
		`(?i)mount\s*fee[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)mount[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*mount`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*mount\s*fee`,
	},
	"lift": {
		`(?i)rail\s*lift[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)lift[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*lift`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*lift\s*fee`,
	},
	"redelivery": {
		`(?i)redelivery\s*fee[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)redelivery[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*redelivery`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*redelivery\s*fee`,
	},
	"dry_run": {
		`(?i)dry\s*run[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*dry\s*run`,
	},
	"toll": {
		`(?i)tolls?[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*toll`,
	},
	"triaxle": {
		`(?i)tri[- ]?axles?[-,=:\s]*(?:rental)?\s*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)tri[- ]?axle\s*rental[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*(?:/|per)?\s*day\s*tri[- ]?axle`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*tri[- ]?axle`,
	},
	// extreme_overweight must be extracted before regular_overweight.
	"extreme_overweight": {
		`(?i)extreme\s*(?:overweight|ow)[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)super\s*overweight[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*extreme\s*(?:overweight|ow)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*super\s*overweight`,
	},
	"regular_overweight": {
		`(?i)overweight[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)overweight\s*fee[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)\bOW[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)\bOW\s*fee[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*(?:overweight|OW\b)`,
	},
	"reefer": {
		`(?i)reefers?[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)refrigerat(?:ed|or)[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*reefer`,
	},
	"genset": {
		`(?i)gensets?[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)gen[- ]?set[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*(?:/|per)?\s*day\s*genset`,
	},
	"hazmat": {
		`(?i)hazmat[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)haz[- ]?mat[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)hazardous[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*hazmat`,
	},
	"stop_off": {
		`(?i)stop[- ]?off[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*stop[- ]?off`,
	},
	"layover": {
		`(?i)layover[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)lay[- ]?over[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*layover`,
	},
	"drop": {
		`(?i)drop[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)pick[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)hook[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)drop\s*[/&]\s*pick[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)live\s*(?:load|unload)[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*(?:drop|pick|hook)`,
	},
	"scale": {
		`(?i)scale[-,=:\s]*(?:fee)?\s*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*scale`,
	},
	"congestion": {
		`(?i)congestion[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)\s*(?:/|per)?\s*(?:hour|hr)?`,
		`(?i)port\s*congestion[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*(?:/|per)?\s*(?:hour|hr)?\s*congestion`,
	},
	"congestion_free": {
		`(?i)congestion.*?(\d+)\s*(?:hours?|hrs?)\s*free`,
		`(?i)(\d+)\s*(?:hours?|hrs?)\s*free.*?congestion`,
	},
	"gate": {
		`(?i)gate\s*fee[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)port\s*gate[-,=:\s]*\$?\s*(\d+(?:\.\d+)?)`,
		`(?i)\$\s*(\d+(?:\.\d+)?)\s*gate\s*fee`,
	},
}

func init() {
	patternSet = make(map[string][]*regexp.Regexp, len(rawPatterns))
	for ct, strs := range rawPatterns {
		compiled := make([]*regexp.Regexp, 0, len(strs))
		for _, s := range strs {
			compiled = append(compiled, regexp.MustCompile(s))
		}
		patternSet[ct] = compiled
	}
}

// looksLikeHTML returns true if s contains HTML markers.
func looksLikeHTML(s string) bool {
	return strings.Contains(s, "<html") ||
		strings.Contains(s, "<HTML") ||
		strings.Contains(s, "<body") ||
		strings.Contains(s, "<div") ||
		strings.Contains(s, "<p>") ||
		strings.Contains(s, "<br") ||
		strings.Contains(s, "<span")
}

var (
	reHTMLTag    = regexp.MustCompile(`<[^>]+>`)
	reMultiSpace = regexp.MustCompile(`[ \t]{2,}`)
	reMultiLine  = regexp.MustCompile(`\n{3,}`)
)

// stripHTML removes tags and decodes common HTML entities.
func stripHTML(s string) string {
	// Replace <br> and <p> with newlines before stripping tags.
	s = regexp.MustCompile(`(?i)<br\s*/?>`).ReplaceAllString(s, "\n")
	s = regexp.MustCompile(`(?i)</p>`).ReplaceAllString(s, "\n")
	s = reHTMLTag.ReplaceAllString(s, " ")
	// Decode common entities.
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = reMultiSpace.ReplaceAllString(s, " ")
	s = reMultiLine.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// matchPatterns tries each compiled pattern and returns the first captured numeric string.
func matchPatterns(text string, patterns []*regexp.Regexp) string {
	for _, re := range patterns {
		m := re.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		// Return the last non-empty capture group (handles patterns with 2 groups).
		for i := len(m) - 1; i >= 1; i-- {
			if m[i] != "" {
				return m[i]
			}
		}
	}
	return ""
}

// reDollarAmount finds all bare dollar amounts in text.
var reDollarAmount = regexp.MustCompile(`\$\s*(\d+(?:\.\d+)?)`)

// ExtractRates runs Stage 1 regex extraction on pre-processed plain text.
// Includes linehaul fallback: highest $ amount in $100–$5000 range if no pattern matches.
func ExtractRates(text string) ParseResult {
	if looksLikeHTML(text) {
		text = stripHTML(text)
	}

	var result ParseResult
	seen := make(map[string]bool)

	for _, ct := range FieldOrder {
		patterns, ok := patternSet[ct]
		if !ok {
			continue
		}
		val := matchPatterns(text, patterns)
		if val == "" {
			continue
		}
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			continue
		}
		unit := chargeTypeMeta[ct]
		result.Items = append(result.Items, RateItem{
			ChargeType: ct,
			Amount:     f,
			Unit:       unit,
			Source:     "regex",
		})
		seen[ct] = true
	}

	// Linehaul fallback: highest dollar amount in $100–$5000 range.
	if !seen["linehaul"] {
		matches := reDollarAmount.FindAllStringSubmatch(text, -1)
		var best float64
		for _, m := range matches {
			f, err := strconv.ParseFloat(m[1], 64)
			if err != nil {
				continue
			}
			if f >= 100 && f <= 5000 && f > best {
				best = f
			}
		}
		if best > 0 {
			result.Items = append([]RateItem{{
				ChargeType: "linehaul",
				Amount:     best,
				Unit:       "$",
				Source:     "regex",
				Note:       "guessed from highest $ amount",
			}}, result.Items...)
			result.Notes = append(result.Notes, "Linehaul guessed from highest amount: $"+strconv.FormatFloat(best, 'f', -1, 64))
		}
	}

	// Correctly check for empty items (fixes JS bug: `rates.length = 0`).
	if len(result.Items) == 0 {
		result.Notes = append(result.Notes, "No rates detected. Enter manually.")
	}

	return result
}

// DefaultUnit returns the expected unit for a given charge type key, defaulting to "$".
func DefaultUnit(ct string) string {
	if u, ok := chargeTypeMeta[ct]; ok {
		return u
	}
	return "$"
}
