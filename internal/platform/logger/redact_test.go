package logger

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestRedactJSON(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // expected JSON (compared structurally, key order ignored)
	}{
		{
			name: "flat object with sensitive keys",
			in:   `{"username":"chubi","password":"hunter2","email":"a@b.c"}`,
			want: `{"username":"chubi","password":"[REDACTED]","email":"a@b.c"}`,
		},
		{
			name: "case-insensitive substring match",
			in:   `{"Password":"x","ACCESS_TOKEN":"jwt","MyApiKey":"k","refreshToken":"r"}`,
			want: `{"Password":"[REDACTED]","ACCESS_TOKEN":"[REDACTED]","MyApiKey":"[REDACTED]","refreshToken":"[REDACTED]"}`,
		},
		{
			name: "nested objects",
			in:   `{"user":{"name":"a","auth":{"otp":"123456","pin":9999}},"note":"ok"}`,
			want: `{"user":{"name":"a","auth":{"otp":"[REDACTED]","pin":"[REDACTED]"}},"note":"ok"}`,
		},
		{
			name: "arrays of objects",
			in:   `{"items":[{"card_number":"4111111111111111","amount":10},{"cvv":"123","amount":20}]}`,
			want: `{"items":[{"card_number":"[REDACTED]","amount":10},{"cvv":"[REDACTED]","amount":20}]}`,
		},
		{
			name: "top-level array",
			in:   `[{"secret":"s"},{"safe":"v"},"plain",42]`,
			want: `[{"secret":"[REDACTED]"},{"safe":"v"},"plain",42]`,
		},
		{
			name: "mixed keys — non-string sensitive values still redacted",
			in:   `{"passwd_hash":"abc","token_count":3,"authorization":null,"ssn":["1","2"]}`,
			want: `{"passwd_hash":"[REDACTED]","token_count":"[REDACTED]","authorization":"[REDACTED]","ssn":"[REDACTED]"}`,
		},
		{
			name: "api_key with separator",
			in:   `{"api_key":"k","api-key-ish":"x"}`,
			want: `{"api_key":"[REDACTED]","api-key-ish":"x"}`,
		},
		{
			name: "no sensitive keys — untouched",
			in:   `{"amount":12.5,"tags":["a","b"],"nested":{"ok":true}}`,
			want: `{"amount":12.5,"tags":["a","b"],"nested":{"ok":true}}`,
		},
		{
			name: "scalar JSON passes through",
			in:   `"just a string"`,
			want: `"just a string"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactJSON([]byte(tt.in))

			var gotV, wantV any
			if err := json.Unmarshal(got, &gotV); err != nil {
				t.Fatalf("output is not valid JSON: %v\noutput: %s", err, got)
			}
			if err := json.Unmarshal([]byte(tt.want), &wantV); err != nil {
				t.Fatalf("bad test expectation: %v", err)
			}
			if !reflect.DeepEqual(gotV, wantV) {
				t.Errorf("RedactJSON mismatch\n got: %s\nwant: %s", got, tt.want)
			}
		})
	}
}

func TestRedactJSONNonJSON(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
	}{
		{"plain text", []byte("this is not json")},
		{"truncated json", []byte(`{"password":"hun`)},
		{"empty input", []byte("")},
		{"nil input", nil},
		{"binary garbage", []byte{0xFF, 0xD8, 0xFF, 0x00}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactJSON(tt.in)
			if string(got) != string(nonJSONPlaceholder) {
				t.Errorf("non-JSON input must return the placeholder, got: %s", got)
			}
		})
	}
}

func TestIsSensitiveKey(t *testing.T) {
	sensitive := []string{"password", "user_password", "PASSWORD", "accessToken", "x-authorization", "ApiKey", "api_key", "card_last4", "CVV2", "otp_code", "PinCode"}
	for _, k := range sensitive {
		if !isSensitiveKey(k) {
			t.Errorf("expected %q to be sensitive", k)
		}
	}
	safe := []string{"username", "email", "amount", "note", "display_name"}
	for _, k := range safe {
		if isSensitiveKey(k) {
			t.Errorf("expected %q to be safe", k)
		}
	}
}
