package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"time"

	"agentic-qr-payment/internal/api"
	"agentic-qr-payment/internal/engine"
	"agentic-qr-payment/web"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	flag.Parse()

	mux := http.NewServeMux()
	eng := engine.New()
	eng.StartPriceFeed(context.Background())
	api.Register(mux, eng)
	mux.Handle("GET /", http.FileServerFS(web.Files))

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second, // covers the 20 s advisor call
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("listening on http://%s", *addr)
	log.Fatal(srv.ListenAndServe())
}
