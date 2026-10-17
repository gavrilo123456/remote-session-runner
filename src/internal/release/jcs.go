package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

var ErrInvalidCanonicalJSON = errors.New("invalid JCS JSON")

// CanonicalizeJSON returns the RFC 8785 JSON Canonicalization Scheme encoding.
// Duplicate properties, invalid Unicode, non-finite numbers, and negative zero
// are rejected before canonical output is produced.
func CanonicalizeJSON(raw []byte) ([]byte, error) {
	if !utf8.Valid(raw) || !validJSONUnicodeEscapes(raw) {
		return nil, ErrInvalidCanonicalJSON
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeJSONValue(decoder)
	if err != nil {
		return nil, fmt.Errorf("%w: decode", ErrInvalidCanonicalJSON)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, ErrInvalidCanonicalJSON
	}
	output := make([]byte, 0, len(raw))
	output, err = appendJCSValue(output, value)
	if err != nil {
		return nil, fmt.Errorf("%w: value", ErrInvalidCanonicalJSON)
	}
	return output, nil
}

func decodeJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			object := make(map[string]any)
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, ErrInvalidCanonicalJSON
				}
				if _, duplicate := object[key]; duplicate {
					return nil, ErrInvalidCanonicalJSON
				}
				member, err := decodeJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				object[key] = member
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return nil, ErrInvalidCanonicalJSON
			}
			return object, nil
		case '[':
			array := make([]any, 0)
			for decoder.More() {
				item, err := decodeJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				array = append(array, item)
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return nil, ErrInvalidCanonicalJSON
			}
			return array, nil
		default:
			return nil, ErrInvalidCanonicalJSON
		}
	case json.Number:
		if _, err := canonicalJSONNumber(value); err != nil {
			return nil, err
		}
		return value, nil
	case string, bool, nil:
		return value, nil
	default:
		return nil, ErrInvalidCanonicalJSON
	}
}

func appendJCSValue(output []byte, value any) ([]byte, error) {
	switch typed := value.(type) {
	case nil:
		return append(output, "null"...), nil
	case bool:
		if typed {
			return append(output, "true"...), nil
		}
		return append(output, "false"...), nil
	case string:
		return appendJCSString(output, typed), nil
	case json.Number:
		number, err := canonicalJSONNumber(typed)
		if err != nil {
			return nil, err
		}
		return append(output, number...), nil
	case []any:
		output = append(output, '[')
		for index, item := range typed {
			if index != 0 {
				output = append(output, ',')
			}
			var err error
			output, err = appendJCSValue(output, item)
			if err != nil {
				return nil, err
			}
		}
		return append(output, ']'), nil
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return lessUTF16(keys[i], keys[j]) })
		output = append(output, '{')
		for index, key := range keys {
			if index != 0 {
				output = append(output, ',')
			}
			output = appendJCSString(output, key)
			output = append(output, ':')
			var err error
			output, err = appendJCSValue(output, typed[key])
			if err != nil {
				return nil, err
			}
		}
		return append(output, '}'), nil
	default:
		return nil, ErrInvalidCanonicalJSON
	}
}

func appendJCSString(output []byte, value string) []byte {
	const hex = "0123456789abcdef"
	output = append(output, '"')
	for _, character := range value {
		switch character {
		case '"', '\\':
			output = append(output, '\\', byte(character))
		case '\b':
			output = append(output, '\\', 'b')
		case '\t':
			output = append(output, '\\', 't')
		case '\n':
			output = append(output, '\\', 'n')
		case '\f':
			output = append(output, '\\', 'f')
		case '\r':
			output = append(output, '\\', 'r')
		default:
			if character < 0x20 {
				output = append(output, '\\', 'u', '0', '0', hex[byte(character)>>4], hex[byte(character)&0x0f])
			} else {
				output = utf8.AppendRune(output, character)
			}
		}
	}
	return append(output, '"')
}

func canonicalJSONNumber(number json.Number) (string, error) {
	text := number.String()
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) || (value == 0 && strings.HasPrefix(text, "-")) {
		return "", ErrInvalidCanonicalJSON
	}
	if value == 0 {
		return "0", nil
	}
	return ecmaNumber(value), nil
}

// ecmaNumber applies ECMAScript's fixed/scientific notation thresholds to the
// shortest round-tripping binary64 digits produced by strconv's Ryu formatter.
func ecmaNumber(value float64) string {
	scientific := strconv.FormatFloat(value, 'e', -1, 64)
	parts := strings.SplitN(scientific, "e", 2)
	exponent, _ := strconv.Atoi(parts[1])
	mantissa := parts[0]
	sign := ""
	if strings.HasPrefix(mantissa, "-") {
		sign = "-"
		mantissa = strings.TrimPrefix(mantissa, "-")
	}
	digits := strings.ReplaceAll(mantissa, ".", "")
	if exponent >= 0 && exponent < 21 {
		decimalPosition := exponent + 1
		if decimalPosition >= len(digits) {
			return sign + digits + strings.Repeat("0", decimalPosition-len(digits))
		}
		return sign + digits[:decimalPosition] + "." + digits[decimalPosition:]
	}
	if exponent < 0 && exponent >= -6 {
		return sign + "0." + strings.Repeat("0", -exponent-1) + digits
	}
	exponentSign := ""
	if exponent >= 0 {
		exponentSign = "+"
	}
	if len(digits) > 1 {
		mantissa = digits[:1] + "." + digits[1:]
	} else {
		mantissa = digits
	}
	return sign + mantissa + "e" + exponentSign + strconv.Itoa(exponent)
}

func lessUTF16(left, right string) bool {
	leftUnits := utf16.Encode([]rune(left))
	rightUnits := utf16.Encode([]rune(right))
	limit := len(leftUnits)
	if len(rightUnits) < limit {
		limit = len(rightUnits)
	}
	for index := 0; index < limit; index++ {
		if leftUnits[index] != rightUnits[index] {
			return leftUnits[index] < rightUnits[index]
		}
	}
	return len(leftUnits) < len(rightUnits)
}

// validJSONUnicodeEscapes rejects isolated UTF-16 surrogate escapes. Go's JSON
// decoder replaces them with U+FFFD, which would otherwise hide invalid input.
func validJSONUnicodeEscapes(raw []byte) bool {
	for index := 0; index < len(raw); {
		if raw[index] != '"' {
			index++
			continue
		}
		index++
		closed := false
		for index < len(raw) {
			switch raw[index] {
			case '"':
				index++
				closed = true
			case '\\':
				index++
				if index >= len(raw) {
					return false
				}
				if raw[index] != 'u' {
					index++
					continue
				}
				codeUnit, ok := parseHexCodeUnit(raw, index+1)
				if !ok {
					return false
				}
				index += 5
				if codeUnit >= 0xdc00 && codeUnit <= 0xdfff {
					return false
				}
				if codeUnit >= 0xd800 && codeUnit <= 0xdbff {
					if index+6 > len(raw) || raw[index] != '\\' || raw[index+1] != 'u' {
						return false
					}
					low, ok := parseHexCodeUnit(raw, index+2)
					if !ok || low < 0xdc00 || low > 0xdfff {
						return false
					}
					index += 6
				}
			default:
				if raw[index] < 0x20 {
					return false
				}
				index++
			}
			if closed {
				break
			}
		}
		if !closed {
			return false
		}
	}
	return true
}

func parseHexCodeUnit(raw []byte, start int) (uint16, bool) {
	if start+4 > len(raw) {
		return 0, false
	}
	parsed, err := strconv.ParseUint(string(raw[start:start+4]), 16, 16)
	return uint16(parsed), err == nil
}
