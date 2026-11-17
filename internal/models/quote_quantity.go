package models

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"math/bits"
	"strconv"
	"strings"
)

// Quote line quantities (#343) are exact decimals with at most three
// fractional digits, from 0.001 to 1,000,000. They are carried as integer
// thousandths of a unit (QuoteQuantity) and stored in
// quote_lines.quantity_milli; nothing on the way from the JSON literal to the
// line total goes through a float64.
const (
	// QuoteQuantityScale is one unit in thousandths.
	QuoteQuantityScale int64 = 1000
	// QuoteLineQuantityMilliMin is 0.001, the smallest quantity.
	QuoteLineQuantityMilliMin int64 = 1
	// QuoteLineQuantityMilliMax is QuoteLineQuantityMax (1,000,000) units.
	QuoteLineQuantityMilliMax int64 = QuoteLineQuantityMax * QuoteQuantityScale
)

// QuoteQuantityBoundsMessage is the message of every QuoteQuantityError and
// the error.details.quantity value of the 400 VALIDATION_ERROR answer.
const QuoteQuantityBoundsMessage = "quantity must be a number from 0.001 to 1000000 with at most 3 decimal places"

// Limits on the literal itself, checked before any digit is converted, so a
// huge literal or exponent costs nothing.
const (
	quoteQuantityLiteralMaxBytes = 64
	quoteQuantityExponentMax     = 30
)

// QuoteQuantityError reports a JSON number that is not a valid quote line
// quantity: zero or negative, above 1,000,000, a non-zero digit beyond the
// third fractional place, or a literal too long (over 64 bytes) or with an
// exponent magnitude above 30. A value that is not a JSON number at all is a
// different error.
type QuoteQuantityError struct {
	// Reason is a short, value-free explanation for logs.
	Reason string
}

func (e *QuoteQuantityError) Error() string {
	if e.Reason == "" {
		return QuoteQuantityBoundsMessage
	}
	return QuoteQuantityBoundsMessage + " (" + e.Reason + ")"
}

// errQuoteQuantityNotNumber answers a token that is not a JSON number.
var errQuoteQuantityNotNumber = errors.New("quantity must be a JSON number")

// QuoteQuantity is a quote line quantity in thousandths of a unit. Its JSON
// form is a bare number without trailing zeros (1.5, 2, 0.001); its column is
// quote_lines.quantity_milli (bigint). The zero value is 0 and is never a
// valid quantity of a written line.
type QuoteQuantity struct {
	milli int64
}

// QuoteQuantityFromMilli builds a quantity from thousandths without any check.
// The service refuses one outside 1..10^9 before writing it.
func QuoteQuantityFromMilli(milli int64) QuoteQuantity {
	return QuoteQuantity{milli: milli}
}

// QuoteQuantityFromUnits builds a quantity of n whole units (n × 1000
// thousandths) without any check.
func QuoteQuantityFromUnits(n int64) QuoteQuantity {
	return QuoteQuantity{milli: n * QuoteQuantityScale}
}

// Milli returns the quantity in thousandths of a unit.
func (q QuoteQuantity) Milli() int64 {
	return q.milli
}

// InBounds reports whether the quantity lies within 0.001..1,000,000.
func (q QuoteQuantity) InBounds() bool {
	return q.milli >= QuoteLineQuantityMilliMin && q.milli <= QuoteLineQuantityMilliMax
}

// ParseQuoteQuantity parses a JSON number literal exactly. It accepts every
// literal whose value lies within 0.001..1,000,000 and has no non-zero digit
// beyond the third fractional place, judged by value: 1e-3, 1.5000 and
// 1000000.000 are accepted. Any other number, and a literal longer than 64
// bytes or with an exponent magnitude above 30, is a *QuoteQuantityError. A
// token that is not a JSON number (a string, a boolean, null, an array or an
// object) is a plain error.
func ParseQuoteQuantity(literal string) (QuoteQuantity, error) {
	if literal == "" || (literal[0] != '-' && (literal[0] < '0' || literal[0] > '9')) {
		return QuoteQuantity{}, errQuoteQuantityNotNumber
	}
	if len(literal) > quoteQuantityLiteralMaxBytes {
		return QuoteQuantity{}, &QuoteQuantityError{Reason: "literal too long"}
	}
	negative, intPart, fracPart, expPart, ok := splitJSONNumber(literal)
	if !ok {
		return QuoteQuantity{}, errQuoteQuantityNotNumber
	}

	exponent := 0
	if expPart != "" {
		sign := 1
		switch expPart[0] {
		case '-':
			sign, expPart = -1, expPart[1:]
		case '+':
			expPart = expPart[1:]
		}
		expPart = strings.TrimLeft(expPart, "0")
		if len(expPart) > 2 {
			return QuoteQuantity{}, &QuoteQuantityError{Reason: "exponent out of range"}
		}
		if expPart != "" {
			magnitude, err := strconv.Atoi(expPart)
			if err != nil {
				return QuoteQuantity{}, errQuoteQuantityNotNumber
			}
			if magnitude > quoteQuantityExponentMax {
				return QuoteQuantity{}, &QuoteQuantityError{Reason: "exponent out of range"}
			}
			exponent = sign * magnitude
		}
	}

	// value = digits × 10^(exponent − len(fracPart)); in thousandths the
	// power is three more.
	digits := strings.TrimLeft(intPart+fracPart, "0")
	if digits == "" {
		return QuoteQuantity{}, &QuoteQuantityError{Reason: "not positive"}
	}
	if negative {
		return QuoteQuantity{}, &QuoteQuantityError{Reason: "not positive"}
	}
	shift := exponent - len(fracPart) + 3
	if shift < 0 {
		drop := -shift
		if drop >= len(digits) || strings.TrimLeft(digits[len(digits)-drop:], "0") != "" {
			return QuoteQuantity{}, &QuoteQuantityError{Reason: "too many decimal places"}
		}
		digits = digits[:len(digits)-drop]
	} else if shift > 0 {
		if len(digits)+shift > 10 {
			return QuoteQuantity{}, &QuoteQuantityError{Reason: "above the maximum"}
		}
		digits += strings.Repeat("0", shift)
	}
	if len(digits) > 10 {
		return QuoteQuantity{}, &QuoteQuantityError{Reason: "above the maximum"}
	}
	milli, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return QuoteQuantity{}, &QuoteQuantityError{Reason: "above the maximum"}
	}
	quantity := QuoteQuantity{milli: milli}
	if !quantity.InBounds() {
		return QuoteQuantity{}, &QuoteQuantityError{Reason: "out of range"}
	}
	return quantity, nil
}

// splitJSONNumber checks the JSON number grammar
// -?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)? and returns its parts; the
// exponent part keeps its sign.
func splitJSONNumber(s string) (negative bool, intPart, fracPart, expPart string, ok bool) {
	i := 0
	if i < len(s) && s[i] == '-' {
		negative = true
		i++
	}
	start := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	intPart = s[start:i]
	if intPart == "" || (len(intPart) > 1 && intPart[0] == '0') {
		return false, "", "", "", false
	}
	if i < len(s) && s[i] == '.' {
		i++
		start = i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		fracPart = s[start:i]
		if fracPart == "" {
			return false, "", "", "", false
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		start = i
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		digitsStart := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == digitsStart {
			return false, "", "", "", false
		}
		expPart = s[start:i]
	}
	if i != len(s) {
		return false, "", "", "", false
	}
	return negative, intPart, fracPart, expPart, true
}

// String renders the quantity as a decimal without trailing zeros: 1500 is
// "1.5", 2000 is "2", 1 is "0.001". It is also the JSON form.
func (q QuoteQuantity) String() string {
	negative := q.milli < 0
	magnitude := uint64(q.milli)
	if negative {
		magnitude = uint64(-(q.milli + 1)) + 1
	}
	whole := magnitude / uint64(QuoteQuantityScale)
	frac := magnitude % uint64(QuoteQuantityScale)
	text := strconv.FormatUint(whole, 10)
	if frac != 0 {
		text += "." + strings.TrimRight(fmt.Sprintf("%03d", frac), "0")
	}
	if negative {
		text = "-" + text
	}
	return text
}

// MarshalJSON writes the quantity as a bare JSON number, never a string.
func (q QuoteQuantity) MarshalJSON() ([]byte, error) {
	return []byte(q.String()), nil
}

// UnmarshalJSON decodes a JSON number exactly through ParseQuoteQuantity. A
// JSON null leaves the value unchanged (a pointer field stays nil without
// calling this method at all).
func (q *QuoteQuantity) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}
	parsed, err := ParseQuoteQuantity(string(data))
	if err != nil {
		return err
	}
	*q = parsed
	return nil
}

// Value stores the quantity as its thousandths.
func (q QuoteQuantity) Value() (driver.Value, error) {
	return q.milli, nil
}

// Scan reads quote_lines.quantity_milli.
func (q *QuoteQuantity) Scan(src interface{}) error {
	switch v := src.(type) {
	case nil:
		q.milli = 0
	case int64:
		q.milli = v
	case int32:
		q.milli = int64(v)
	case int:
		q.milli = int64(v)
	case uint64:
		if v > uint64(1<<63-1) {
			return fmt.Errorf("quantity_milli %d does not fit an int64", v)
		}
		q.milli = int64(v)
	case []byte:
		parsed, err := strconv.ParseInt(string(v), 10, 64)
		if err != nil {
			return fmt.Errorf("quantity_milli is not an integer: %w", err)
		}
		q.milli = parsed
	case string:
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("quantity_milli is not an integer: %w", err)
		}
		q.milli = parsed
	default:
		return fmt.Errorf("cannot scan %T into a quote quantity", src)
	}
	return nil
}

// uint128 is an unsigned 128-bit integer for the exact line arithmetic.
type uint128 struct{ hi, lo uint64 }

// mulUint128 returns a × m, or false when the product needs more than 128
// bits.
func mulUint128(a uint128, m uint64) (uint128, bool) {
	h1, l1 := bits.Mul64(a.lo, m)
	h2, l2 := bits.Mul64(a.hi, m)
	if h2 != 0 {
		return uint128{}, false
	}
	hi, carry := bits.Add64(h1, l2, 0)
	if carry != 0 {
		return uint128{}, false
	}
	return uint128{hi: hi, lo: l1}, true
}

// divRoundHalfUp returns round_half_up(a / d) for an even d, or false when
// the quotient does not fit an int64.
func divRoundHalfUp(a uint128, d uint64) (int64, bool) {
	lo, carry := bits.Add64(a.lo, d/2, 0)
	hi, carry := bits.Add64(a.hi, 0, carry)
	if carry != 0 {
		return 0, false
	}
	if hi/d != 0 {
		return 0, false
	}
	quotient, _ := bits.Div64(hi%d, lo, d)
	if quotient > uint64(1<<63-1) {
		return 0, false
	}
	return int64(quotient), true
}

// ComputeQuoteLineAmounts returns a line's gross and its total after the
// per-line discount, both rounded half up to the minor unit and computed in
// 128-bit integer arithmetic:
//
//	gross     = round_half_up(milli × unit_price_cents / 1000)
//	lineTotal = round_half_up(milli × unit_price_cents × (10000 − discount_bp) / 10^7)
//
// The line total is rounded once, from the exact product, not from the
// rounded gross. ok is false when the rounded gross exceeds
// QuoteAmountCentsMax, when an input is negative or the discount is above
// 10000, or when a result would not fit an int64; for whole quantities the
// results equal ComputeQuoteLineTotal's.
func ComputeQuoteLineAmounts(quantity QuoteQuantity, unitPriceCents, discountBP int64) (gross, lineTotal int64, ok bool) {
	if quantity.milli < 0 || unitPriceCents < 0 || discountBP < 0 || discountBP > QuoteDiscountBPScale {
		return 0, 0, false
	}
	hi, lo := bits.Mul64(uint64(quantity.milli), uint64(unitPriceCents))
	product := uint128{hi: hi, lo: lo}
	gross, ok = divRoundHalfUp(product, uint64(QuoteQuantityScale))
	if !ok {
		return 0, 0, false
	}
	discounted, ok := mulUint128(product, uint64(QuoteDiscountBPScale-discountBP))
	if !ok {
		return gross, 0, false
	}
	lineTotal, ok = divRoundHalfUp(discounted, uint64(QuoteQuantityScale*QuoteDiscountBPScale))
	if !ok {
		return gross, 0, false
	}
	return gross, lineTotal, gross <= QuoteAmountCentsMax
}

// quoteLineGross is the rounded gross of a stored line, as the subtotal sums
// it.
func quoteLineGross(line QuoteLine) int64 {
	gross, _, _ := ComputeQuoteLineAmounts(line.Quantity, line.UnitPriceCents, 0)
	return gross
}
