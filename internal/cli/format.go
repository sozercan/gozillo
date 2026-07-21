package cli

import (
	"strconv"
	"strings"

	"gozillo/internal/zillow"
)

func formatAddress(address zillow.Address) string {
	if strings.TrimSpace(address.Full) != "" {
		return address.Full
	}
	locality := strings.TrimSpace(strings.Join(nonEmptyStrings(address.City, address.State, address.PostalCode), " "))
	return strings.Join(nonEmptyStrings(address.Street, locality), ", ")
}

func formatMoney(value *int64, fallback string) string {
	if value == nil {
		return fallback
	}
	return "$" + commaInt(*value)
}

func formatInteger(value *int64) string {
	if value == nil {
		return ""
	}
	return commaInt(*value)
}

func formatPlainInteger(value *int64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(*value, 10)
}

func formatFloat(value *float64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatFloat(*value, 'f', -1, 64)
}

func commaInt(value int64) string {
	sign := ""
	if value < 0 {
		sign = "-"
		value = -value
	}
	digits := strconv.FormatInt(value, 10)
	for index := len(digits) - 3; index > 0; index -= 3 {
		digits = digits[:index] + "," + digits[index:]
	}
	return sign + digits
}

func nonEmptyStrings(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}
