package gateway

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

const geminiPricingURL = "https://aistudio.google.com/docs/pricing.md"

var geminiPriceModel = regexp.MustCompile("\\[`(gemini-[a-z0-9.-]+)`\\]\\(/docs/models/")
var geminiStandardTable = regexp.MustCompile(`(?s)\{% tab title="Standard" %\}\s*(\{% table %\}.*?\{% /table %\})`)
var geminiPriceTable = regexp.MustCompile(`(?s)\{% table %\}.*?\{% /table %\}`)
var geminiDatedRate = regexp.MustCompile(`^\s*(\$[0-9.]+) through ([A-Za-z]+ [0-9]+, [0-9]{4})\.\s*(\$[0-9.]+) starting ([A-Za-z]+ [0-9]+, [0-9]{4})\.\s*$`)

func parseGeminiRate(value string, now time.Time) (*string, bool) {
	var rates []string
	for line := range strings.SplitSeq(value, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "(storage price)") || strings.HasPrefix(line, "Equivalent to ") {
			continue
		}
		rates = append(rates, line)
	}
	// Use the published text rate when Google lists separate modalities.
	if len(rates) > 1 && (strings.HasSuffix(rates[0], " (text)") || strings.HasSuffix(rates[0], " (text / image / video)")) {
		rates = rates[:1]
	}
	value = strings.Join(rates, "\n")
	value = strings.NewReplacer(" (text / image / video)", "", " (text)", "", " (audio)", "").Replace(value)
	if parts := geminiDatedRate.FindStringSubmatch(value); parts != nil {
		last, lastErr := time.Parse("January 2, 2006", parts[2])
		first, firstErr := time.Parse("January 2, 2006", parts[4])
		if lastErr != nil || firstErr != nil || !last.AddDate(0, 0, 1).Equal(first) {
			return nil, false
		}
		if now.UTC().Before(first) {
			return parsePrice(parts[1])
		}
		return parsePrice(parts[3])
	}
	return parsePrice(value)
}

func parseGeminiPricing(markdown string, now time.Time) ([]modelPrice, error) {
	prices := []modelPrice{}
	seen := map[string]bool{}
	for _, section := range strings.Split(markdown, "\n## ")[1:] {
		table := geminiStandardTable.FindString(section)
		if table == "" && !strings.Contains(section, "{% tab ") {
			table = geminiPriceTable.FindString(section)
		}
		if table == "" || !strings.Contains(table, "* Paid Tier, per 1M tokens in USD") {
			continue
		}
		intro := section[:strings.Index(section, table)]
		firstModel := geminiPriceModel.FindStringIndex(intro)
		if firstModel == nil {
			continue
		}
		modelList, _, _ := strings.Cut(intro[firstModel[0]:], "\n\n")
		model := geminiPriceModel.FindAllStringSubmatch(modelList, -1)
		price := modelPrice{LongPolicy: "none", OutputApplicable: model[0][1] != "gemini-embedding-001"}
		valid := true
		for _, row := range strings.Split(table, "\n  ---") {
			cells := strings.Split(row, "\n  * ")
			if len(cells) != 4 {
				continue
			}
			label, paid := strings.TrimSpace(cells[1]), strings.TrimSpace(cells[3])
			paid = strings.TrimSpace(strings.Split(paid, "{% /table %}")[0])
			switch label {
			case "Input price", "Output price", "Output price (including thinking tokens)", "Context caching price":
				if paid == "Not available" || paid == "Not applicable" {
					continue
				}
				paid = strings.TrimSpace(strings.TrimSuffix(paid, "Implicit caching only (explicit caching is not supported)."))
				rate, ok := parseGeminiRate(paid, now)
				valid = valid && ok
				switch label {
				case "Input price":
					price.Input = rate
				case "Context caching price":
					price.Cached = rate
				default:
					price.Output = rate
				}
			}
		}
		if !valid || price.Input == nil || (price.OutputApplicable && price.Output == nil) {
			continue
		}
		for _, match := range model {
			price.Model = match[1]
			if seen[price.Model] {
				return nil, errors.New("pricing_conflict")
			}
			seen[price.Model] = true
			prices = append(prices, price)
		}
	}
	if len(prices) == 0 {
		return nil, errors.New("pricing_format_changed")
	}
	return prices, nil
}
