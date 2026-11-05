// Command quickstart generates one image: TOKPORT_API_KEY=sk-... go run ./examples/quickstart
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
	out, err := client.Run(context.Background(), tokport.ModelGptImage2, tokport.Input{"prompt": "a lighthouse at dusk, watercolor", "size": "1024x1024"}, nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(out.Image.Data[0].URL)
}
