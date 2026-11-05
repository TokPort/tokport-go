# TokPort Go SDK

[![Go Reference](https://pkg.go.dev/badge/github.com/TokPort/tokport-go.svg)](https://pkg.go.dev/github.com/TokPort/tokport-go)
[![CI](https://github.com/TokPort/tokport-go/actions/workflows/ci.yml/badge.svg)](https://github.com/TokPort/tokport-go/actions/workflows/ci.yml)

The official Go client for **[TokPort](https://tokport.io/?utm_source=pkg.go.dev&utm_medium=readme&utm_campaign=tokport-go)**, the pay-as-you-go API for image and video generation. One API key gives you GPT Image, Nano Banana, Seedream, Seedance, Kling, Qwen Image, Wan and [30+ more models](https://tokport.io/models?utm_source=pkg.go.dev&utm_medium=readme&utm_campaign=tokport-go).

- Sync calls: images come back inline. For videos the client submits the task, polls it and downloads the clip
- A queue with live status (server-sent events, with polling as a fallback), webhooks and cancellation
- Uses only the standard library and works with `context.Context`. Model ids are constants, so editors can autocomplete them

```bash
go get github.com/TokPort/tokport-go
```

Get an API key at **[tokport.io/dashboard/keys](https://tokport.io/dashboard/keys?utm_source=pkg.go.dev&utm_medium=readme&utm_campaign=tokport-go)**. New accounts can [sign up here](https://tokport.io/register?utm_source=pkg.go.dev&utm_medium=readme&utm_campaign=tokport-go).

## Quickstart

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/TokPort/tokport-go"
)

func main() {
	client, err := tokport.NewClient(os.Getenv("TOKPORT_API_KEY"))
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	// Image: answers inline
	img, err := client.Run(ctx, tokport.ModelGptImage2, tokport.Input{"prompt": "a neon koi in a rainy alley", "size": "1024x1024"}, nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(img.Image.Data[0].URL)

	// Video: submits, polls and downloads the MP4 for you
	vid, err := client.Run(ctx, tokport.ModelSeedance20Fast, tokport.Input{"prompt": "waves crashing at night", "seconds": "5"}, nil)
	if err != nil {
		log.Fatal(err)
	}
	_ = os.WriteFile("waves.mp4", vid.Video.Bytes, 0o644)
}
```

## Queue: progress updates, webhooks, cancellation

```go
res, err := client.Subscribe(ctx, tokport.ModelKling30, tokport.Input{"prompt": "a paper boat drifting down a stream", "seconds": "5"}, &tokport.SubscribeOptions{
	OnQueueUpdate: func(s tokport.QueueStatus) { fmt.Println(s.Status) },
})
if err != nil {
	log.Fatal(err)
}
fmt.Println(res.Video.Video.URL)

// Or step by step
req, _ := client.Queue.Submit(ctx, tokport.ModelSeedream5Pro, tokport.Input{"prompt": "a travel poster of Kyoto"}, &tokport.SubmitOptions{WebhookURL: "https://example.com/hook"})
_ = client.Queue.Stream(ctx, tokport.ModelSeedream5Pro, req.RequestID, false, func(s tokport.QueueStatus) bool {
	fmt.Println(s.Status)
	return true
})
out, _ := client.Queue.Result(ctx, tokport.ModelSeedream5Pro, req.RequestID, 30)
fmt.Println(out.Image.Data[0].URL)
```

## Image inputs (edits, image-to-video)

```go
photo, _ := os.ReadFile("me.jpg")
edited, err := client.Run(ctx, tokport.ModelSeedream5Pro, tokport.Input{
	"prompt": "make it a watercolor",
	"image":  tokport.ToDataURL(photo, "image/jpeg"),
}, nil)
```

## Verify webhooks

```go
func hook(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	payload, err := tokport.VerifyWebhook(body, r.Header, os.Getenv("TOKPORT_WEBHOOK_SECRET"), nil)
	if err != nil {
		http.Error(w, "bad signature", http.StatusBadRequest)
		return
	}
	fmt.Println(payload.RequestID, payload.Status)
}
```

## Models

Model ids that were live when this release was built are constants (`tokport.ModelGptImage2`, `tokport.ModelKling30`, …). Any newer id from the [model catalog](https://tokport.io/models?utm_source=pkg.go.dev&utm_medium=readme&utm_campaign=tokport-go) works as a plain string. You can also query the catalog at runtime:

```go
models, _ := client.Models.List(ctx, &tokport.ModelFilter{Modality: "video"})
```

| Image | Video |
| --- | --- |
| [GPT Image 2](https://tokport.io/models/gpt-image-2?utm_source=pkg.go.dev&utm_medium=readme&utm_campaign=tokport-go) | [Seedance 2.5](https://tokport.io/models/seedance-2-5?utm_source=pkg.go.dev&utm_medium=readme&utm_campaign=tokport-go) |
| [Nano Banana 3 Pro](https://tokport.io/models/nano-banana-3-pro?utm_source=pkg.go.dev&utm_medium=readme&utm_campaign=tokport-go) | [Seedance 2.0 Fast](https://tokport.io/models/seedance-2-0-fast?utm_source=pkg.go.dev&utm_medium=readme&utm_campaign=tokport-go) |
| [Seedream 5 Pro](https://tokport.io/models/seedream-5-pro?utm_source=pkg.go.dev&utm_medium=readme&utm_campaign=tokport-go) | [Kling 3.0](https://tokport.io/models/kling-3-0?utm_source=pkg.go.dev&utm_medium=readme&utm_campaign=tokport-go) |
| [Qwen Image 3.0](https://tokport.io/models/qwen-image-v3?utm_source=pkg.go.dev&utm_medium=readme&utm_campaign=tokport-go) | [Wan 3.0 Video](https://tokport.io/models/wan3-video?utm_source=pkg.go.dev&utm_medium=readme&utm_campaign=tokport-go) |
| [Grok Image 2.0](https://tokport.io/models/grok-image-2-0?utm_source=pkg.go.dev&utm_medium=readme&utm_campaign=tokport-go) | [HappyHorse 1.1](https://tokport.io/models/happyhorse-1-1?utm_source=pkg.go.dev&utm_medium=readme&utm_campaign=tokport-go) |

Live per-model rates: [tokport.io/pricing](https://tokport.io/pricing?utm_source=pkg.go.dev&utm_medium=readme&utm_campaign=tokport-go).

## Errors

API errors are `*tokport.Error`, with `Status`, `Code`, `Message` and `RequestID` (include the request id when you contact support):

```go
var apiErr *tokport.Error
if errors.As(err, &apiErr) && apiErr.Status == 402 {
	// top up at https://tokport.io/dashboard
}
```

## Links

- Docs: [tokport.io/docs](https://tokport.io/docs?utm_source=pkg.go.dev&utm_medium=readme&utm_campaign=tokport-go)
- Models: [tokport.io/models](https://tokport.io/models?utm_source=pkg.go.dev&utm_medium=readme&utm_campaign=tokport-go)
- Pricing: [tokport.io/pricing](https://tokport.io/pricing?utm_source=pkg.go.dev&utm_medium=readme&utm_campaign=tokport-go)
- Other SDKs: [TypeScript](https://github.com/TokPort/tokport-js) · [Python](https://github.com/TokPort/tokport-python)

## License

MIT
