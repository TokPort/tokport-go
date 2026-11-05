package tokport

import (
	"encoding/json"
	"fmt"
)

// TaskKind selects the endpoint for a request.
type TaskKind string

const (
	TextToImage  TaskKind = "text-to-image"
	ImageToImage TaskKind = "image-to-image"
	TextToVideo  TaskKind = "text-to-video"
	ImageToVideo TaskKind = "image-to-video"
)

// QueueState is the state of a queued request.
type QueueState string

const (
	InQueue    QueueState = "IN_QUEUE"
	InProgress QueueState = "IN_PROGRESS"
	Completed  QueueState = "COMPLETED"
	Failed     QueueState = "FAILED"
	Cancelled  QueueState = "CANCELLED"
)

// Terminal reports whether no further status updates will follow.
func (s QueueState) Terminal() bool { return s == Completed || s == Failed || s == Cancelled }

// Input is the request body for a model: prompt, size, seconds, image, … See each model's page
// on https://tokport.io/models for its parameters.
type Input map[string]any

// ErrorInfo is the error object inside API responses.
type ErrorInfo struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

// LogLine is a worker log entry (QueueStatus.Logs, when requested).
type LogLine struct {
	Timestamp string `json:"timestamp"`
	Message   string `json:"message"`
}

// QueueStatus describes a queued request.
type QueueStatus struct {
	Status           QueueState         `json:"status"`
	RequestID        string             `json:"request_id"`
	ResponseURL      string             `json:"response_url"`
	StatusURL        string             `json:"status_url"`
	CancelURL        string             `json:"cancel_url"`
	QueuePosition    *int               `json:"queue_position,omitempty"`
	GatewayRequestID string             `json:"gateway_request_id,omitempty"`
	Logs             []LogLine          `json:"logs,omitempty"`
	Metrics          map[string]float64 `json:"metrics,omitempty"`
	Error            *ErrorInfo         `json:"error,omitempty"`
}

// ImageData is one generated image.
type ImageData struct {
	URL           string `json:"url,omitempty"`
	B64JSON       string `json:"b64_json,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

// ImageResult is the OpenAI-style images response.
type ImageResult struct {
	Created   int64       `json:"created,omitempty"`
	Data      []ImageData `json:"data"`
	RequestID string      `json:"request_id,omitempty"`
}

// VideoFile is the finished clip of a queued video request.
type VideoFile struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
}

// Artifact is an extra output file of a queued request.
type Artifact struct {
	Key         string `json:"key"`
	Type        string `json:"type"`
	ContentType string `json:"content_type"`
	URL         string `json:"url"`
}

// VideoResult is the queue result of a video request.
type VideoResult struct {
	RequestID string     `json:"request_id"`
	TaskID    string     `json:"task_id"`
	Status    string     `json:"status"`
	Video     *VideoFile `json:"video"`
	Artifacts []Artifact `json:"artifacts"`
	Seconds   string     `json:"seconds,omitempty"`
	Size      string     `json:"size,omitempty"`
}

// VideoTask is the /v1/videos task object.
type VideoTask struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	Progress    int        `json:"progress"`
	Model       string     `json:"model,omitempty"`
	CreatedAt   int64      `json:"created_at,omitempty"`
	CompletedAt int64      `json:"completed_at,omitempty"`
	Error       *ErrorInfo `json:"error,omitempty"`
}

// VideoRunResult is what Run returns for a video model: the task plus the downloaded clip.
type VideoRunResult struct {
	VideoTask
	Bytes       []byte
	ContentType string
}

// RunResult holds the outcome of Run: Image for image models, Video for video models.
type RunResult struct {
	Image *ImageResult
	Video *VideoRunResult
}

// Result holds a queue result: Image for image models, Video for video models. Raw is the
// response body as received.
type Result struct {
	Image *ImageResult
	Video *VideoResult
	Raw   json.RawMessage
}

func decodeResult(raw json.RawMessage) (*Result, error) {
	var probe struct {
		Data   json.RawMessage `json:"data"`
		TaskID string          `json:"task_id"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	r := &Result{Raw: raw}
	if probe.Data != nil {
		r.Image = &ImageResult{}
		return r, json.Unmarshal(raw, r.Image)
	}
	r.Video = &VideoResult{}
	return r, json.Unmarshal(raw, r.Video)
}

// Pricing is a model's list price.
type Pricing struct {
	Price    float64 `json:"price"`
	Unit     string  `json:"unit"`
	Currency string  `json:"currency"`
}

// ModelInfo is a catalog entry from https://tokport.io/models.
type ModelInfo struct {
	ID          string     `json:"id"`
	DisplayName string     `json:"display_name"`
	Vendor      string     `json:"vendor"`
	Modality    string     `json:"modality"`
	Category    TaskKind   `json:"category"`
	Tasks       []TaskKind `json:"tasks"`
	Tags        []string   `json:"tags"`
	Description string     `json:"description"`
	Status      string     `json:"status"`
	Pricing     *Pricing   `json:"pricing"`
	Added       string     `json:"added"`
	Endpoints   []struct {
		Task TaskKind `json:"task"`
		Path string   `json:"path"`
	} `json:"endpoints"`
	URLs struct {
		Page       string `json:"page"`
		Playground string `json:"playground"`
		API        string `json:"api"`
		OpenAPI    string `json:"openapi"`
		LLMsTxt    string `json:"llms_txt"`
	} `json:"urls"`
	ThumbnailURL string `json:"thumbnail_url"`
}

// Error is returned for API errors and for requests that failed, were cancelled or timed out.
type Error struct {
	Status    int
	Code      string
	Message   string
	RequestID string
	Body      json.RawMessage
}

func (e *Error) Error() string {
	return fmt.Sprintf("tokport: %s (status %d, code %s)", e.Message, e.Status, e.Code)
}
