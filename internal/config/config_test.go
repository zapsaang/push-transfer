package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestConfig_OptionalChannels(t *testing.T) {
	// Empty lookup simulation (no channel env vars set)
	envMap := map[string]string{
		"AUTH_CREDENTIALS": `[{"name":"probe","token":"my-secret-token"}]`,
	}
	lookup := func(k string) string {
		return envMap[k]
	}

	cfg, err := LoadFromLookup(lookup)
	if err != nil {
		t.Fatalf("unexpected error with optional channels missing: %v", err)
	}

	if cfg.Bark != nil {
		t.Errorf("expected Bark channel to be nil when unconfigured, got %+v", cfg.Bark)
	}
	if cfg.Telegram != nil {
		t.Errorf("expected Telegram channel to be nil when unconfigured, got %+v", cfg.Telegram)
	}
	if cfg.Email != nil {
		t.Errorf("expected Email channel to be nil when unconfigured, got %+v", cfg.Email)
	}
	if cfg.IMAP != nil {
		t.Errorf("expected IMAP channel to be nil when unconfigured, got %+v", cfg.IMAP)
	}
	if cfg.Crypto != nil {
		t.Errorf("expected Crypto to be nil when unconfigured, got %+v", cfg.Crypto)
	}

	// Server and Auth defaults
	if cfg.Server.Port != 8889 {
		t.Errorf("expected default port 8889, got %d", cfg.Server.Port)
	}
	if len(cfg.Auth.Credentials) != 1 || cfg.Auth.Credentials[0].Token != "my-secret-token" {
		t.Errorf("expected one credential with token 'my-secret-token', got %+v", cfg.Auth.Credentials)
	}
	if cfg.RunTrigger {
		t.Errorf("expected RunTrigger to be false by default, got true")
	}
}

func TestConfig_RunTrigger(t *testing.T) {
	cases := []struct {
		val      string
		expected bool
	}{
		{"true", true},
		{"TRUE", true},
		{"1", true},
		{"false", false},
		{"0", false},
		{"", false},
		{"invalid", false},
	}

	for _, tc := range cases {
		lookup := func(k string) string {
			if k == "RUN_TRIGGER" {
				return tc.val
			}
			return ""
		}
		cfg, err := LoadFromLookup(lookup)
		if err != nil {
			t.Fatalf("unexpected error for RUN_TRIGGER=%q: %v", tc.val, err)
		}
		if cfg.RunTrigger != tc.expected {
			t.Errorf("for RUN_TRIGGER=%q expected %v, got %v", tc.val, tc.expected, cfg.RunTrigger)
		}
	}
}

func TestConfig_EnabledChannels(t *testing.T) {
	envMap := map[string]string{
		"AUTH_CREDENTIALS":             `[{"name":"admin","username":"admin","password":"password123"}]`,
		"TELEGRAM_BOT_TOKEN":           "123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11",
		"TELEGRAM_CHAT_ID":             "-1001234567890",
		"TELEGRAM_API_URL":             "https://tg-proxy.example.com",
		"BARK_DEVICE_KEY_LIST":         "key1, key2 , key3",
		"BARK_URL_LIST":                "https://bark1.example.com, https://bark2.example.com",
		"BARK_GET_URL":                 "https://custom.bark/get",
		"BARK_POST_URL":                "https://custom.bark/post",
		"AES_128_ECB_KEY":              "1234567890123456",
		"AES_128_ECB_IV":               "6543210987654321",
		"EMAIL_DELIVER_SENDER_EMAIL":   "sender@example.com",
		"EMAIL_DELIVER_RECEIVER_EMAIL": "receiver@example.com",
		"EMAIL_DELIVER_PASSWORD":       "emailpass",
		"EMAIL_DELIVER_SMTP_SERVER":    "smtp.custom.com",
		"EMAIL_DELIVER_SMTP_PORT":      "587",
		"EMAIL_TRIGGER_LOGIN_EMAIL":    "trigger@example.com",
		"EMAIL_TRIGGER_PASSWORD":       "triggerpass",
		"EMAIL_TRIGGER_IMAP_SERVER":    "imap.custom.com",
		"EMAIL_TRIGGER_IMAP_PORT":      "1993",
	}
	lookup := func(k string) string {
		return envMap[k]
	}

	cfg, err := LoadFromLookup(lookup)
	if err != nil {
		t.Fatalf("LoadFromLookup failed: %v", err)
	}

	// Telegram
	if cfg.Telegram == nil {
		t.Fatal("expected Telegram to be non-nil")
	}
	if cfg.Telegram.BaseURL != "https://tg-proxy.example.com" {
		t.Errorf("expected Telegram BaseURL 'https://tg-proxy.example.com', got %q", cfg.Telegram.BaseURL)
	}
	if cfg.Telegram.BotToken != envMap["TELEGRAM_BOT_TOKEN"] {
		t.Errorf("expected Telegram BotToken %q, got %q", envMap["TELEGRAM_BOT_TOKEN"], cfg.Telegram.BotToken)
	}
	if cfg.Telegram.ChatID != -1001234567890 {
		t.Errorf("expected Telegram ChatID -1001234567890, got %d", cfg.Telegram.ChatID)
	}

	// Bark
	if cfg.Bark == nil {
		t.Fatal("expected Bark to be non-nil")
	}
	if len(cfg.Bark.DeviceKeys) != 3 || cfg.Bark.DeviceKeys[1] != "key2" {
		t.Errorf("expected 3 device keys with 'key2', got %v", cfg.Bark.DeviceKeys)
	}
	if len(cfg.Bark.URLs) != 2 || cfg.Bark.URLs[0] != "https://bark1.example.com" {
		t.Errorf("expected 2 URLs, got %v", cfg.Bark.URLs)
	}
	if cfg.Bark.PostURL != "https://custom.bark/post" {
		t.Errorf("expected custom PostURL, got %q", cfg.Bark.PostURL)
	}

	// Crypto
	if cfg.Crypto == nil {
		t.Fatal("expected Crypto to be non-nil")
	}
	if string(cfg.Crypto.Key) != "1234567890123456" {
		t.Errorf("expected Crypto key '1234567890123456', got %q", string(cfg.Crypto.Key))
	}

	// Email
	if cfg.Email == nil {
		t.Fatal("expected Email to be non-nil")
	}
	if cfg.Email.SenderEmail != "sender@example.com" || cfg.Email.SMTPPort != 587 {
		t.Errorf("expected email sender and port 587, got sender=%q port=%d", cfg.Email.SenderEmail, cfg.Email.SMTPPort)
	}

	// IMAP
	if cfg.IMAP == nil {
		t.Fatal("expected IMAP to be non-nil")
	}
	if cfg.IMAP.LoginEmail != "trigger@example.com" || cfg.IMAP.IMAPPort != 1993 {
		t.Errorf("expected imap login and port 1993, got login=%q port=%d", cfg.IMAP.LoginEmail, cfg.IMAP.IMAPPort)
	}
}

func TestConfig_RegexRules(t *testing.T) {
	envMap := map[string]string{
		"REGEX_FILTER_BLACK": `["^垃圾短信", "退订回T"]`,
		"REGEX_FILTER_WHITE": `["10086", "【招商银行】"]`,
		"REGEX_CAPTCHA":      `["验证码[是为：:]\\s*(\\d{4,6})", "(?i)code[\\s:]*(\\d{4,6})"]`,
		"REGEX_EMERGENCY":    `["(?i)urgent", "紧急"]`,
	}
	lookup := func(k string) string {
		return envMap[k]
	}

	cfg, err := LoadFromLookup(lookup)
	if err != nil {
		t.Fatalf("LoadFromLookup failed: %v", err)
	}

	if len(cfg.Rules.FilterBlack) != 2 || cfg.Rules.FilterBlack[0] != "^垃圾短信" {
		t.Errorf("FilterBlack parsed incorrectly: %v", cfg.Rules.FilterBlack)
	}
	if len(cfg.Rules.FilterWhite) != 2 || cfg.Rules.FilterWhite[1] != "【招商银行】" {
		t.Errorf("FilterWhite parsed incorrectly: %v", cfg.Rules.FilterWhite)
	}
	if len(cfg.Rules.Captcha) != 2 || !strings.Contains(cfg.Rules.Captcha[0], `\d{4,6}`) {
		t.Errorf("Captcha rules parsed incorrectly: %v", cfg.Rules.Captcha)
	}
	if len(cfg.Rules.Emergency) != 2 || cfg.Rules.Emergency[1] != "紧急" {
		t.Errorf("Emergency parsed incorrectly: %v", cfg.Rules.Emergency)
	}
}

func TestConfig_Validation(t *testing.T) {
	// Test Default ports
	t.Run("default ports", func(t *testing.T) {
		envMap := map[string]string{
			"EMAIL_DELIVER_SENDER_EMAIL": "a@b.com",
			"EMAIL_TRIGGER_LOGIN_EMAIL":  "c@d.com",
		}
		cfg, err := LoadFromLookup(func(k string) string { return envMap[k] })
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if cfg.Server.Port != 8889 {
			t.Errorf("expected default server port 8889, got %d", cfg.Server.Port)
		}
		if cfg.Email == nil || cfg.Email.SMTPPort != 465 {
			t.Errorf("expected default SMTP port 465, got %+v", cfg.Email)
		}
		if cfg.IMAP == nil || cfg.IMAP.IMAPPort != 993 {
			t.Errorf("expected default IMAP port 993, got %+v", cfg.IMAP)
		}
	})

	// Test invalid server port
	t.Run("invalid server port", func(t *testing.T) {
		envMap := map[string]string{
			"PORT": "invalid-port",
		}
		_, err := LoadFromLookup(func(k string) string { return envMap[k] })
		if err == nil {
			t.Fatal("expected error for invalid PORT, got nil")
		}
	})

	// Test invalid Telegram chat ID
	t.Run("invalid telegram chat id", func(t *testing.T) {
		envMap := map[string]string{
			"TELEGRAM_BOT_TOKEN": "bot123",
			"TELEGRAM_CHAT_ID":   "not-an-int",
		}
		_, err := LoadFromLookup(func(k string) string { return envMap[k] })
		if err == nil {
			t.Fatal("expected error for invalid TELEGRAM_CHAT_ID, got nil")
		}
	})

	// Test invalid regex JSON
	t.Run("invalid regex json array", func(t *testing.T) {
		envMap := map[string]string{
			"REGEX_CAPTCHA": `not-a-json-array`,
		}
		_, err := LoadFromLookup(func(k string) string { return envMap[k] })
		if err == nil {
			t.Fatal("expected error for invalid REGEX_CAPTCHA JSON, got nil")
		}
	})

	// Test invalid AES key length
	t.Run("invalid AES key length", func(t *testing.T) {
		envMap := map[string]string{
			"AES_128_ECB_KEY": "too-short",
		}
		_, err := LoadFromLookup(func(k string) string { return envMap[k] })
		if err == nil {
			t.Fatal("expected error for short AES_128_ECB_KEY, got nil")
		}
	})

	// Bark uses the raw UTF-8 bytes of the configured key, and its app validates the
	// key by *character* count, so a multi-byte 16-byte key is unusable in practice.
	t.Run("AES key with 16 bytes but fewer than 16 characters is rejected", func(t *testing.T) {
		envMap := map[string]string{
			"AES_128_ECB_KEY": "密钥密钥密钥密钥密钥", // 30 bytes / 10 characters
		}
		if _, err := LoadFromLookup(func(k string) string { return envMap[k] }); err == nil {
			t.Fatal("expected error for non-ASCII AES_128_ECB_KEY, got nil")
		}

		envMap["AES_128_ECB_KEY"] = strings.Repeat("é", 8) // 16 bytes / 8 characters
		if _, err := LoadFromLookup(func(k string) string { return envMap[k] }); err == nil {
			t.Fatal("expected error for multi-byte AES_128_ECB_KEY, got nil")
		}

		// A 16-character ASCII key is the supported form.
		envMap["AES_128_ECB_KEY"] = "1234567890123456"
		cfg, err := LoadFromLookup(func(k string) string { return envMap[k] })
		if err != nil {
			t.Fatalf("unexpected error for valid AES key: %v", err)
		}
		if cfg.Crypto == nil || string(cfg.Crypto.Key) != "1234567890123456" {
			t.Fatalf("expected crypto key to be parsed, got %+v", cfg.Crypto)
		}
	})

	// Test empty item in regex list
	t.Run("empty item in regex array", func(t *testing.T) {
		envMap := map[string]string{
			"REGEX_FILTER_WHITE": `["valid", "   "]`,
		}
		_, err := LoadFromLookup(func(k string) string { return envMap[k] })
		if err == nil {
			t.Fatal("expected error for empty item in regex list, got nil")
		}
	})

	// Test invalid SMTP port
	t.Run("invalid SMTP port", func(t *testing.T) {
		envMap := map[string]string{
			"EMAIL_DELIVER_SENDER_EMAIL": "a@b.com",
			"EMAIL_DELIVER_SMTP_PORT":    "not-a-port",
		}
		_, err := LoadFromLookup(func(k string) string { return envMap[k] })
		if err == nil {
			t.Fatal("expected error for invalid SMTP port, got nil")
		}
	})

	// Test invalid IMAP port
	t.Run("invalid IMAP port", func(t *testing.T) {
		envMap := map[string]string{
			"EMAIL_TRIGGER_LOGIN_EMAIL": "a@b.com",
			"EMAIL_TRIGGER_IMAP_PORT":   "not-a-port",
		}
		_, err := LoadFromLookup(func(k string) string { return envMap[k] })
		if err == nil {
			t.Fatal("expected error for invalid IMAP port, got nil")
		}
	})

	// Test Load() with os.Getenv
	t.Run("Load with os.Getenv", func(t *testing.T) {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		if cfg == nil {
			t.Fatal("expected non-nil config from Load()")
		}
	})
}

func TestConfig_HasWebhookAuth(t *testing.T) {
	t.Run("empty auth", func(t *testing.T) {
		if (AuthConfig{}).HasWebhookAuth() {
			t.Fatal("expected HasWebhookAuth to be false for empty AuthConfig")
		}
	})

	t.Run("nil credentials", func(t *testing.T) {
		if (AuthConfig{Credentials: nil}).HasWebhookAuth() {
			t.Fatal("expected HasWebhookAuth to be false for nil credentials")
		}
	})

	t.Run("empty credentials slice", func(t *testing.T) {
		if (AuthConfig{Credentials: []Credential{}}).HasWebhookAuth() {
			t.Fatal("expected HasWebhookAuth to be false for an empty credentials slice")
		}
	})

	t.Run("token credential", func(t *testing.T) {
		a := AuthConfig{Credentials: []Credential{{Name: "c1", Token: "tok1"}}}
		if !a.HasWebhookAuth() {
			t.Fatal("expected HasWebhookAuth to be true for a token credential")
		}
	})

	t.Run("basic credential", func(t *testing.T) {
		a := AuthConfig{Credentials: []Credential{{Name: "c1", Username: "user", Password: "pass"}}}
		if !a.HasWebhookAuth() {
			t.Fatal("expected HasWebhookAuth to be true for a basic credential")
		}
	})

	t.Run("dual credential", func(t *testing.T) {
		a := AuthConfig{Credentials: []Credential{{Name: "c1", Token: "tok1", Username: "user", Password: "pass"}}}
		if !a.HasWebhookAuth() {
			t.Fatal("expected HasWebhookAuth to be true for a dual credential")
		}
	})
}

func TestConfig_AuthCredentialsParse(t *testing.T) {
	t.Run("valid multi-entry with defaults and whitespace trimming", func(t *testing.T) {
		input := `[
			{"name":"phone-a","token":"  tok-a  "},
			{"token":"tok-b"},
			{"name":"custom-basic","username":"  usr-c  ","password":"  pwd-c  "}
		]`
		envMap := map[string]string{
			"AUTH_CREDENTIALS": input,
		}
		cfg, err := LoadFromLookup(func(k string) string { return envMap[k] })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(cfg.Auth.Credentials) != 3 {
			t.Fatalf("expected 3 credentials, got %d", len(cfg.Auth.Credentials))
		}

		expected := []Credential{
			{Name: "phone-a", Token: "tok-a"},
			{Name: "credential-2", Token: "tok-b"},
			{Name: "custom-basic", Username: "usr-c", Password: "pwd-c"},
		}
		for i, exp := range expected {
			got := cfg.Auth.Credentials[i]
			if got != exp {
				t.Errorf("[%d] expected %+v, got %+v", i, exp, got)
			}
		}
	})

	t.Run("blank AUTH_CREDENTIALS results in empty credentials and nil err", func(t *testing.T) {
		for _, raw := range []string{"", "   ", "\t\n"} {
			envMap := map[string]string{
				"AUTH_CREDENTIALS": raw,
			}
			cfg, err := LoadFromLookup(func(k string) string { return envMap[k] })
			if err != nil {
				t.Fatalf("raw %q: unexpected error: %v", raw, err)
			}
			if len(cfg.Auth.Credentials) != 0 {
				t.Fatalf("raw %q: expected empty credentials, got %v", raw, cfg.Auth.Credentials)
			}
		}
	})

	t.Run("single element without name gets default name credential-1", func(t *testing.T) {
		envMap := map[string]string{
			"AUTH_CREDENTIALS": `[{"token":"single-token"}]`,
		}
		cfg, err := LoadFromLookup(func(k string) string { return envMap[k] })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(cfg.Auth.Credentials) != 1 {
			t.Fatalf("expected 1 credential, got %d", len(cfg.Auth.Credentials))
		}
		expected := Credential{Name: "credential-1", Token: "single-token"}
		if cfg.Auth.Credentials[0] != expected {
			t.Fatalf("expected %+v, got %+v", expected, cfg.Auth.Credentials[0])
		}
	})
}

func TestConfig_AuthCredentialsValidation(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		errContains string
	}{
		// V2: Malformed JSON / Element not object / Trailing garbage / Unknown field
		{
			name:        "V2_malformed_json",
			input:       `{not-valid-json`,
			errContains: "must be a JSON array of credential objects",
		},
		{
			name:        "V2_element_not_object",
			input:       `["string-not-object"]`,
			errContains: "must be a JSON array of credential objects",
		},
		{
			name:        "V2_unknown_field_spelling",
			input:       `[{"tokens":"secret"}]`,
			errContains: "unknown field \"tokens\"",
		},
		{
			name:        "V2_trailing_garbage",
			input:       `[{"token":"secret"}] trailing_data`,
			errContains: "trailing data",
		},

		// V3: Empty array or null
		{
			name:        "V3_empty_array",
			input:       `[]`,
			errContains: "must contain at least one credential",
		},
		{
			name:        "V3_null",
			input:       `null`,
			errContains: "must contain at least one credential",
		},

		// V4: Entry has no material
		{
			name:        "V4_no_material",
			input:       `[{"name":"only-name"}]`,
			errContains: "entry 1 must set either \"token\" or both \"username\" and \"password\"",
		},
		{
			name:        "V4_second_entry_no_material",
			input:       `[{"token":"tok1"}, {"name":"second-only-name"}]`,
			errContains: "entry 2 must set either \"token\" or both \"username\" and \"password\"",
		},

		// V5: Keys present but empty or blank
		{
			name:        "V5_token_blank",
			input:       `[{"token":"   "}]`,
			errContains: "entry 1: \"token\" must not be blank",
		},
		{
			name:        "V5_name_blank",
			input:       `[{"name":"   ","token":"my-token"}]`,
			errContains: "entry 1: \"name\" must not be blank",
		},
		{
			name:        "V5_username_blank",
			input:       `[{"username":"   ","password":"p"}]`,
			errContains: "entry 1: \"username\" must not be blank",
		},
		{
			name:        "V5_password_blank",
			input:       `[{"username":"u","password":"   "}]`,
			errContains: "entry 1: \"password\" must not be blank",
		},

		// V6: Basic auth one-sided
		{
			name:        "V6_username_only",
			input:       `[{"username":"u"}]`,
			errContains: "entry 1: \"username\" and \"password\" must be provided together",
		},
		{
			name:        "V6_password_only",
			input:       `[{"password":"p"}]`,
			errContains: "entry 1: \"username\" and \"password\" must be provided together",
		},

		// V7: Duplicate name (explicit and generated)
		{
			name:        "V7_duplicate_explicit_name",
			input:       `[{"name":"phone","token":"tok1"},{"name":"phone","token":"tok2"}]`,
			errContains: "entry 2: duplicate credential name \"phone\"",
		},
		{
			name:        "V7_duplicate_generated_name_collision",
			input:       `[{"token":"tok1"},{"name":"credential-1","token":"tok2"}]`,
			errContains: "entry 2: duplicate credential name \"credential-1\"",
		},

		// V8: Name exceeds 64 characters
		{
			name:        "V8_name_exceeds_64_runes",
			input:       `[{"name":"` + strings.Repeat("a", 65) + `","token":"tok1"}]`,
			errContains: "entry 1: \"name\" must not exceed 64 characters",
		},

		// V9: Name starts with reserved prefix env:
		{
			name:        "V9_name_reserved_prefix",
			input:       `[{"name":"env:custom","token":"tok1"}]`,
			errContains: "entry 1: \"name\" must not start with reserved prefix \"env:\"",
		},

		// V10: More than 64 entries
		{
			name: "V10_exceeds_64_entries",
			input: func() string {
				var items []string
				for i := 1; i <= 65; i++ {
					items = append(items, fmt.Sprintf(`{"token":"tok-%d"}`, i))
				}
				return "[" + strings.Join(items, ",") + "]"
			}(),
			errContains: "must not contain more than 64 credentials",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envMap := map[string]string{
				"AUTH_CREDENTIALS": tc.input,
			}
			cfg, err := LoadFromLookup(func(k string) string { return envMap[k] })
			if err == nil {
				t.Fatalf("expected error containing %q, got nil error and cfg=%+v", tc.errContains, cfg)
			}
			if !strings.Contains(err.Error(), tc.errContains) {
				t.Errorf("expected error containing %q, got %q", tc.errContains, err.Error())
			}
		})
	}
}

// TestConfig_AuthCredentialsErrorNeverLeaksSecretMaterial is a permanent negative regression
// guard: AUTH_CREDENTIALS holds plaintext tokens/passwords, and the startup failure path
// (config error -> slog.Error in main) must never echo the raw value.
func TestConfig_AuthCredentialsErrorNeverLeaksSecretMaterial(t *testing.T) {
	const secret = "s3cr3t-token-value"

	cases := []struct {
		name  string
		input string
	}{
		{"trailing garbage", `[{"token":"` + secret + `"}] oops`},
		{"malformed json", `{"token":"` + secret + `"`},
		{"truncated array", `[{"token":"` + secret + `"},`},
		{"unknown field", `[{"tokn":"` + secret + `"}]`},
		{"secret in password", `[{"username":"u","password":"` + secret + `"}] trailing`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseCredentials(tc.input)
			if err == nil {
				t.Fatalf("expected an error for input %q", tc.input)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error message leaks credential material: %v", err)
			}
		})
	}
}

// TestConfig_AuthCredentialsBoundaries locks the inclusive/exclusive edges of V7-V10 and the
// JSON decoding contracts (case-insensitive keys, explicit null == absent).
func TestConfig_AuthCredentialsBoundaries(t *testing.T) {
	t.Run("exactly 64 entries is accepted", func(t *testing.T) {
		items := make([]string, 0, 64)
		for i := 1; i <= 64; i++ {
			items = append(items, fmt.Sprintf(`{"token":"tok-%d"}`, i))
		}
		cfg, err := LoadFromLookup(func(k string) string {
			if k == "AUTH_CREDENTIALS" {
				return "[" + strings.Join(items, ",") + "]"
			}
			return ""
		})
		if err != nil {
			t.Fatalf("expected 64 entries to be accepted, got: %v", err)
		}
		if len(cfg.Auth.Credentials) != 64 {
			t.Fatalf("expected 64 credentials, got %d", len(cfg.Auth.Credentials))
		}
	})

	t.Run("name of exactly 64 runes is accepted", func(t *testing.T) {
		name := strings.Repeat("é", 64) // multi-byte runes: byte length is 128
		cfg, err := LoadFromLookup(func(k string) string {
			if k == "AUTH_CREDENTIALS" {
				return `[{"name":"` + name + `","token":"tok"}]`
			}
			return ""
		})
		if err != nil {
			t.Fatalf("expected 64-rune name to be accepted, got: %v", err)
		}
		if cfg.Auth.Credentials[0].Name != name {
			t.Fatalf("expected name %q, got %q", name, cfg.Auth.Credentials[0].Name)
		}
	})

	t.Run("reserved prefix only rejected at the start of the name", func(t *testing.T) {
		cfg, err := LoadFromLookup(func(k string) string {
			if k == "AUTH_CREDENTIALS" {
				return `[{"name":"myenv:device","token":"tok"}]`
			}
			return ""
		})
		if err != nil {
			t.Fatalf("expected 'myenv:device' to be accepted, got: %v", err)
		}
		if cfg.Auth.Credentials[0].Name != "myenv:device" {
			t.Fatalf("unexpected name: %q", cfg.Auth.Credentials[0].Name)
		}
	})

	t.Run("json field names are matched case-insensitively", func(t *testing.T) {
		cfg, err := LoadFromLookup(func(k string) string {
			if k == "AUTH_CREDENTIALS" {
				return `[{"NAME":"upper","TOKEN":"tok"}]`
			}
			return ""
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := Credential{Name: "upper", Token: "tok"}
		if cfg.Auth.Credentials[0] != expected {
			t.Fatalf("expected %+v, got %+v", expected, cfg.Auth.Credentials[0])
		}
	})

	t.Run("explicit null token is treated as absent", func(t *testing.T) {
		_, err := parseCredentials(`[{"token":null}]`)
		if err == nil {
			t.Fatal("expected V4 error when token is null and no basic pair is present")
		}
		if !strings.Contains(err.Error(), `entry 1 must set either "token" or both "username" and "password"`) {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("explicit null token with a full basic pair is accepted", func(t *testing.T) {
		cfg, err := LoadFromLookup(func(k string) string {
			if k == "AUTH_CREDENTIALS" {
				return `[{"token":null,"username":"u","password":"p"}]`
			}
			return ""
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := Credential{Name: "credential-1", Username: "u", Password: "p"}
		if cfg.Auth.Credentials[0] != expected {
			t.Fatalf("expected %+v, got %+v", expected, cfg.Auth.Credentials[0])
		}
	})
}

// TestConfig_AuthCredentialsRejectsDuplicateFactors pins the strict-validation replacement for
// the removed legacy/union dedup: with a single credential source there is no reason to accept
// two entries that declare the same authentication factor, because only one of them can ever be
// the audit identity of a matching request. Rejecting at startup is fail-fast and unambiguous.
func TestConfig_AuthCredentialsRejectsDuplicateFactors(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		errContains string
	}{
		{
			name:        "duplicate token across two entries",
			input:       `[{"name":"a","token":"shared-token"},{"name":"b","token":"shared-token"}]`,
			errContains: `entry 2: duplicate "token" already used by entry 1`,
		},
		{
			name:        "duplicate basic pair across two entries",
			input:       `[{"name":"a","username":"u","password":"p"},{"name":"b","username":"u","password":"p"}]`,
			errContains: `entry 2: duplicate "username"/"password" pair already used by entry 1`,
		},
		{
			name:        "duplicate token between a token-only and a dual entry",
			input:       `[{"name":"a","token":"t"},{"name":"b","token":"t","username":"u","password":"p"}]`,
			errContains: `entry 2: duplicate "token" already used by entry 1`,
		},
		{
			name:        "duplicate basic pair between a basic-only and a dual entry",
			input:       `[{"name":"a","username":"u","password":"p"},{"name":"b","token":"t","username":"u","password":"p"}]`,
			errContains: `entry 2: duplicate "username"/"password" pair already used by entry 1`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseCredentials(tc.input)
			if err == nil {
				t.Fatalf("expected an error for %s", tc.input)
			}
			if !strings.Contains(err.Error(), tc.errContains) {
				t.Errorf("expected error containing %q, got %q", tc.errContains, err.Error())
			}
		})
	}
}

// TestConfig_AuthCredentialsAllowsDistinctFactors is the negative half of the rule above: only an
// identical factor is rejected, and the error must not fire for merely similar credentials.
func TestConfig_AuthCredentialsAllowsDistinctFactors(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{
			name:  "same username with a different password",
			input: `[{"name":"a","username":"u","password":"p1"},{"name":"b","username":"u","password":"p2"}]`,
		},
		{
			name:  "same password with a different username",
			input: `[{"name":"a","username":"u1","password":"p"},{"name":"b","username":"u2","password":"p"}]`,
		},
		{
			name:  "different tokens",
			input: `[{"name":"a","token":"t1"},{"name":"b","token":"t2"}]`,
		},
		{
			name:  "token that equals another entry's username",
			input: `[{"name":"a","token":"same-value"},{"name":"b","username":"same-value","password":"p"}]`,
		},
		{
			name:  "token and basic pair on the same entry",
			input: `[{"name":"a","token":"t","username":"u","password":"p"}]`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseCredentials(tc.input); err != nil {
				t.Fatalf("expected no error for %s, got: %v", tc.input, err)
			}
		})
	}
}

func TestConfig_BarkGetURLFallbackToPostURL(t *testing.T) {
	t.Run("derives PostURL from BARK_GET_URL when BARK_POST_URL is unset", func(t *testing.T) {
		envMap := map[string]string{
			"BARK_DEVICE_KEY_LIST": "dev1",
			"BARK_GET_URL":         "https://bark.selfhosted.io/",
		}
		cfg, err := LoadFromLookup(func(k string) string { return envMap[k] })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Bark == nil {
			t.Fatal("expected non-nil BarkConfig")
		}
		if cfg.Bark.PostURL != "https://bark.selfhosted.io/push" {
			t.Fatalf("expected derived PostURL 'https://bark.selfhosted.io/push', got %q", cfg.Bark.PostURL)
		}
	})

	t.Run("prioritizes BARK_POST_URL over BARK_GET_URL", func(t *testing.T) {
		envMap := map[string]string{
			"BARK_DEVICE_KEY_LIST": "dev1",
			"BARK_GET_URL":         "https://bark.selfhosted.io/",
			"BARK_POST_URL":        "https://bark.selfhosted.io/custom-push",
		}
		cfg, err := LoadFromLookup(func(k string) string { return envMap[k] })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Bark.PostURL != "https://bark.selfhosted.io/custom-push" {
			t.Fatalf("expected PostURL 'https://bark.selfhosted.io/custom-push', got %q", cfg.Bark.PostURL)
		}
	})

	t.Run("defaults to api.day.app/push when both unset", func(t *testing.T) {
		envMap := map[string]string{
			"BARK_DEVICE_KEY_LIST": "dev1",
		}
		cfg, err := LoadFromLookup(func(k string) string { return envMap[k] })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Bark.PostURL != "https://api.day.app/push" {
			t.Fatalf("expected default PostURL 'https://api.day.app/push', got %q", cfg.Bark.PostURL)
		}
	})

	t.Run("does not duplicate /push when BARK_GET_URL already ends with /push", func(t *testing.T) {
		envMap := map[string]string{
			"BARK_DEVICE_KEY_LIST": "dev1",
			"BARK_GET_URL":         "https://bark.selfhosted.io/push/",
		}
		cfg, err := LoadFromLookup(func(k string) string { return envMap[k] })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Bark.PostURL != "https://bark.selfhosted.io/push" {
			t.Fatalf("expected PostURL 'https://bark.selfhosted.io/push', got %q", cfg.Bark.PostURL)
		}
	})
}

func TestConfig_TrustedProxies(t *testing.T) {
	t.Run("loads TRUSTED_PROXIES comma-separated list into ServerConfig", func(t *testing.T) {
		envMap := map[string]string{
			"TRUSTED_PROXIES": " 198.51.100.0/24 , 203.0.113.10 , ",
		}
		cfg, err := LoadFromLookup(func(k string) string { return envMap[k] })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(cfg.Server.TrustedProxies) != 2 || cfg.Server.TrustedProxies[0] != "198.51.100.0/24" || cfg.Server.TrustedProxies[1] != "203.0.113.10" {
			t.Fatalf("expected 2 trusted proxies, got %v", cfg.Server.TrustedProxies)
		}
	})

	t.Run("falls back to WEBHOOK_TRUSTED_PROXIES", func(t *testing.T) {
		envMap := map[string]string{
			"WEBHOOK_TRUSTED_PROXIES": "10.10.0.0/16",
		}
		cfg, err := LoadFromLookup(func(k string) string { return envMap[k] })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(cfg.Server.TrustedProxies) != 1 || cfg.Server.TrustedProxies[0] != "10.10.0.0/16" {
			t.Fatalf("expected [10.10.0.0/16], got %v", cfg.Server.TrustedProxies)
		}
	})
}

func TestConfig_WhitespaceHandling(t *testing.T) {
	t.Run("whitespace-only optional channel env vars remain nil", func(t *testing.T) {
		envMap := map[string]string{
			"TELEGRAM_BOT_TOKEN":         "   ",
			"TELEGRAM_CHAT_ID":           " \t ",
			"EMAIL_DELIVER_SENDER_EMAIL": "   ",
			"EMAIL_TRIGGER_LOGIN_EMAIL":  "   ",
			"AES_128_ECB_KEY":            "   ",
		}
		cfg, err := LoadFromLookup(func(k string) string { return envMap[k] })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Telegram != nil {
			t.Errorf("expected nil Telegram on whitespace-only env, got %+v", cfg.Telegram)
		}
		if cfg.Email != nil {
			t.Errorf("expected nil Email on whitespace-only env, got %+v", cfg.Email)
		}
		if cfg.IMAP != nil {
			t.Errorf("expected nil IMAP on whitespace-only env, got %+v", cfg.IMAP)
		}
		if cfg.Crypto != nil {
			t.Errorf("expected nil Crypto on whitespace-only env, got %+v", cfg.Crypto)
		}
	})

	t.Run("padded TELEGRAM_CHAT_ID and AUTH_CREDENTIALS are trimmed cleanly", func(t *testing.T) {
		envMap := map[string]string{
			"AUTH_CREDENTIALS":   `  [ {"name":"  padded  ","token":"  my-token-123 "} ]  `,
			"TELEGRAM_BOT_TOKEN": " 123:abc ",
			"TELEGRAM_CHAT_ID":   " -100987654 ",
		}
		cfg, err := LoadFromLookup(func(k string) string { return envMap[k] })
		if err != nil {
			t.Fatalf("expected padded TELEGRAM_CHAT_ID to parse, got error: %v", err)
		}
		if len(cfg.Auth.Credentials) != 1 {
			t.Fatalf("expected 1 credential, got %+v", cfg.Auth.Credentials)
		}
		expected := Credential{Name: "padded", Token: "my-token-123"}
		if cfg.Auth.Credentials[0] != expected {
			t.Errorf("expected trimmed credential %+v, got %+v", expected, cfg.Auth.Credentials[0])
		}
		if cfg.Telegram == nil || cfg.Telegram.ChatID != -100987654 || cfg.Telegram.BotToken != "123:abc" {
			t.Errorf("unexpected Telegram config: %+v", cfg.Telegram)
		}
	})
}

func TestConfig_DeprecatedIVFlag(t *testing.T) {
	t.Run("sets DeprecatedIVConfigured when AES_128_ECB_IV is set with key", func(t *testing.T) {
		envMap := map[string]string{
			"AES_128_ECB_KEY": "1234567890123456",
			"AES_128_ECB_IV":  "6543210987654321",
		}
		cfg, err := LoadFromLookup(func(k string) string { return envMap[k] })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Crypto == nil || !cfg.Crypto.DeprecatedIVConfigured {
			t.Fatalf("expected DeprecatedIVConfigured to be true, got %+v", cfg.Crypto)
		}
	})

	t.Run("sets DeprecatedIVConfigured when only AES_128_ECB_IV is set", func(t *testing.T) {
		envMap := map[string]string{
			"AES_128_ECB_IV": "6543210987654321",
		}
		cfg, err := LoadFromLookup(func(k string) string { return envMap[k] })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Crypto == nil || !cfg.Crypto.DeprecatedIVConfigured {
			t.Fatalf("expected DeprecatedIVConfigured to be true when only IV is set, got %+v", cfg.Crypto)
		}
	})

	t.Run("DeprecatedIVConfigured is false when AES_128_ECB_IV is empty", func(t *testing.T) {
		envMap := map[string]string{
			"AES_128_ECB_KEY": "1234567890123456",
		}
		cfg, err := LoadFromLookup(func(k string) string { return envMap[k] })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Crypto == nil || cfg.Crypto.DeprecatedIVConfigured {
			t.Fatalf("expected DeprecatedIVConfigured to be false, got %+v", cfg.Crypto)
		}
	})
}

func TestConfig_IMAPMailbox(t *testing.T) {
	t.Run("defaults to INBOX when EMAIL_TRIGGER_MAILBOX is unset", func(t *testing.T) {
		envMap := map[string]string{
			"EMAIL_TRIGGER_LOGIN_EMAIL": "user@example.com",
			"EMAIL_TRIGGER_PASSWORD":    "pass",
		}
		cfg, err := LoadFromLookup(func(k string) string { return envMap[k] })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.IMAP == nil || cfg.IMAP.Mailbox != "INBOX" {
			t.Fatalf("expected default Mailbox 'INBOX', got %+v", cfg.IMAP)
		}
	})

	t.Run("loads custom EMAIL_TRIGGER_MAILBOX", func(t *testing.T) {
		envMap := map[string]string{
			"EMAIL_TRIGGER_LOGIN_EMAIL": "user@example.com",
			"EMAIL_TRIGGER_PASSWORD":    "pass",
			"EMAIL_TRIGGER_MAILBOX":     "PushTransfer",
		}
		cfg, err := LoadFromLookup(func(k string) string { return envMap[k] })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.IMAP == nil || cfg.IMAP.Mailbox != "PushTransfer" {
			t.Fatalf("expected Mailbox 'PushTransfer', got %+v", cfg.IMAP)
		}
	})
}

func TestConfig_PartialChannelHelpers(t *testing.T) {
	t.Run("TelegramConfig helpers", func(t *testing.T) {
		var nilTG *TelegramConfig
		if nilTG.Enabled() || nilTG.HasPartialConfig() || nilTG.IsPartial() {
			t.Fatal("expected false for nil TelegramConfig")
		}

		partialTokenOnly := &TelegramConfig{BotToken: "token"}
		if partialTokenOnly.Enabled() || !partialTokenOnly.HasPartialConfig() || !partialTokenOnly.IsPartial() {
			t.Fatal("expected partial config when only BotToken is set")
		}

		partialChatOnly := &TelegramConfig{ChatID: 12345}
		if partialChatOnly.Enabled() || !partialChatOnly.HasPartialConfig() {
			t.Fatal("expected partial config when only ChatID is set")
		}

		fullTG := &TelegramConfig{BotToken: "token", ChatID: 12345}
		if !fullTG.Enabled() || fullTG.HasPartialConfig() || fullTG.IsPartial() {
			t.Fatal("expected Enabled=true and Partial=false for complete TelegramConfig")
		}
	})

	t.Run("EmailConfig helpers", func(t *testing.T) {
		var nilEmail *EmailConfig
		if nilEmail.Enabled() || nilEmail.HasPartialConfig() || nilEmail.IsPartial() {
			t.Fatal("expected false for nil EmailConfig")
		}

		partialSenderOnly := &EmailConfig{SenderEmail: "a@b.com"}
		if partialSenderOnly.Enabled() || !partialSenderOnly.HasPartialConfig() || !partialSenderOnly.IsPartial() {
			t.Fatal("expected partial config when only SenderEmail is set")
		}

		partialReceiverOnly := &EmailConfig{ReceiverEmail: "c@d.com"}
		if partialReceiverOnly.Enabled() || !partialReceiverOnly.HasPartialConfig() {
			t.Fatal("expected partial config when only ReceiverEmail is set")
		}

		fullEmail := &EmailConfig{SenderEmail: "a@b.com", ReceiverEmail: "c@d.com"}
		if !fullEmail.Enabled() || fullEmail.HasPartialConfig() || fullEmail.IsPartial() {
			t.Fatal("expected Enabled=true and Partial=false for complete EmailConfig")
		}
	})

	t.Run("IMAPConfig helpers", func(t *testing.T) {
		var nilIMAP *IMAPConfig
		if nilIMAP.Enabled() || nilIMAP.HasPartialConfig() || nilIMAP.IsPartial() {
			t.Fatal("expected false for nil IMAPConfig")
		}

		partialLoginOnly := &IMAPConfig{LoginEmail: "user@example.com"}
		if partialLoginOnly.Enabled() || !partialLoginOnly.HasPartialConfig() || !partialLoginOnly.IsPartial() {
			t.Fatal("expected partial config when only LoginEmail is set")
		}

		partialPassOnly := &IMAPConfig{Password: "secret"}
		if partialPassOnly.Enabled() || !partialPassOnly.HasPartialConfig() {
			t.Fatal("expected partial config when only Password is set")
		}

		fullIMAP := &IMAPConfig{LoginEmail: "user@example.com", Password: "secret"}
		if !fullIMAP.Enabled() || fullIMAP.HasPartialConfig() || fullIMAP.IsPartial() {
			t.Fatal("expected Enabled=true and Partial=false for complete IMAPConfig")
		}
	})

	t.Run("AuthConfig HasWebhookAuth edge cases", func(t *testing.T) {
		if (AuthConfig{}).HasWebhookAuth() {
			t.Fatal("expected HasWebhookAuth=false for empty AuthConfig")
		}

		// Whitespace-only material never survives parseCredentials, but the predicate must not
		// be fooled by a hand-built AuthConfig either: it counts entries, not raw characters.
		wsAuth := AuthConfig{Credentials: []Credential{{Name: "ws", Token: "  "}}}
		if !wsAuth.HasWebhookAuth() {
			t.Fatal("expected HasWebhookAuth to count the entry; parseCredentials is what rejects blanks")
		}

		fullBasic := AuthConfig{Credentials: []Credential{{Name: "basic", Username: "admin", Password: "secret"}}}
		if !fullBasic.HasWebhookAuth() {
			t.Fatal("expected complete Basic credential to count")
		}

		tokenAuth := AuthConfig{Credentials: []Credential{{Name: "tok", Token: "my-token"}}}
		if !tokenAuth.HasWebhookAuth() {
			t.Fatal("expected complete Token credential to count")
		}
	})
}

func TestConfig_StandaloneEnvVars_TriggerPartialConfig(t *testing.T) {
	t.Run("TELEGRAM_API_URL alone triggers partial config", func(t *testing.T) {
		cfg, err := LoadFromLookup(func(k string) string {
			if k == "TELEGRAM_API_URL" {
				return "https://custom-tg.example.com"
			}
			return ""
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Telegram == nil {
			t.Fatal("expected cfg.Telegram to be instantiated when TELEGRAM_API_URL is set")
		}
		if !cfg.Telegram.HasPartialConfig() {
			t.Fatal("expected cfg.Telegram.HasPartialConfig() == true when only TELEGRAM_API_URL is set")
		}
	})

	t.Run("EMAIL_DELIVER_SMTP_SERVER alone triggers partial config", func(t *testing.T) {
		cfg, err := LoadFromLookup(func(k string) string {
			if k == "EMAIL_DELIVER_SMTP_SERVER" {
				return "smtp.mycustomserver.com"
			}
			return ""
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Email == nil {
			t.Fatal("expected cfg.Email to be instantiated when EMAIL_DELIVER_SMTP_SERVER is set")
		}
		if !cfg.Email.HasPartialConfig() {
			t.Fatal("expected cfg.Email.HasPartialConfig() == true when only EMAIL_DELIVER_SMTP_SERVER is set")
		}
	})

	t.Run("EMAIL_TRIGGER_MAILBOX alone triggers partial config", func(t *testing.T) {
		cfg, err := LoadFromLookup(func(k string) string {
			if k == "EMAIL_TRIGGER_MAILBOX" {
				return "PushTransferMailbox"
			}
			return ""
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.IMAP == nil {
			t.Fatal("expected cfg.IMAP to be instantiated when EMAIL_TRIGGER_MAILBOX is set")
		}
		if !cfg.IMAP.HasPartialConfig() {
			t.Fatal("expected cfg.IMAP.HasPartialConfig() == true when only EMAIL_TRIGGER_MAILBOX is set")
		}
	})
}
