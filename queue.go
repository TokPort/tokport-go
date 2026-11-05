package tokport

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// QueueService is the queue API: submit, follow and fetch long-running requests.
type QueueService struct{ c *Client }

// SubmitOptions tunes Queue.Submit. The zero value is fine.
type SubmitOptions struct {
	Task       TaskKind // force the task kind; inferred from the input otherwise
	WebhookURL string   // POSTed the result when the request finishes (see VerifyWebhook)
	Priority   string   // "normal" (default) or "low"
}

func queuePath(model string, params url.Values, parts ...string) string {
	p := "/queue/v1/" + url.PathEscape(model)
	for _, part := range parts {
		p += "/" + url.PathEscape(part)
	}
	if qs := params.Encode(); qs != "" {
		p += "?" + qs
	}
	return p
}

func logsParam(logs bool) url.Values {
	if logs {
		return url.Values{"logs": {"1"}}
	}
	return nil
}

// Submit queues a request and returns its initial status.
func (q *QueueService) Submit(ctx context.Context, model string, input Input, opts *SubmitOptions) (*QueueStatus, error) {
	if opts == nil {
		opts = &SubmitOptions{}
	}
	params := url.Values{}
	if opts.Task != "" {
		params.Set("task", string(opts.Task))
	}
	if opts.WebhookURL != "" {
		params.Set("webhook", opts.WebhookURL)
	}
	var header map[string]string
	if opts.Priority != "" {
		header = map[string]string{"X-Tokport-Priority": opts.Priority}
	}
	if input == nil {
		input = Input{}
	}
	var s QueueStatus
	if err := q.c.doJSON(ctx, http.MethodPost, queueBase, queuePath(model, params), input, header, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Status returns the current status of a request.
func (q *QueueService) Status(ctx context.Context, model, requestID string, logs bool) (*QueueStatus, error) {
	var s QueueStatus
	if err := q.c.doJSON(ctx, http.MethodGet, queueBase, queuePath(model, logsParam(logs), "requests", requestID, "status"), nil, nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Stream calls fn for each status frame (server-sent events) until a terminal state, the stream
// ends, or fn returns false.
func (q *QueueService) Stream(ctx context.Context, model, requestID string, logs bool, fn func(QueueStatus) bool) error {
	req, err := q.c.newRequest(ctx, http.MethodGet, queueBase, queuePath(model, logsParam(logs), "requests", requestID, "status", "stream"), nil, map[string]string{"Accept": "text/event-stream"})
	if err != nil {
		return err
	}
	// the client's per-request timeout would cut long streams; ctx bounds this one
	streamClient := *q.c.http
	streamClient.Timeout = 0
	res, err := streamClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return &Error{Status: res.StatusCode, Code: "stream", Message: fmt.Sprintf("stream unavailable (%d)", res.StatusCode)}
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var data []string
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			if len(data) > 0 {
				var s QueueStatus
				if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &s); err != nil {
					return err
				}
				data = data[:0]
				if !fn(s) || s.Status.Terminal() {
					return nil
				}
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimLeft(line[5:], " "))
		}
	}
	return sc.Err()
}

// Result returns the result of a finished request. With waitSeconds > 0 (max 60) the server
// holds the call until the request finishes or the time is up. A request that is still running
// yields an *Error with Code "pending".
func (q *QueueService) Result(ctx context.Context, model, requestID string, waitSeconds int) (*Result, error) {
	var params url.Values
	if waitSeconds > 0 {
		params = url.Values{"timeout": {strconv.Itoa(waitSeconds)}}
	}
	raw, err := q.c.do(ctx, http.MethodGet, queueBase, queuePath(model, params, "requests", requestID), nil, nil)
	if err != nil {
		return nil, err
	}
	var probe struct {
		Status QueueState `json:"status"`
	}
	if json.Unmarshal(raw, &probe) == nil && (probe.Status == InQueue || probe.Status == InProgress) {
		return nil, &Error{Status: 202, Code: "pending", Message: "request is still " + string(probe.Status), Body: raw}
	}
	return decodeResult(raw)
}

// CancelResponse is the answer to Queue.Cancel.
type CancelResponse struct {
	Status    string `json:"status"`
	RequestID string `json:"request_id"`
}

// Cancel asks to cancel a queued request.
func (q *QueueService) Cancel(ctx context.Context, model, requestID string) (*CancelResponse, error) {
	var out CancelResponse
	if err := q.c.doJSON(ctx, http.MethodPut, queueBase, queuePath(model, nil, "requests", requestID, "cancel"), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ModelsService reads the public model catalog.
type ModelsService struct{ c *Client }

// ModelFilter narrows Models.List. The zero value lists everything.
type ModelFilter struct {
	Query     string   // free-text search
	Modality  string   // "image" or "video"
	Vendor    string   // e.g. "OpenAI", "Google"
	Task      TaskKind // e.g. TextToVideo
	Available bool     // only models that can be called right now
}

// List returns the catalog: ids, prices, endpoints and links.
func (m *ModelsService) List(ctx context.Context, f *ModelFilter) ([]ModelInfo, error) {
	if f == nil {
		f = &ModelFilter{}
	}
	params := url.Values{}
	for k, v := range map[string]string{"q": f.Query, "modality": f.Modality, "vendor": f.Vendor, "task": string(f.Task)} {
		if v != "" {
			params.Set(k, v)
		}
	}
	if f.Available {
		params.Set("available", "1")
	}
	path := "/api/models"
	if qs := params.Encode(); qs != "" {
		path += "?" + qs
	}
	var body struct {
		Models []ModelInfo `json:"models"`
	}
	if err := m.c.doJSON(ctx, http.MethodGet, siteBase, path, nil, nil, &body); err != nil {
		return nil, err
	}
	return body.Models, nil
}
