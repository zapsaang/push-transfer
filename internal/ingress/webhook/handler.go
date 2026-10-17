package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/zapsaang/push-transfer/internal/config"
	"github.com/zapsaang/push-transfer/internal/model"
)

// Processor processes messages through the filtering and enrichment pipeline.
type Processor interface {
	Process(msg *model.Message) (bool, error)
}

// Deliverer distributes messages to delivery targets.
type Deliverer interface {
	Deliver(ctx context.Context, msg *model.Message) error
}

// DefaultMaxBodyBytes is the default maximum webhook request body size (1MB).
const DefaultMaxBodyBytes int64 = 1 << 20

// HandlerOptions configures the Webhook Handler.
type HandlerOptions struct {
	Credentials    []config.Credential
	MaxBodyBytes   int64
	TrustedProxies []string
	Processor      Processor
	Deliverer      Deliverer
	Logger         *slog.Logger
}

// Handler handles incoming Webhook HTTP requests.
type Handler struct {
	creds          *credentialSet
	maxBodyBytes   int64
	trustedProxies []*net.IPNet
	processor      Processor
	deliverer      Deliverer
	logger         *slog.Logger
}

// NewHandler creates a new Webhook Handler.
//
// There is no open-access mode: a handler built without any usable credential rejects every
// request. Serving the webhook unauthenticated is prevented at startup instead
// (cmd/push-transfer fails fast), so an empty set here means the caller wired things up wrong
// and the safe outcome is to deny.
func NewHandler(opts HandlerOptions) *Handler {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	maxBodyBytes := opts.MaxBodyBytes
	if maxBodyBytes <= 0 {
		maxBodyBytes = DefaultMaxBodyBytes
	}
	creds := newCredentialSet(opts.Credentials)
	if creds.empty() {
		logger.Warn("webhook handler built without any usable credential; all requests will be rejected (fail-closed)")
	}
	return &Handler{
		creds:          creds,
		maxBodyBytes:   maxBodyBytes,
		trustedProxies: parseTrustedProxies(opts.TrustedProxies, logger),
		processor:      opts.Processor,
		deliverer:      opts.Deliverer,
		logger:         logger,
	}
}

func parseTrustedProxies(entries []string, logger *slog.Logger) []*net.IPNet {
	var nets []*net.IPNet
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "/") {
			ip := net.ParseIP(entry)
			if ip == nil {
				logger.Warn("ignoring invalid trusted proxy IP", "entry", entry)
				continue
			}
			if ip.To4() != nil {
				entry += "/32"
			} else {
				entry += "/128"
			}
		}
		_, ipNet, err := net.ParseCIDR(entry)
		if err != nil {
			logger.Warn("ignoring invalid trusted proxy CIDR", "entry", entry, "error", err)
			continue
		}
		nets = append(nets, ipNet)
	}
	return nets
}

func isTrustedProxy(remoteIP net.IP, customNets []*net.IPNet) bool {
	if remoteIP == nil {
		return false
	}
	if len(customNets) > 0 {
		for _, n := range customNets {
			if n.Contains(remoteIP) {
				return true
			}
		}
		return false
	}
	return remoteIP.IsLoopback() || remoteIP.IsPrivate()
}

// clientIPFromForwardedFor resolves the client address from an X-Forwarded-For value.
//
// Each proxy appends the address it saw to the header, so the rightmost entries are the
// ones a trusted proxy vouched for while the leftmost ones may have been supplied by the
// client itself. The value is therefore walked from right to left, skipping hops that are
// themselves trusted proxies; the first untrusted address is the real client. If every
// hop is a trusted proxy, the leftmost entry is the origin. An empty string means the
// header held no usable address.
func clientIPFromForwardedFor(xff string, customNets []*net.IPNet) string {
	var leftmost, candidate string
	for _, part := range strings.Split(xff, ",") {
		ipStr := parseIPCandidate(part)
		if ipStr == "" {
			continue
		}
		if leftmost == "" {
			leftmost = ipStr
		}
		if ip := net.ParseIP(ipStr); ip != nil && isTrustedProxy(ip, customNets) {
			continue
		}
		candidate = ipStr
	}
	if candidate == "" {
		return leftmost
	}
	return candidate
}

// GetRealIP extracts client IP addressing proxy headers (X-Forwarded-For, X-Real-IP)
// only when the direct peer is a trusted proxy (loopback or private network by default),
// falling back to RemoteAddr.
func GetRealIP(r *http.Request) string {
	return extractRealIP(r, nil)
}

func (h *Handler) getRealIP(r *http.Request) string {
	return extractRealIP(r, h.trustedProxies)
}

func parseIPCandidate(raw string) string {
	candidate := strings.TrimSpace(raw)
	if candidate == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(candidate); err == nil && host != "" {
		candidate = host
	} else if len(candidate) > 2 && candidate[0] == '[' && candidate[len(candidate)-1] == ']' {
		candidate = candidate[1 : len(candidate)-1]
	}
	if zone := strings.IndexByte(candidate, '%'); zone != -1 {
		candidate = candidate[:zone]
	}
	if ip := net.ParseIP(candidate); ip != nil {
		return ip.String()
	}
	return ""
}

func extractRealIP(r *http.Request, customNets []*net.IPNet) string {
	if r == nil {
		return ""
	}
	remoteHost := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil && host != "" {
		remoteHost = host
	}

	remoteIP := net.ParseIP(remoteHost)
	if isTrustedProxy(remoteIP, customNets) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if clientIP := clientIPFromForwardedFor(xff, customNets); clientIP != "" {
				return clientIP
			}
		}

		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			if ipStr := parseIPCandidate(xri); ipStr != "" {
				return ipStr
			}
		}
	}

	return remoteHost
}

func (h *Handler) isAuthorized(r *http.Request) (string, bool) {
	return h.creds.verify(r)
}

// ServeHTTP implements http.Handler for the webhook endpoint.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	clientIP := h.getRealIP(r)
	h.logger.Info("incoming request", "ip", clientIP, "method", r.Method, "path", r.URL.Path)

	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"status":  "failed",
			"message": "method not allowed",
		})
		return
	}

	// 1. Authentication (Bearer Token and/or HTTP Basic Auth)
	name, authorized := h.isAuthorized(r)
	if !authorized {
		h.logger.Warn("webhook auth failed", "ip", clientIP)
		// RFC 9110 section 15.5.2 requires a 401 response for HTTP Basic Auth to carry a
		// challenge, otherwise clients cannot prompt for credentials. Only advertise
		// Basic when it is actually configured, so token-only deployments do not trigger
		// spurious browser credential dialogs.
		if h.creds != nil && h.creds.hasBasicAuth {
			w.Header().Set("WWW-Authenticate", `Basic realm="push-transfer", charset="UTF-8"`)
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"status":  "failed",
			"message": "unauthorized",
		})
		return
	}
	if name != "" {
		h.logger.Info("webhook authorized", "ip", clientIP, "credential", name)
	}

	// 2. Decode JSON body with size limit
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBodyBytes)
	defer func() { _ = r.Body.Close() }()

	dec := json.NewDecoder(r.Body)
	var msg model.Message
	err := dec.Decode(&msg)
	if err == nil {
		if extraErr := dec.Decode(&struct{}{}); !errors.Is(extraErr, io.EOF) {
			if extraErr != nil {
				err = extraErr
			} else {
				err = errors.New("unexpected trailing JSON data")
			}
		}
	}
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			h.logger.Warn("webhook request body exceeds limit", "ip", clientIP, "limit", h.maxBodyBytes)
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{
				"status":  "failed",
				"message": "request body too large",
			})
			return
		}
		h.logger.Warn("webhook invalid json", "ip", clientIP, "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"status":  "failed",
			"message": "invalid json",
		})
		return
	}

	// Validate msg_type: fail-fast on missing or invalid msg_type (ADR-05)
	if !msg.MsgType.Valid() {
		h.logger.Warn("webhook invalid or missing msg_type", "ip", clientIP, "msg_type", string(msg.MsgType))
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"status":  "failed",
			"message": "invalid or missing msg_type",
		})
		return
	}

	// 3. Pipeline Processing
	if h.processor != nil {
		pass, err := h.processor.Process(&msg)
		if err != nil {
			h.logger.Error("webhook pipeline error", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"status":  "failed",
				"message": "process failed",
			})
			return
		}
		if !pass {
			h.logger.Info("webhook message blocked by filter", "title", msg.Title)
			writeJSON(w, http.StatusOK, map[string]string{
				"status": "blocked",
			})
			return
		}
	}

	// 4. Delivery
	if h.deliverer != nil {
		if err := h.deliverer.Deliver(r.Context(), &msg); err != nil {
			h.logger.Error("webhook delivery failed", "error", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"status":  "deliver_failed",
				"message": err.Error(),
			})
			return
		}
	}

	// 5. Success
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "success",
	})
}

func writeJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(data)
}
