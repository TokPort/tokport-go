package tokport

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type call struct {
	Method, Path, Query string
	Header              http.Header
	Body                string
}

// stub serves routes keyed by "METHOD /path" and records every request.
func stub(t *testing.T, routes map[string]http.HandlerFunc) (*httptest.Server, *[]call) {
	t.Helper()
	var mu sync.Mutex
	calls := []call{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, call{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), string(body)})
		mu.Unlock()
		h, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"message":"no stub for ` + r.Method + " " + r.URL.Path + `"}}`))
			return
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func jsonReply(status int, body any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
}

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c, err := NewClient("sk-test", WithBaseURL(srv.URL), WithSiteURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestInferTaskAndEndpoints(t *testing.T) {
	cases := []struct {
		model string
		input Input
		want  TaskKind
	}{
		{ModelSeedream5Pro, Input{"prompt": "x"}, TextToImage},
		{ModelSeedream5Pro, Input{"prompt": "x", "image": "data:…"}, ImageToImage},
		{ModelKling30, Input{"prompt": "x"}, TextToVideo},
		{"seedance-9-unreleased", Input{"images": []any{"u"}}, ImageToVideo},
		{ModelSeedream5Pro, Input{"images": []any{}}, TextToImage},
		// catalog modality beats the name guess
		{ModelKling30Image, Input{"prompt": "x"}, TextToImage},
		{ModelWan27Image, Input{"prompt": "x"}, TextToImage},
	}
	for _, tc := range cases {
		if got := InferTask(tc.model, tc.input); got != tc.want {
			t.Errorf("InferTask(%q, %v) = %s, want %s", tc.model, tc.input, got, tc.want)
		}
	}
	if EndpointFor(ImageToImage) != "/v1/images/edits" || EndpointFor(ImageToVideo) != "/v1/videos" || EndpointFor(TextToImage) != "/v1/images/generations" {
		t.Error("EndpointFor mismatch")
	}
	if got := ToDataURL([]byte{1, 2}, "image/jpeg"); got != "data:image/jpeg;base64,AQI=" {
		t.Errorf("ToDataURL = %s", got)
	}
	if _, err := NewClient(""); err == nil {
		t.Error("NewClient(\"\") should fail")
	}
}

func TestRunImageAndAPIErrors(t *testing.T) {
	srv, calls := stub(t, map[string]http.HandlerFunc{
		"POST /v1/images/generations": jsonReply(200, map[string]any{"created": 1, "data": []any{map[string]any{"url": "https://x/1.png"}}}),
	})
	out, err := newTestClient(t, srv).Run(context.Background(), ModelSeedream5Pro, Input{"prompt": "a cat", "n": 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Image == nil || out.Image.Data[0].URL != "https://x/1.png" {
		t.Fatalf("unexpected result %+v", out)
	}
	c := (*calls)[0]
	if c.Header.Get("Authorization") != "Bearer sk-test" || !strings.HasPrefix(c.Header.Get("User-Agent"), "tokport-go/") {
		t.Errorf("headers %v", c.Header)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(c.Body), &body)
	if body["model"] != ModelSeedream5Pro || body["prompt"] != "a cat" {
		t.Errorf("body %v", body)
	}

	failing, _ := stub(t, map[string]http.HandlerFunc{
		"POST /v1/images/generations": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Oneapi-Request-Id", "rid2")
			jsonReply(402, map[string]any{"error": map[string]any{"message": "insufficient balance", "code": "quota"}})(w, r)
		},
	})
	_, err = newTestClient(t, failing).Run(context.Background(), ModelSeedream5Pro, Input{"prompt": "x"}, nil)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != 402 || apiErr.Code != "quota" || apiErr.RequestID != "rid2" || apiErr.Message != "insufficient balance" {
		t.Fatalf("unexpected error %#v", err)
	}
}

func TestRunVideoPollsAndDownloads(t *testing.T) {
	polls := 0
	srv, _ := stub(t, map[string]http.HandlerFunc{
		"POST /v1/videos": jsonReply(200, map[string]any{"id": "task_1", "status": "queued"}),
		"GET /v1/videos/task_1": func(w http.ResponseWriter, r *http.Request) {
			polls++
			status := "in_progress"
			if polls >= 2 {
				status = "completed"
			}
			jsonReply(200, map[string]any{"id": "task_1", "status": status, "progress": 50})(w, r)
		},
		"GET /v1/videos/task_1/content": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte{1, 2, 3})
		},
	})
	out, err := newTestClient(t, srv).Run(context.Background(), ModelSeedance20, Input{"prompt": "waves", "seconds": "5"}, &RunOptions{PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if out.Video == nil || out.Video.ContentType != "video/mp4" || string(out.Video.Bytes) != "\x01\x02\x03" || polls != 2 {
		t.Fatalf("unexpected %+v polls=%d", out.Video, polls)
	}
}

func TestQueueSubmitOptions(t *testing.T) {
	srv, calls := stub(t, map[string]http.HandlerFunc{
		"POST /queue/v1/seedream-5-pro": jsonReply(200, map[string]any{"status": "IN_QUEUE", "request_id": "req_1", "queue_position": 0}),
	})
	s, err := newTestClient(t, srv).Queue.Submit(context.Background(), ModelSeedream5Pro, Input{"prompt": "x"}, &SubmitOptions{Task: TextToImage, WebhookURL: "https://hook", Priority: "low"})
	if err != nil {
		t.Fatal(err)
	}
	c := (*calls)[0]
	if s.RequestID != "req_1" || s.QueuePosition == nil || *s.QueuePosition != 0 {
		t.Errorf("status %+v", s)
	}
	if !strings.Contains(c.Query, "webhook=https%3A%2F%2Fhook") || !strings.Contains(c.Query, "task=text-to-image") || c.Header.Get("X-Tokport-Priority") != "low" {
		t.Errorf("call %+v", c)
	}
	if c.Body != `{"prompt":"x"}` {
		t.Errorf("body %s", c.Body)
	}
}

func TestSubscribeStreamsThenFetchesResult(t *testing.T) {
	srv, _ := stub(t, map[string]http.HandlerFunc{
		"POST /queue/v1/seedream-5-pro": jsonReply(200, map[string]any{"status": "IN_QUEUE", "request_id": "req_1"}),
		"GET /queue/v1/seedream-5-pro/requests/req_1/status/stream": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"status\":\"IN_QUEUE\",\"request_id\":\"req_1\",\"queue_position\":1}\n\n: keep-alive\n\n")
			_, _ = io.WriteString(w, "data: {\"status\":\"IN_PROGRESS\",\"request_id\":\"req_1\"}\r\n\r\ndata: {\"status\":\"COMPLETED\",\"request_id\":\"req_1\"}\n\n")
		},
		"GET /queue/v1/seedream-5-pro/requests/req_1": jsonReply(200, map[string]any{"data": []any{map[string]any{"url": "https://x/1.png"}}, "request_id": "req_1"}),
	})
	var seen []string
	out, err := newTestClient(t, srv).Subscribe(context.Background(), ModelSeedream5Pro, Input{"prompt": "x"}, &SubscribeOptions{OnQueueUpdate: func(s QueueStatus) { seen = append(seen, string(s.Status)) }})
	if err != nil {
		t.Fatal(err)
	}
	if out.Image == nil || out.Image.RequestID != "req_1" {
		t.Fatalf("result %+v", out)
	}
	if strings.Join(seen, ",") != "IN_QUEUE,IN_QUEUE,IN_PROGRESS,COMPLETED" {
		t.Errorf("updates %v", seen)
	}
}

func TestSubscribeFallsBackToPollingAndFails(t *testing.T) {
	n := 0
	srv, _ := stub(t, map[string]http.HandlerFunc{
		"POST /queue/v1/m": jsonReply(200, map[string]any{"status": "IN_QUEUE", "request_id": "req_2"}),
		"GET /queue/v1/m/requests/req_2/status/stream": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) },
		"GET /queue/v1/m/requests/req_2/status": func(w http.ResponseWriter, r *http.Request) {
			n++
			status := "IN_PROGRESS"
			if n >= 2 {
				status = "FAILED"
			}
			jsonReply(200, map[string]any{"status": status, "request_id": "req_2", "error": map[string]any{"message": "boom", "code": "x"}})(w, r)
		},
	})
	_, err := newTestClient(t, srv).Subscribe(context.Background(), "m", Input{"prompt": "x"}, &SubscribeOptions{PollInterval: time.Millisecond})
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Message != "boom" {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestResultPendingVideoAndCancel(t *testing.T) {
	srv, calls := stub(t, map[string]http.HandlerFunc{
		"GET /queue/v1/m/requests/req_3":        jsonReply(202, map[string]any{"status": "IN_PROGRESS", "request_id": "req_3"}),
		"GET /queue/v1/m/requests/req_4":        jsonReply(200, map[string]any{"request_id": "req_4", "task_id": "t", "status": "completed", "video": map[string]any{"url": "https://x/v.mp4", "content_type": "video/mp4"}, "artifacts": []any{}}),
		"PUT /queue/v1/m/requests/req_3/cancel": jsonReply(200, map[string]any{"status": "CANCELLATION_REQUESTED", "request_id": "req_3"}),
	})
	c := newTestClient(t, srv)
	_, err := c.Queue.Result(context.Background(), "m", "req_3", 0)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "pending" {
		t.Fatalf("want pending, got %v", err)
	}
	r, err := c.Queue.Result(context.Background(), "m", "req_4", 30)
	if err != nil || r.Video == nil || r.Video.Video.URL != "https://x/v.mp4" {
		t.Fatalf("video result %+v %v", r, err)
	}
	if (*calls)[1].Query != "timeout=30" {
		t.Errorf("query %s", (*calls)[1].Query)
	}
	cr, err := c.Queue.Cancel(context.Background(), "m", "req_3")
	if err != nil || cr.RequestID != "req_3" {
		t.Fatalf("cancel %+v %v", cr, err)
	}
}

func TestModelsListHitsSiteWithoutKey(t *testing.T) {
	srv, calls := stub(t, map[string]http.HandlerFunc{
		"GET /api/models": jsonReply(200, map[string]any{"models": []any{map[string]any{"id": "a", "tasks": []string{"text-to-video"}}}}),
	})
	models, err := newTestClient(t, srv).Models.List(context.Background(), &ModelFilter{Modality: "video", Available: true})
	if err != nil || len(models) != 1 || models[0].ID != "a" || models[0].Tasks[0] != TextToVideo {
		t.Fatalf("models %+v %v", models, err)
	}
	c := (*calls)[0]
	if c.Query != "available=1&modality=video" || c.Header.Get("Authorization") != "" {
		t.Errorf("call %+v", c)
	}
}

func TestVerifyWebhook(t *testing.T) {
	secret := "whsec_test"
	body := []byte(`{"request_id":"req_1","gateway_request_id":"g","status":"OK","payload":{"ok":true}}`)
	ts := "1700000000"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + string(body)))
	h := http.Header{}
	h.Set("X-Tokport-Webhook-Timestamp", ts)
	h.Set("X-Tokport-Webhook-Signature", hex.EncodeToString(mac.Sum(nil)))
	now := &WebhookOptions{Now: time.Unix(1700000010, 0)}

	p, err := VerifyWebhook(body, h, secret, now)
	if err != nil || p.RequestID != "req_1" || string(p.Payload) != `{"ok":true}` {
		t.Fatalf("valid webhook rejected: %v", err)
	}
	if _, err := VerifyWebhook(append(body, ' '), h, secret, now); !errors.Is(err, ErrInvalidWebhook) {
		t.Error("tampered body accepted")
	}
	if _, err := VerifyWebhook(body, h, secret, &WebhookOptions{Now: time.Unix(1700009999, 0)}); !errors.Is(err, ErrInvalidWebhook) {
		t.Error("stale timestamp accepted")
	}
	if _, err := VerifyWebhook(body, h, "other", now); !errors.Is(err, ErrInvalidWebhook) {
		t.Error("wrong secret accepted")
	}
}
