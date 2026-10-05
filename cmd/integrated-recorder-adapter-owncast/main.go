package main

import (
	"log"
	"os"

	"github.com/integrated-recorder/adapter-sdk-go/adapter"
	"github.com/integrated-recorder/source.owncast/internal/owncast"
)

func main() {
	if err := adapter.Serve(owncast.New()); err != nil {
		log.Printf("adapter stopped: %v", err)
		os.Exit(1)
	}
}
