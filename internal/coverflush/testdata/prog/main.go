// A program that waits to be stopped, like the daemon: built with -cover by
// coverflush's test, it must leave its counters when it gets SIGTERM even
// though it never exits by itself.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/metavert-io/metarush/internal/coverflush"
)

func main() {
	stop := coverflush.OnSignal(os.Interrupt, syscall.SIGTERM)
	defer stop()
	held := make(chan os.Signal, 1)
	signal.Notify(held, os.Interrupt, syscall.SIGTERM) // the program's own handling: it keeps running
	fmt.Println("ready")
	select {}
}
