// Package config provides configuration parsing and environment variable loading for push-transfer.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ServerConfig contains HTTP server configuration.
type ServerConfig struct {
	Port           int
	TrustedProxies []string
}

// AuthConfig contains authentication credentials for webhooks and APIs.
type AuthConfig struct {
	Username string
	Password string
	Token    string
}

// HasTokenAuth reports whether non-empty Bearer token authentication is configured.
func (a AuthConfig) HasTokenAuth() bool {
	return strings.TrimSpace(a.Token) != ""
}

// HasBasicAuth reports whether both username and password for HTTP Basic Auth are configured.
func (a AuthConfig) HasBasicAuth() bool {
	return strings.TrimSpace(a.Username) != "" && strings.TrimSpace(a.Password) != ""
}

// HasPartialBasicAuth reports whether exactly one of username or password is set.
func (a AuthConfig) HasPartialBasicAuth() bool {
	return (strings.TrimSpace(a.Username) != "") != (strings.TrimSpace(a.Password) != "")
}

// HasWebhookAuth reports whether either Token or Basic authentication is configured.
func (a AuthConfig) HasWebhookAuth() bool {
	return a.HasTokenAuth() || a.HasBasicAuth()
}

// TelegramConfig contains credentials and endpoint for Telegram message dispatching.
type TelegramConfig struct {
	BaseURL  string
	BotToken string
	ChatID   int64
}

// Enabled reports whether TelegramConfig has both BotToken and ChatID configured.
func (c *TelegramConfig) Enabled() bool {
	return c != nil && strings.TrimSpace(c.BotToken) != "" && c.ChatID != 0
}

// HasPartialConfig reports whether TelegramConfig is non-nil but missing required fields.
func (c *TelegramConfig) HasPartialConfig() bool {
	if c == nil || c.Enabled() {
		return false
	}
	return strings.TrimSpace(c.BotToken) != "" || c.ChatID != 0 || strings.TrimSpace(c.BaseURL) != ""
}

// IsPartial is an alias for HasPartialConfig.
func (c *TelegramConfig) IsPartial() bool {
	return c.HasPartialConfig()
}

// BarkConfig contains endpoints and device keys for Bark iOS dispatching.
type BarkConfig struct {
	DeviceKeys []string
	URLs       []string
	PostURL    string
}

// CryptoConfig contains encryption keys for Bark payload encryption.
type CryptoConfig struct {
	Key                    []byte
	DeprecatedIVConfigured bool
}

// EmailConfig contains SMTP configuration for sending emails.
type EmailConfig struct {
	SenderEmail   string
	ReceiverEmail string
	Password      string
	SMTPServer    string
	SMTPPort      int
}

// Enabled reports whether EmailConfig has both SenderEmail and ReceiverEmail configured.
func (c *EmailConfig) Enabled() bool {
	return c != nil && strings.TrimSpace(c.SenderEmail) != "" && strings.TrimSpace(c.ReceiverEmail) != ""
}

// HasPartialConfig reports whether EmailConfig is non-nil but missing required fields.
func (c *EmailConfig) HasPartialConfig() bool {
	if c == nil || c.Enabled() {
		return false
	}
	return strings.TrimSpace(c.SenderEmail) != "" || strings.TrimSpace(c.ReceiverEmail) != "" || strings.TrimSpace(c.Password) != "" || strings.TrimSpace(c.SMTPServer) != ""
}

// IsPartial is an alias for HasPartialConfig.
func (c *EmailConfig) IsPartial() bool {
	return c.HasPartialConfig()
}

// IMAPConfig contains IMAP configuration for incoming email triggers.
type IMAPConfig struct {
	LoginEmail string
	Password   string
	IMAPServer string
	IMAPPort   int
	Mailbox    string
}

// Enabled reports whether IMAPConfig has both LoginEmail and Password configured.
func (c *IMAPConfig) Enabled() bool {
	return c != nil && strings.TrimSpace(c.LoginEmail) != "" && strings.TrimSpace(c.Password) != ""
}

// HasPartialConfig reports whether IMAPConfig is non-nil but missing required fields.
func (c *IMAPConfig) HasPartialConfig() bool {
	if c == nil || c.Enabled() {
		return false
	}
	return strings.TrimSpace(c.LoginEmail) != "" || strings.TrimSpace(c.Password) != "" || strings.TrimSpace(c.IMAPServer) != "" || strings.TrimSpace(c.Mailbox) != ""
}

// IsPartial is an alias for HasPartialConfig.
func (c *IMAPConfig) IsPartial() bool {
	return c.HasPartialConfig()
}

// RulesConfig holds JSON-parsed regex lists for message processing.
type RulesConfig struct {
	FilterBlack []string
	FilterWhite []string
	Captcha     []string
	Emergency   []string
}

// Config represents the application configuration tree.
// Optional channel configurations are nil when unconfigured.
type Config struct {
	Server     ServerConfig
	Auth       AuthConfig
	Bark       *BarkConfig
	Telegram   *TelegramConfig
	Email      *EmailConfig
	IMAP       *IMAPConfig
	Crypto     *CryptoConfig
	Rules      RulesConfig
	RunTrigger bool
}

// Load loads the configuration from system environment variables.
func Load() (*Config, error) {
	return LoadFromLookup(os.Getenv)
}

// LoadFromLookup loads configuration using a custom lookup function,
// enabling decoupled, fully isolated testing without mutating process environment.
func LoadFromLookup(lookup func(string) string) (*Config, error) {
	if lookup == nil {
		lookup = os.Getenv
	}

	// 1. Server Config
	portStr := lookup("PORT")
	if portStr == "" {
		portStr = lookup("SERVER_PORT")
	}
	port, err := parseOptionalInt(portStr, 8889, "PORT")
	if err != nil {
		return nil, err
	}

	trustedProxiesRaw := strings.TrimSpace(lookup("TRUSTED_PROXIES"))
	if trustedProxiesRaw == "" {
		trustedProxiesRaw = strings.TrimSpace(lookup("WEBHOOK_TRUSTED_PROXIES"))
	}
	trustedProxies := parseCommaList(trustedProxiesRaw)

	runTriggerStr := strings.TrimSpace(strings.ToLower(lookup("RUN_TRIGGER")))
	runTrigger := runTriggerStr == "true" || runTriggerStr == "1"

	cfg := &Config{
		Server: ServerConfig{
			Port:           port,
			TrustedProxies: trustedProxies,
		},
		Auth: AuthConfig{
			Username: strings.TrimSpace(lookup("AUTH_USERNAME")),
			Password: strings.TrimSpace(lookup("AUTH_PASSWORD")),
			Token:    strings.TrimSpace(lookup("AUTH_TOKEN")),
		},
		RunTrigger: runTrigger,
	}

	// 2. Telegram Config (Optional)
	tgToken := strings.TrimSpace(lookup("TELEGRAM_BOT_TOKEN"))
	tgChatIDStr := strings.TrimSpace(lookup("TELEGRAM_CHAT_ID"))
	tgBaseURL := strings.TrimSpace(lookup("TELEGRAM_API_URL"))
	if tgBaseURL == "" {
		tgBaseURL = strings.TrimSpace(lookup("TELEGRAM_BASE_URL"))
	}
	if tgToken != "" || tgChatIDStr != "" || tgBaseURL != "" {
		var chatID int64
		if tgChatIDStr != "" {
			var err error
			chatID, err = strconv.ParseInt(tgChatIDStr, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("environment variable TELEGRAM_CHAT_ID must be an integer, got: %q: %w", tgChatIDStr, err)
			}
		}
		cfg.Telegram = &TelegramConfig{
			BaseURL:  strings.TrimRight(tgBaseURL, "/"),
			BotToken: tgToken,
			ChatID:   chatID,
		}
	}

	// 3. Bark Config (Optional)
	devKeys := parseCommaList(lookup("BARK_DEVICE_KEY_LIST"))
	urls := parseCommaList(lookup("BARK_URL_LIST"))
	if len(devKeys) > 0 || len(urls) > 0 {
		postURL := strings.TrimSpace(lookup("BARK_POST_URL"))
		if postURL == "" {
			if getURL := strings.TrimSpace(lookup("BARK_GET_URL")); getURL != "" {
				postURL = strings.TrimSuffix(strings.TrimRight(getURL, "/"), "/push") + "/push"
			} else {
				postURL = "https://api.day.app/push"
			}
		}
		cfg.Bark = &BarkConfig{
			DeviceKeys: devKeys,
			URLs:       urls,
			PostURL:    postURL,
		}
	}

	// 4. Crypto Config (Optional)
	aesKey := strings.TrimSpace(lookup("AES_128_ECB_KEY"))
	aesIV := strings.TrimSpace(lookup("AES_128_ECB_IV"))
	if aesKey != "" || aesIV != "" {
		var keyBytes []byte
		if aesKey != "" {
			// Bark feeds the raw UTF-8 bytes of the configured key to AES-128-CBC and
			// its app validates the key by character count, so the key must be 16
			// ASCII characters (16 bytes == 16 runes). A multi-byte key would be
			// accepted here but could never be entered into the Bark app, silently
			// breaking every encrypted push.
			if len(aesKey) != 16 || utf8.RuneCountInString(aesKey) != 16 {
				return nil, fmt.Errorf(
					"environment variable AES_128_ECB_KEY must be exactly 16 ASCII characters, got %d bytes / %d characters",
					len(aesKey), utf8.RuneCountInString(aesKey))
			}
			keyBytes = []byte(aesKey)
		}
		cfg.Crypto = &CryptoConfig{
			Key:                    keyBytes,
			DeprecatedIVConfigured: aesIV != "",
		}
	}

	// 5. Email Deliver Config (Optional)
	emailSender := strings.TrimSpace(lookup("EMAIL_DELIVER_SENDER_EMAIL"))
	emailReceiver := strings.TrimSpace(lookup("EMAIL_DELIVER_RECEIVER_EMAIL"))
	emailPass := strings.TrimSpace(lookup("EMAIL_DELIVER_PASSWORD"))
	emailServerRaw := strings.TrimSpace(lookup("EMAIL_DELIVER_SMTP_SERVER"))
	emailPortRaw := strings.TrimSpace(lookup("EMAIL_DELIVER_SMTP_PORT"))
	if emailSender != "" || emailReceiver != "" || emailPass != "" || emailServerRaw != "" || emailPortRaw != "" {
		smtpServer := getOrDefault(lookup, "EMAIL_DELIVER_SMTP_SERVER", "smtp.exmail.qq.com")
		smtpPort, err := parseOptionalInt(lookup("EMAIL_DELIVER_SMTP_PORT"), 465, "EMAIL_DELIVER_SMTP_PORT")
		if err != nil {
			return nil, err
		}
		cfg.Email = &EmailConfig{
			SenderEmail:   emailSender,
			ReceiverEmail: emailReceiver,
			Password:      emailPass,
			SMTPServer:    smtpServer,
			SMTPPort:      smtpPort,
		}
	}

	// 6. IMAP Trigger Config (Optional)
	imapLogin := strings.TrimSpace(lookup("EMAIL_TRIGGER_LOGIN_EMAIL"))
	imapPass := strings.TrimSpace(lookup("EMAIL_TRIGGER_PASSWORD"))
	imapServerRaw := strings.TrimSpace(lookup("EMAIL_TRIGGER_IMAP_SERVER"))
	imapPortRaw := strings.TrimSpace(lookup("EMAIL_TRIGGER_IMAP_PORT"))
	imapMailboxRaw := strings.TrimSpace(lookup("EMAIL_TRIGGER_MAILBOX"))
	if imapLogin != "" || imapPass != "" || imapServerRaw != "" || imapPortRaw != "" || imapMailboxRaw != "" {
		imapServer := getOrDefault(lookup, "EMAIL_TRIGGER_IMAP_SERVER", "imap.exmail.qq.com")
		imapPort, err := parseOptionalInt(lookup("EMAIL_TRIGGER_IMAP_PORT"), 993, "EMAIL_TRIGGER_IMAP_PORT")
		if err != nil {
			return nil, err
		}
		cfg.IMAP = &IMAPConfig{
			LoginEmail: imapLogin,
			Password:   imapPass,
			IMAPServer: imapServer,
			IMAPPort:   imapPort,
			Mailbox:    getOrDefault(lookup, "EMAIL_TRIGGER_MAILBOX", "INBOX"),
		}
	}

	// 7. Regex Rules
	blackRules, err := parseJSONRegexList(lookup("REGEX_FILTER_BLACK"), "REGEX_FILTER_BLACK")
	if err != nil {
		return nil, err
	}
	whiteRules, err := parseJSONRegexList(lookup("REGEX_FILTER_WHITE"), "REGEX_FILTER_WHITE")
	if err != nil {
		return nil, err
	}
	captchaRules, err := parseJSONRegexList(lookup("REGEX_CAPTCHA"), "REGEX_CAPTCHA")
	if err != nil {
		return nil, err
	}
	emergencyRules, err := parseJSONRegexList(lookup("REGEX_EMERGENCY"), "REGEX_EMERGENCY")
	if err != nil {
		return nil, err
	}

	cfg.Rules = RulesConfig{
		FilterBlack: blackRules,
		FilterWhite: whiteRules,
		Captcha:     captchaRules,
		Emergency:   emergencyRules,
	}

	return cfg, nil
}

func getOrDefault(lookup func(string) string, key, defaultVal string) string {
	val := strings.TrimSpace(lookup(key))
	if val == "" {
		return defaultVal
	}
	return val
}

func parseOptionalInt(val string, defaultVal int, name string) (int, error) {
	val = strings.TrimSpace(val)
	if val == "" {
		return defaultVal, nil
	}
	parsed, err := strconv.Atoi(val)
	if err != nil {
		return 0, fmt.Errorf("environment variable %s must be an integer, got: %q: %w", name, val, err)
	}
	return parsed, nil
}

func parseCommaList(val string) []string {
	parts := strings.Split(val, ",")
	res := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			res = append(res, trimmed)
		}
	}
	return res
}

func parseJSONRegexList(val, name string) ([]string, error) {
	val = strings.TrimSpace(val)
	if val == "" {
		return []string{}, nil
	}

	var rawItems []string
	if err := json.Unmarshal([]byte(val), &rawItems); err != nil {
		return nil, fmt.Errorf("environment variable %s must be a JSON array of regex strings, got: %q: %w", name, val, err)
	}

	for _, item := range rawItems {
		if strings.TrimSpace(item) == "" {
			return nil, fmt.Errorf("environment variable %s must not contain an empty regex", name)
		}
	}

	return rawItems, nil
}
