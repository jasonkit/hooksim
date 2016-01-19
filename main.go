package main

import (
	"flag"
	"fmt"
	"hooksim/config"
	"hooksim/poller"
	"hooksim/webhook"
	"os"
	"os/signal"
	"time"
)

func parseFlag() (int, int, string) {
	port := flag.Int("p", 9000, "Listening port")
	interval := flag.Int("i", 5, "Polling interval for all repositories")
	conf := flag.String("c", "config.json", "Path to config file")
	dataDir := flag.String("d", "./data", "Path to data directory")
	flag.Parse()

	config.DataDir = *dataDir

	return *port, *interval, *conf
}

func main() {

	port, interval, conf := parseFlag()

	err := config.Load(conf)
	if err != nil {
		fmt.Printf("Error:%v\n", err)
		return
	}

	server := webhook.Server(port)
	p := poller.New(interval)

	go p.Run()
	go server.ListenAndServe()

	signalCh := make(chan os.Signal)
	signal.Notify(signalCh, os.Interrupt)

	for {
		sig := <-signalCh
		switch sig.String() {
		case "interrupt":
			fmt.Printf("shutting down...\n")

			webhookStopCh := server.StopChan()
			server.Stop(5 * time.Second)
			<-webhookStopCh

			p.Stop()
			<-p.StopDoneCh

			fmt.Printf("done\n")
			return
		}
	}
}
