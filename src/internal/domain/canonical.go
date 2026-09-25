package domain

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strings"
	"unicode/utf8"
)

const CanonicalizationVersionV1 uint16 = 1

const canonicalHashDomainV1 = "remote-session-runner/canonical-json/v1\x00"

var (
	ErrInvalidCanonicalJSON     = errors.New("invalid canonical mutation JSON")
	ErrInvalidCanonicalDefaults = errors.New("invalid canonical mutation defaults")
	ErrInvalidCanonicalHash     = errors.New("invalid canonical request hash")
)

// CanonicalizationOptions contains effective request defaults resolved by the
// caller. Defaults are inserted only when the request omits that top-level
// field. The environment/config layer must resolve configurable defaults
// before hashing; this pure domain package does not infer them.
type CanonicalizationOptions struct {
	Defaults map[string]json.RawMessage
}

// CanonicalHash is a versioned SHA-256 digest of canonical mutation JSON.
// Its fields are private so invalid version/digest combinations cannot be
// constructed accidentally; NewCanonicalHash is available for store reads.
type CanonicalHash struct {
	version uint16
	digest  [sha256.Size]byte
}

// NewCanonicalHash reconstructs a stored digest. Versions must be nonzero and
// the digest must contain exactly one SHA-256 value. Unknown positive versions
// are retained so comparison can safely classify them as a conflict.
func NewCanonicalHash(version uint16, digest []byte) (CanonicalHash, error) {
	if version == 0 || len(digest) != sha256.Size {
		return CanonicalHash{}, ErrInvalidCanonicalHash
	}
	var sum [sha256.Size]byte
	copy(sum[:], digest)
	return CanonicalHash{version: version, digest: sum}, nil
}

// Version returns the canonicalization version stored with the digest.
func (h CanonicalHash) Version() uint16 { return h.version }

// SHA256 returns a copy of the digest bytes for durable storage.
func (h CanonicalHash) SHA256() []byte {
	return append([]byte(nil), h.digest[:]...)
}

// String returns a version-tagged hexadecimal digest suitable for diagnostics.
func (h CanonicalHash) String() string {
	return fmt.Sprintf("v%d:%s", h.version, hex.EncodeToString(h.digest[:]))
}

// IdempotencyComparison is the pure result of comparing two stored request
// hashes for one already-selected (controller, operation, key) record.
type IdempotencyComparison string

const (
	IdempotencySamePayload IdempotencyComparison = "same_payload"
	IdempotencyConflict    IdempotencyComparison = "conflict"
)

// CompareIdempotency reports whether two request hashes represent the same
// canonical payload. A version mismatch is a conflict, even when digest bytes
// happen to match, so a caller never silently reinterprets an old hash.
func CompareIdempotency(existing, incoming CanonicalHash) IdempotencyComparison {
	if existing.version == 0 || incoming.version == 0 || existing.version != incoming.version {
		return IdempotencyConflict
	}
	if subtle.ConstantTimeCompare(existing.digest[:], incoming.digest[:]) == 1 {
		return IdempotencySamePayload
	}
	return IdempotencyConflict
}

// CanonicalizeMutationRequestJSON returns canonical JSON v1 for one v1
// mutation. The caller supplies the operation derived from its validated
// route/frame; if raw also contains an operation field, it must match. This
// makes the same operation-plus-payload hash independent of mailbox/bridge
// correlation wrappers. Object keys are sorted, array order and string code
// points are preserved, and JSON number spellings are normalized exactly.
// The top-level request_id and idempotency_key identify an exchange/lookup;
// they are excluded from the semantic payload hash. An omitted source on
// create_session/run means the design-defined empty source. Other effective
// top-level defaults must be supplied in options after caller-side
// validation; nested defaults must already be materialized in raw. The
// caller validates the operation-specific schema before using this helper.
func CanonicalizeMutationRequestJSON(operation string, raw []byte, options CanonicalizationOptions) ([]byte, error) {
	if !isMutationOperation(operation) {
		return nil, fmt.Errorf("%w: unsupported mutation operation %q", ErrInvalidCanonicalJSON, operation)
	}
	if err := ValidateSerializedRequest(raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCanonicalJSON, err)
	}
	if !utf8.Valid(raw) || !validUnicodeEscapes(raw) {
		return nil, ErrInvalidCanonicalJSON
	}

	value, err := decodeCanonicalJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCanonicalJSON, err)
	}
	request, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: request must be a JSON object", ErrInvalidCanonicalJSON)
	}
	if suppliedOperation, exists := request["operation"]; exists {
		if suppliedOperation != operation {
			return nil, fmt.Errorf("%w: request operation does not match canonical operation", ErrInvalidCanonicalJSON)
		}
	}

	delete(request, "request_id")
	delete(request, "idempotency_key")
	request["operation"] = operation
	if operation == "create_session" || operation == "run" {
		if _, exists := request["source"]; !exists {
			request["source"] = map[string]any{"mode": "empty"}
		}
	}

	defaultFields := make([]string, 0, len(options.Defaults))
	for name := range options.Defaults {
		defaultFields = append(defaultFields, name)
	}
	sort.Strings(defaultFields)
	for _, name := range defaultFields {
		if !utf8.ValidString(name) {
			return nil, fmt.Errorf("%w: field name is not valid UTF-8", ErrInvalidCanonicalDefaults)
		}
		if name == "" || name == "request_id" || name == "idempotency_key" || name == "operation" || name == "source" {
			return nil, fmt.Errorf("%w: reserved field %q", ErrInvalidCanonicalDefaults, name)
		}
		if len(options.Defaults[name]) > MaxSerializedRequestBytes {
			return nil, fmt.Errorf("%w: field %q exceeds the serialized request limit", ErrInvalidCanonicalDefaults, name)
		}
		defaultValue, err := decodeCanonicalJSON(options.Defaults[name])
		if err != nil {
			return nil, fmt.Errorf("%w: field %q: %v", ErrInvalidCanonicalDefaults, name, err)
		}
		if _, exists := request[name]; !exists {
			request[name] = defaultValue
		}
	}

	return appendCanonicalValue(nil, request)
}

// HashMutationRequestJSON canonicalizes one mutation and computes its
// versioned SHA-256 fingerprint. The caller supplies its validated operation.
// The domain separator prevents these digests from being confused with
// unversioned hashes of other byte streams.
func HashMutationRequestJSON(operation string, raw []byte, options CanonicalizationOptions) (CanonicalHash, error) {
	canonical, err := CanonicalizeMutationRequestJSON(operation, raw, options)
	if err != nil {
		return CanonicalHash{}, err
	}
	input := make([]byte, 0, len(canonicalHashDomainV1)+len(canonical))
	input = append(input, canonicalHashDomainV1...)
	input = append(input, canonical...)
	return CanonicalHash{version: CanonicalizationVersionV1, digest: sha256.Sum256(input)}, nil
}

func isMutationOperation(operation string) bool {
	switch operation {
	case "create_session", "submit_command", "cancel_command", "close_session", "run":
		return true
	default:
		return false
	}
}

func decodeCanonicalJSON(raw []byte) (any, error) {
	if !utf8.Valid(raw) || !validUnicodeEscapes(raw) {
		return nil, ErrInvalidCanonicalJSON
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeCanonicalValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}

func decodeCanonicalValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); ok {
		switch delimiter {
		case '{':
			object := make(map[string]any)
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, errors.New("object key is not a string")
				}
				if _, duplicate := object[key]; duplicate {
					return nil, fmt.Errorf("duplicate object key %q", key)
				}
				value, err := decodeCanonicalValue(decoder)
				if err != nil {
					return nil, err
				}
				object[key] = value
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return nil, errors.New("unterminated object")
			}
			return object, nil
		case '[':
			array := make([]any, 0)
			for decoder.More() {
				value, err := decodeCanonicalValue(decoder)
				if err != nil {
					return nil, err
				}
				array = append(array, value)
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return nil, errors.New("unterminated array")
			}
			return array, nil
		default:
			return nil, fmt.Errorf("unexpected JSON delimiter %q", delimiter)
		}
	}
	return token, nil
}

func appendCanonicalValue(output []byte, value any) ([]byte, error) {
	switch typed := value.(type) {
	case nil:
		return append(output, "null"...), nil
	case bool:
		return append(output, fmt.Sprintf("%t", typed)...), nil
	case string:
		encoded, err := canonicalJSONString(typed)
		if err != nil {
			return nil, err
		}
		return append(output, encoded...), nil
	case json.Number:
		return append(output, canonicalJSONNumber(typed)...), nil
	case []any:
		output = append(output, '[')
		for index, item := range typed {
			if index > 0 {
				output = append(output, ',')
			}
			var err error
			output, err = appendCanonicalValue(output, item)
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
		sort.Strings(keys)
		output = append(output, '{')
		for index, key := range keys {
			if index > 0 {
				output = append(output, ',')
			}
			encodedKey, err := canonicalJSONString(key)
			if err != nil {
				return nil, err
			}
			output = append(output, encodedKey...)
			output = append(output, ':')
			output, err = appendCanonicalValue(output, typed[key])
			if err != nil {
				return nil, err
			}
		}
		return append(output, '}'), nil
	default:
		return nil, fmt.Errorf("unsupported canonical JSON value %T", value)
	}
}

func canonicalJSONString(value string) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'}), nil
}

// canonicalJSONNumber represents every nonzero JSON number as a normalized
// decimal significand and base-10 exponent. This preserves exact decimal
// values without float64 rounding or allocating enormous exponent expansions.
func canonicalJSONNumber(number json.Number) string {
	text := number.String()
	sign := ""
	if strings.HasPrefix(text, "-") {
		sign = "-"
		text = text[1:]
	}

	exponent := new(big.Int)
	if exponentIndex := strings.IndexAny(text, "eE"); exponentIndex >= 0 {
		exponentText := text[exponentIndex+1:]
		text = text[:exponentIndex]
		exponentText = strings.TrimPrefix(exponentText, "+")
		if _, ok := exponent.SetString(exponentText, 10); !ok {
			return number.String()
		}
	}

	fractionLength := 0
	if point := strings.IndexByte(text, '.'); point >= 0 {
		fractionLength = len(text) - point - 1
		text = text[:point] + text[point+1:]
	}
	significant := strings.TrimLeft(text, "0")
	if significant == "" {
		return "0"
	}
	trailingZeros := len(significant) - len(strings.TrimRight(significant, "0"))
	significant = strings.TrimRight(significant, "0")

	decimalPower := new(big.Int).Sub(exponent, big.NewInt(int64(fractionLength)))
	decimalPower.Add(decimalPower, big.NewInt(int64(trailingZeros)))
	scientificExponent := new(big.Int).Add(decimalPower, big.NewInt(int64(len(significant)-1)))

	mantissa := significant[:1]
	if len(significant) > 1 {
		mantissa += "." + significant[1:]
	}
	return sign + mantissa + "e" + scientificExponent.String()
}

func validUnicodeEscapes(raw []byte) bool {
	for index := 0; index < len(raw); index++ {
		if raw[index] != '"' {
			continue
		}
		index++
	stringScan:
		for index < len(raw) {
			switch raw[index] {
			case '"':
				break stringScan
			case '\\':
				if index+1 >= len(raw) {
					return false
				}
				if raw[index+1] != 'u' {
					index++
					break
				}
				codepoint, ok := unicodeEscapeValue(raw[index+2:])
				if !ok {
					return false
				}
				index += 5
				if codepoint >= 0xD800 && codepoint <= 0xDBFF {
					if index+6 >= len(raw) || raw[index+1] != '\\' || raw[index+2] != 'u' {
						return false
					}
					low, ok := unicodeEscapeValue(raw[index+3:])
					if !ok || low < 0xDC00 || low > 0xDFFF {
						return false
					}
					index += 6
				} else if codepoint >= 0xDC00 && codepoint <= 0xDFFF {
					return false
				}
			case '\n', '\r':
				return false
			}
			index++
		}
	}
	return true
}

func unicodeEscapeValue(raw []byte) (uint16, bool) {
	if len(raw) < 4 {
		return 0, false
	}
	var value uint16
	for _, digit := range raw[:4] {
		value <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			value |= uint16(digit - '0')
		case digit >= 'a' && digit <= 'f':
			value |= uint16(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			value |= uint16(digit-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}
