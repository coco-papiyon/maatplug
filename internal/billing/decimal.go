package billing

import (
	"errors"
	"math/big"
	"regexp"
	"strings"
)

var decimalPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

func validDecimal(value string) bool { return decimalPattern.MatchString(value) }

func decimalPlaces(value string) int {
	if index := strings.IndexByte(value, '.'); index >= 0 {
		return len(value) - index - 1
	}
	return 0
}

func addDecimals(values ...string) (string, error) {
	total := new(big.Rat)
	for _, value := range values {
		parsed, ok := new(big.Rat).SetString(value)
		if !ok {
			return "", errors.New("invalid decimal")
		}
		total.Add(total, parsed)
	}
	return formatRat(total, 12), nil
}

func formatRat(value *big.Rat, scale int) string {
	factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	scaled := new(big.Rat).Mul(value, new(big.Rat).SetInt(factor))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(scaled.Num(), scaled.Denom(), remainder)
	if new(big.Int).Abs(new(big.Int).Mul(remainder, big.NewInt(2))).Cmp(scaled.Denom()) >= 0 {
		if scaled.Sign() >= 0 {
			quotient.Add(quotient, big.NewInt(1))
		} else {
			quotient.Sub(quotient, big.NewInt(1))
		}
	}
	negative := quotient.Sign() < 0
	digits := new(big.Int).Abs(quotient).String()
	for len(digits) <= scale {
		digits = "0" + digits
	}
	if scale > 0 {
		digits = digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
		digits = strings.TrimRight(strings.TrimRight(digits, "0"), ".")
	}
	if negative && digits != "0" {
		return "-" + digits
	}
	return digits
}

func safeIdentifier(value string) bool {
	if value == "" || len(value) > 160 {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '.' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}
