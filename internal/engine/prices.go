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

// PriceInfo is the USD price used for redemption quotes.
type PriceInfo struct {
	USD     float64   `json:"usd"`
	Source  string    `json:"source"` // coingecko or sample
	Updated time.Time `json:"updated"`
}

const priceRefresh = 5 * time.Minute

var coingeckoIDs = map[Asset]string{ATOM: "cosmos"}

// priceUSD returns the current price; caller holds e.mu.
func (e *Engine) priceUSD(a Asset) float64 {
	if p, ok := e.prices[a]; ok && p.USD > 0 {
		return p.USD
	}
	return price[a]
}

// StartPriceFeed fetches the ATOM price from CoinGecko now and every 5 minutes.
// On failure the last known (or sample) prices stay in use.
func (e *Engine) StartPriceFeed(ctx context.Context) {
	go func() {
		t := time.NewTicker(priceRefresh)
		defer t.Stop()
		for {
			if err := e.refreshPrices(ctx); err != nil {
				log.Printf("price feed: %v (keeping previous prices)", err)
			}
			e.maybeBoost()
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func (e *Engine) refreshPrices(ctx context.Context) error {
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
	now := e.now()
	got := map[Asset]PriceInfo{}
	for a, id := range coingeckoIDs {
		usd := body[id]["usd"]
		if usd <= 0 {
			return fmt.Errorf("missing price for %s", id)
		}
		got[a] = PriceInfo{USD: usd, Source: "coingecko", Updated: now}
	}
	e.mu.Lock()
	for a, p := range got {
		e.prices[a] = p
	}
	e.mu.Unlock()
	return nil
}
