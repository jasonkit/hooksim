package main

import (
	"fmt"
	"hooksim/config"
	"hooksim/webhook"
	"os"
	"os/signal"
	"time"
)

func main() {
	err := config.Load("./config.json")
	if err != nil {
		fmt.Printf("Error:%v\n", err)
		return
	}

	server := webhook.Server(9000)

	go server.ListenAndServe()

	signalCh := make(chan os.Signal)
	signal.Notify(signalCh, os.Interrupt)

	for {
		sig := <-signalCh
		switch sig.String() {
		case "interrupt":
			fmt.Printf("shutting down...\n")
			server.Stop(5 * time.Second)
			fmt.Printf("done\n")
			return
		}
	}
}
