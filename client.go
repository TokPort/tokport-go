// Package tokport is the Go client for the TokPort image & video generation API
// (https://tokport.io): GPT Image, Nano Banana, Seedream, Seedance, Kling, Qwen Image and more
// behind one OpenAI-compatible API key.
//
//	client, err := tokport.NewClient(os.Getenv("TOKPORT_API_KEY"))
//	out, err := client.Run(ctx, tokport.ModelGptImage2, tokport.Input{"prompt": "a neon koi"}, nil)
//	fmt.Println(out.Image.Data[0].URL)
//
// Docs: https://tokport.io/docs/clients · Models and prices: https://tokport.io/models
package tokport

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Version is the SDK version, sent in the User-Agent header.
const Version = "0.1.0"

const (
	DefaultBaseURL = "https://api.tokport.io"
	DefaultSiteURL = "https://tokport.io"
	defaultTimeout = 10 * time.Minute
)

var videoHint = regexp.MustCompile(`(?i)video|seedance|kling|hailuo|wan|sora|veo|vidu|jimeng|minimax`)

// Client talks to the TokPort API. It is safe for concurrent use.
type Client struct {
	apiKey   string
	baseURL  string
	queueURL string
	siteURL  string
	http     *http.Client

	Queue  *QueueService
	Models *ModelsService
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the API base URL (default https://api.tokport.io).
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") } }

// WithQueueURL overrides the queue API base URL (defaults to the base URL).
func WithQueueURL(u string) Option { return func(c *Client) { c.queueURL = strings.TrimRight(u, "/") } }

// WithSiteURL overrides the storefront URL used for the model catalog (default https://tokport.io).
func WithSiteURL(u string) Option { return func(c *Client) { c.siteURL = strings.TrimRight(u, "/") } }

// WithHTTPClient sets the HTTP client (default: http.Client with a 2 minute timeout per request;
// streams are not subject to it).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// NewClient returns a client for the given API key (https://tokport.io/dashboard/keys).
func NewClient(apiKey string, opts ...Option) (*Client, error) {
	if apiKey == "" {
		return nil, &Error{Code: "config", Message: "provide an API key (get one at https://tokport.io/dashboard/keys)"}
	}
	c := &Client{apiKey: apiKey, baseURL: DefaultBaseURL, siteURL: DefaultSiteURL, http: &http.Client{Timeout: 2 * time.Minute}}
	for _, o := range opts {
		o(c)
	}
	if c.queueURL == "" {
		c.queueURL = c.baseURL
	}
	c.Queue = &QueueService{c: c}
	c.Models = &ModelsService{c: c}
	return c, nil
}

// InferTask picks the task for a sync call: the model's modality from the catalog, else a guess
// from its id; an image input makes it an image-to-* task.
func InferTask(model string, input Input) TaskKind {
	modality, known := ModelModality[model]
	hasImage := false
	for _, k := range []string{"image", "input_reference", "images"} {
		if v, ok := input[k]; ok && v != nil && v != "" {
			if list, isList := v.([]any); isList && len(list) == 0 {
				continue
			}
			hasImage = true
		}
	}
	if modality == "video" || (!known && videoHint.MatchString(model)) {
		if hasImage {
			return ImageToVideo
		}
		return TextToVideo
	}
	if hasImage {
		return ImageToImage
	}
	return TextToImage
}

// EndpointFor is the sync endpoint for a task.
func EndpointFor(task TaskKind) string {
	switch task {
	case TextToVideo, ImageToVideo:
		return "/v1/videos"
	case ImageToImage:
		return "/v1/images/edits"
	default:
		return "/v1/images/generations"
	}
}

// ToDataURL turns raw bytes into a data: URL for image inputs.
func ToDataURL(data []byte, mime string) string {
	if mime == "" {
		mime = "image/png"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

type base int

const (
	apiBase base = iota
	queueBase
	siteBase
)

func (c *Client) url(b base, path string) string {
	switch b {
	case queueBase:
		return c.queueURL + path
	case siteBase:
		return c.siteURL + path
	default:
		return c.baseURL + path
	}
}

func (c *Client) newRequest(ctx context.Context, method string, b base, path string, body any, header map[string]string) (*http.Request, error) {
	var rd io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(b, path), rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "tokport-go/"+Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if b != siteBase {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	return req, nil
}

// do sends a request and returns the body, turning error statuses (not 202) into *Error.
func (c *Client) do(ctx context.Context, method string, b base, path string, body any, header map[string]string) (json.RawMessage, error) {
	req, err := c.newRequest(ctx, method, b, path, body, header)
	if err != nil {
		return nil, err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		e := &Error{Status: res.StatusCode, Code: fmt.Sprintf("http_%d", res.StatusCode), Message: fmt.Sprintf("HTTP %d", res.StatusCode), RequestID: res.Header.Get("X-Oneapi-Request-Id")}
		if json.Valid(raw) {
			e.Body = raw
			var wrapped struct {
				Error *ErrorInfo `json:"error"`
			}
			if json.Unmarshal(raw, &wrapped) == nil && wrapped.Error != nil {
				if wrapped.Error.Message != "" {
					e.Message = wrapped.Error.Message
				}
				if wrapped.Error.Code != "" {
					e.Code = wrapped.Error.Code
				}
			}
		}
		return nil, e
	}
	return raw, nil
}

func (c *Client) doJSON(ctx context.Context, method string, b base, path string, body any, header map[string]string, out any) error {
	raw, err := c.do(ctx, method, b, path, body, header)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// RunOptions tunes Run. The zero value is fine.
type RunOptions struct {
	Task         TaskKind      // inferred from the model and input when empty
	PollInterval time.Duration // video status polling, default 5s
	Timeout      time.Duration // give up on a video after this long, default 10 minutes
}

// Run is a synchronous call. Images answer inline; videos submit, poll until done and download
// the clip into RunResult.Video.Bytes.
func (c *Client) Run(ctx context.Context, model string, input Input, opts *RunOptions) (*RunResult, error) {
	if opts == nil {
		opts = &RunOptions{}
	}
	task := opts.Task
	if task == "" {
		task = InferTask(model, input)
	}
	body := Input{"model": model}
	for k, v := range input {
		body[k] = v
	}
	endpoint := EndpointFor(task)
	if endpoint != "/v1/videos" {
		var img ImageResult
		if err := c.doJSON(ctx, http.MethodPost, apiBase, endpoint, body, nil, &img); err != nil {
			return nil, err
		}
		return &RunResult{Image: &img}, nil
	}
	var submitted VideoTask
	if err := c.doJSON(ctx, http.MethodPost, apiBase, "/v1/videos", body, nil, &submitted); err != nil {
		return nil, err
	}
	poll, timeout := opts.PollInterval, opts.Timeout
	if poll <= 0 {
		poll = 5 * time.Second
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	deadline := time.Now().Add(timeout)
	taskPath := "/v1/videos/" + url.PathEscape(submitted.ID)
	for {
		if err := sleep(ctx, poll); err != nil {
			return nil, err
		}
		var status VideoTask
		if err := c.doJSON(ctx, http.MethodGet, apiBase, taskPath, nil, nil, &status); err != nil {
			return nil, err
		}
		switch status.Status {
		case "completed":
			clip, contentType, err := c.download(ctx, taskPath+"/content")
			if err != nil {
				return nil, err
			}
			return &RunResult{Video: &VideoRunResult{VideoTask: status, Bytes: clip, ContentType: contentType}}, nil
		case "failed":
			e := &Error{Status: 422, Code: "task_failed", Message: "video task failed"}
			if status.Error != nil {
				e.Message = status.Error.Message
				if status.Error.Code != "" {
					e.Code = status.Error.Code
				}
			}
			return nil, e
		}
		if time.Now().After(deadline) {
			return nil, &Error{Status: 408, Code: "timeout", Message: "timed out waiting for the video"}
		}
	}
}

func (c *Client) download(ctx context.Context, path string) ([]byte, string, error) {
	req, err := c.newRequest(ctx, http.MethodGet, apiBase, path, nil, nil)
	if err != nil {
		return nil, "", err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return nil, "", &Error{Status: res.StatusCode, Code: "content", Message: fmt.Sprintf("content download failed (%d)", res.StatusCode)}
	}
	clip, err := io.ReadAll(res.Body)
	contentType := res.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "video/mp4"
	}
	return clip, contentType, err
}

// SubscribeOptions tunes Subscribe. The zero value is fine.
type SubscribeOptions struct {
	SubmitOptions
	Logs          bool              // include worker logs in status updates
	OnQueueUpdate func(QueueStatus) // called for every status update
	Timeout       time.Duration     // give up after this long, default 10 minutes
	PollInterval  time.Duration     // polling when the event stream is unavailable, default 2s
}

// Subscribe submits to the queue, follows the status (server-sent events, falling back to
// polling) and returns the result.
func (c *Client) Subscribe(ctx context.Context, model string, input Input, opts *SubscribeOptions) (*Result, error) {
	if opts == nil {
		opts = &SubscribeOptions{}
	}
	notify := func(s QueueStatus) {
		if opts.OnQueueUpdate != nil {
			opts.OnQueueUpdate(s)
		}
	}
	timeout, poll := opts.Timeout, opts.PollInterval
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if poll <= 0 {
		poll = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	timedOut := func(err error) error {
		if errors.Is(err, context.DeadlineExceeded) {
			return &Error{Status: 408, Code: "timeout", Message: "timed out waiting for the request"}
		}
		return err
	}

	submitted, err := c.Queue.Submit(ctx, model, input, &opts.SubmitOptions)
	if err != nil {
		return nil, err
	}
	notify(*submitted)
	last := *submitted
	streamErr := c.Queue.Stream(ctx, model, submitted.RequestID, opts.Logs, func(s QueueStatus) bool {
		last = s
		notify(s)
		return !s.Status.Terminal()
	})
	if streamErr != nil && ctx.Err() != nil {
		return nil, timedOut(ctx.Err())
	}
	// stream unavailable or cut short: poll instead
	for !last.Status.Terminal() {
		if err := sleep(ctx, poll); err != nil {
			return nil, timedOut(err)
		}
		s, err := c.Queue.Status(ctx, model, submitted.RequestID, opts.Logs)
		if err != nil {
			return nil, timedOut(err)
		}
		last = *s
		notify(last)
	}
	switch last.Status {
	case Failed:
		e := &Error{Status: 422, Code: "failed", Message: "request failed", RequestID: last.GatewayRequestID}
		if last.Error != nil {
			e.Message = last.Error.Message
			if last.Error.Code != "" {
				e.Code = last.Error.Code
			}
		}
		return nil, e
	case Cancelled:
		return nil, &Error{Status: 410, Code: "cancelled", Message: "request was cancelled"}
	}
	return c.Queue.Result(ctx, model, submitted.RequestID, 0)
}
