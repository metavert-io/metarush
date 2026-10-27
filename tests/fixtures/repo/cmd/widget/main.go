// Command widget watches a folder and turns new clips into videos.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"

	"example.invalid/widget/internal/queue"
)

func main() {
	dir := flag.String("watch", ".", "the folder to watch")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	q := queue.New()
	go queue.Watch(ctx, *dir, q)
	if err := queue.Run(ctx, q); err != nil {
		log.Fatal(err)
	}
}
