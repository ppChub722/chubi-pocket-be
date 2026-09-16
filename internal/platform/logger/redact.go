package logger

import (
	"encoding/json"
	"strings"
)

// redactKeys — any JSON key containing one of these substrings
// (case-insensitive) gets its value replaced. From logging-plan.md
// component 3. When unsure, add to this list: adding fields later is easy,
// un-leaking a token is not.
var redactKeys = []string{
	"password",
	"passwd",
	"secret",
	"token",
	"authorization",
	"apikey",
	"api_key",
	"pin",
	"otp",
	"ssn",
	"card",
	"cvv",
}

const redactedPlaceholder = "[REDACTED]"

// nonJSONPlaceholder is returned for anything that doesn't parse as JSON —
// bodies we can't walk are never logged raw.
var nonJSONPlaceholder = []byte(`"[NON-JSON BODY SKIPPED]"`)

// RedactJSON walks arbitrary JSON and replaces every value whose key matches
// the redact list with "[REDACTED]". Nested objects and arrays are walked
// recursively. Non-JSON input returns a placeholder instead of the input —
// fail closed, never leak.
func RedactJSON(b []byte) []byte {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nonJSONPlaceholder
	}
	out, err := json.Marshal(redactValue(v))
	if err != nil {
		return nonJSONPlaceholder
	}
	return out
}

func redactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if isSensitiveKey(k) {
				t[k] = redactedPlaceholder
			} else {
				t[k] = redactValue(val)
			}
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = redactValue(val)
		}
		return t
	default:
		return v
	}
}

func isSensitiveKey(key string) bool {
	lk := strings.ToLower(key)
	for _, s := range redactKeys {
		if strings.Contains(lk, s) {
			return true
		}
	}
	return false
}
