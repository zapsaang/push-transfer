package imap

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"strings"

	"github.com/emersion/go-message/charset"
	"github.com/emersion/go-message/mail"
	"github.com/zapsaang/push-transfer/internal/model"
)

// TriggerSubject is the standard email subject required for triggering push-transfer.
const TriggerSubject = "PushTransfer Email Trigger"

// DecodeSubject decodes an RFC 2047 encoded subject into a UTF-8 string.
func DecodeSubject(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}

	decoder := &mime.WordDecoder{
		CharsetReader: charset.Reader,
	}

	decoded, err := decoder.DecodeHeader(raw)
	if err != nil {
		return "", fmt.Errorf("failed to decode RFC 2047 subject: %w", err)
	}

	return strings.TrimSpace(decoded), nil
}

// IsTriggerSubject reports whether subject matches the expected trigger subject.
func IsTriggerSubject(subject string) bool {
	return strings.TrimSpace(subject) == TriggerSubject
}

// ExtractTextPlain extracts text/plain content from an email message.
// Ignores attachments and non-plain parts.
func ExtractTextPlain(r io.Reader) (string, error) {
	mr, err := mail.CreateReader(r)
	if err != nil {
		return "", fmt.Errorf("failed to create mail reader: %w", err)
	}
	defer func() { _ = mr.Close() }()

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("error reading email part: %w", err)
		}

		switch h := part.Header.(type) {
		case *mail.InlineHeader:
			contentType, _, _ := h.ContentType()
			if contentType == "" || contentType == "text/plain" {
				b, err := io.ReadAll(part.Body)
				if err != nil {
					return "", fmt.Errorf("failed to read inline text body: %w", err)
				}
				return strings.TrimSpace(string(b)), nil
			}
		case *mail.AttachmentHeader:
			// Explicitly skip attachments
			continue
		}
	}

	return "", errors.New("no text/plain inline body found in email")
}

// ParseEmail parses an RFC 822 email, decodes subject and body, and returns
// the model.Message if the subject matches TriggerSubject.
func ParseEmail(r io.Reader) (*model.Message, bool, error) {
	mr, err := mail.CreateReader(r)
	if err != nil {
		return nil, false, fmt.Errorf("failed to create mail reader: %w", err)
	}
	defer func() { _ = mr.Close() }()

	// 1. Decode subject
	rawSubject := mr.Header.Get("Subject")
	subject, err := DecodeSubject(rawSubject)
	if err != nil {
		return nil, false, err
	}

	if !IsTriggerSubject(subject) {
		return nil, false, nil
	}

	// 2. Extract body
	var bodyText string
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, false, fmt.Errorf("error reading email part: %w", err)
		}

		switch h := part.Header.(type) {
		case *mail.InlineHeader:
			contentType, _, _ := h.ContentType()
			if contentType == "" || contentType == "text/plain" {
				b, err := io.ReadAll(part.Body)
				if err != nil {
					return nil, false, fmt.Errorf("failed to read inline text body: %w", err)
				}
				bodyText = strings.TrimSpace(string(b))
				break
			}
		}
		if bodyText != "" {
			break
		}
	}

	if bodyText == "" {
		return nil, false, errors.New("empty or missing text/plain body in email")
	}

	// 3. Unmarshal message
	var msg model.Message
	if err := json.Unmarshal([]byte(bodyText), &msg); err != nil {
		return nil, false, fmt.Errorf("failed to unmarshal JSON payload: %w", err)
	}

	return &msg, true, nil
}
