package logger

import (
	"encoding/json"
	"strings"
)

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

// nonJSONPlaceholder is returned for anything that doesn't parse as JSON — bodies we can't walk
// are never logged raw.
var nonJSONPlaceholder = []byte(`"[NON-JSON BODY SKIPPED]"`)

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
