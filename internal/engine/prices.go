package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

// PriceInfo is the ATOM USD price used to quote ATOM top-ups and payments.
type PriceInfo struct {
	USD     float64   `json:"usd"`
	Source  string    `json:"source"` // coingecko or sample
	Updated time.Time `json:"updated"`
}

const (
	sampleATOMUSD = 1.18 // used until CoinGecko answers
	priceRefresh  = 5 * time.Minute
)

// atomUSD returns the current ATOM price; caller holds e.mu.
func (e *Engine) atomUSD() float64 {
	if e.atomPrice.USD > 0 {
		return e.atomPrice.USD
	}
	return sampleATOMUSD
}

// StartPriceFeed fetches the ATOM price from CoinGecko now and every 5 minutes.
// On failure the last known (or sample) price stays in use.
func (e *Engine) StartPriceFeed(ctx context.Context) {
	go func() {
		t := time.NewTicker(priceRefresh)
		defer t.Stop()
		for {
			if err := e.refreshPrice(ctx); err != nil {
				log.Printf("price feed: %v (keeping previous price)", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func (e *Engine) refreshPrice(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	url := "https://api.coingecko.com/api/v3/simple/price?ids=cosmos&vs_currencies=usd"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if key := os.Getenv("COINGECKO_API_KEY"); key != "" {
		req.Header.Set("x-cg-demo-api-key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("coingecko status %d", resp.StatusCode)
	}
	var body map[string]map[string]float64
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("bad response: %w", err)
	}
	usd := body["cosmos"]["usd"]
	if usd <= 0 {
		return fmt.Errorf("missing price for cosmos")
	}
	e.mu.Lock()
	e.atomPrice = PriceInfo{USD: usd, Source: "coingecko", Updated: e.now()}
	e.mu.Unlock()
	return nil
}
