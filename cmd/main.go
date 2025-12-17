package main

import (
	"flag"
	"github.com/JDRadatti/lightning/internal"
	"log"
	"net/http"
	"os"
)

var addr = flag.String("addr", ":8080", "http service address")

func main() {
	// Set up logging
	logFile, err := os.OpenFile("server.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("Failed to open log file: %v", err)
	}
	defer logFile.Close()
	log.SetOutput(logFile)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)

	// Start server
	flag.Parse()
	pm := internal.NewPartyManager()
	go pm.Run()
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		internal.ServeWs(pm, w, r)
	})
	err = http.ListenAndServe(*addr, nil)
	if err != nil {
		log.Fatal("Failed to ListenAndServe: ", err)
	}
}
