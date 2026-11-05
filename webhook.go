package tokport

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidWebhook means the webhook signature, timestamp or body did not check out.
var ErrInvalidWebhook = errors.New("tokport: invalid webhook signature")

var hex64 = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// WebhookPayload is the body TokPort POSTs to a request's webhook URL.
type WebhookPayload struct {
	RequestID        string          `json:"request_id"`
	GatewayRequestID string          `json:"gateway_request_id"`
	Status           string          `json:"status"` // "OK" or "ERROR"
	Payload          json.RawMessage `json:"payload,omitempty"`
	Error            *ErrorInfo      `json:"error,omitempty"`
}

// WebhookOptions tunes VerifyWebhook. The zero value is fine.
type WebhookOptions struct {
	Tolerance time.Duration // accepted clock skew, default 5 minutes
	Now       time.Time     // for tests; defaults to time.Now()
}

// VerifyWebhook checks a TokPort webhook and returns its payload. The signature is HMAC-SHA256
// over "<timestamp>.<raw body>" with the API key's signing secret
// (https://tokport.io/dashboard/keys). Pass the body exactly as received.
func VerifyWebhook(rawBody []byte, header http.Header, secret string, opts *WebhookOptions) (*WebhookPayload, error) {
	if opts == nil {
		opts = &WebhookOptions{}
	}
	tolerance, now := opts.Tolerance, opts.Now
	if tolerance <= 0 {
		tolerance = 5 * time.Minute
	}
	if now.IsZero() {
		now = time.Now()
	}
	timestamp := header.Get("X-Tokport-Webhook-Timestamp")
	signature := header.Get("X-Tokport-Webhook-Signature")
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || ts < 0 || !hex64.MatchString(signature) {
		return nil, ErrInvalidWebhook
	}
	if d := now.Sub(time.Unix(ts, 0)); d > tolerance || d < -tolerance {
		return nil, ErrInvalidWebhook
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(rawBody)
	if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(strings.ToLower(signature))) {
		return nil, ErrInvalidWebhook
	}
	var p WebhookPayload
	if err := json.Unmarshal(rawBody, &p); err != nil {
		return nil, ErrInvalidWebhook
	}
	return &p, nil
}
