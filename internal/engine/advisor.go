package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
)

var kst = time.FixedZone("KST", 9*3600)

func minuteOfDay(t time.Time) int {
	t = t.In(kst)
	return t.Hour()*60 + t.Minute()
}

func promoActive(p *Promo, now time.Time) bool {
	m := minuteOfDay(now)
	return m >= p.StartMin && m < p.EndMin
}

func hhmm(min int) string { return fmt.Sprintf("%02d:%02d", min/60, min%60) }

// seedSample sets sample areas, spending profiles, wallet holdings and happy hours.
func (e *Engine) seedSample() {
	areas := map[string]string{"MRC_BAR_001": "Seongsu", "MRC_CAFE_002": "Yeonnam", "MRC_REST_014": "Euljiro", "MRC_REST_031": "Mangwon"}
	start := max(0, minuteOfDay(e.now())-30)
	promos := map[string]*Promo{
		"MRC_BAR_001":  {StartMin: 14 * 60, EndMin: 17 * 60, ExtraBps: 200},
		"MRC_CAFE_002": {StartMin: start, EndMin: min(24*60, start+120), ExtraBps: 200}, // window around server start so the demo has an active one
		"MRC_REST_014": {StartMin: 15 * 60, EndMin: 17 * 60, ExtraBps: 300},
	}
	for _, m := range e.merchants {
		m.Area, m.Promo = areas[m.ID], promos[m.ID]
	}
	type sample struct {
		area     string
		points   int64 // earned earlier at the store in the user's area
		avg      int64
		perWeek  int64
		holdings map[string]int64
	}
	samples := []sample{
		{"Seongsu", 1200, 6_500_000, 6, map[string]int64{"injective-1": 0, "8453": 120 * Micro, "1": 40 * Micro, "solana": 0, "cosmoshub-4": 30 * Micro}},
		{"Euljiro", 80, 14 * Micro, 3, map[string]int64{"injective-1": 50 * Micro, "8453": 0, "1": 200 * Micro, "solana": 0, "cosmoshub-4": 0}},
		{"Yeonnam", 3400, 9 * Micro, 4, map[string]int64{"injective-1": 0, "8453": 0, "1": 10 * Micro, "solana": 80 * Micro, "cosmoshub-4": 60 * Micro}},
	}
	for i, u := range e.users {
		s := samples[i%len(samples)]
		u.Area, u.Profile, u.Holdings = s.area, Profile{AvgOrder: s.avg, PerWeek: s.perWeek}, s.holdings
		for _, m := range e.merchants {
			if m.Area == u.Area && m.Status == StatusActive {
				u.Points[m.ID] = s.points
			}
		}
	}
}

// AdviceItem is one suggestion. The agent never executes; Action only pre-fills a screen.
type AdviceItem struct {
	Kind       string `json:"kind"` // points, topup, promo
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	Reason     string `json:"reason"`
	Action     string `json:"action,omitempty"` // topup
	Amount     int64  `json:"amount_micro,omitempty"`
	Chain      string `json:"chain,omitempty"`
	MerchantID string `json:"merchant_id,omitempty"`
}

type Advice struct {
	Source string       `json:"source"` // ai or rules
	Model  string       `json:"model,omitempty"`
	Note   string       `json:"note,omitempty"`
	Items  []AdviceItem `json:"items"`
}

type adviceFacts struct {
	Points        []storePoints      `json:"points_by_store"`
	PointValueUSD float64            `json:"point_value_usd"`
	BalanceUSD    float64            `json:"balance_usd"`
	AvgOrderUSD   float64            `json:"avg_order_usd"`
	OrdersPerWeek int64              `json:"orders_per_week"`
	Area          string             `json:"area"`
	HoldingsUSD   map[string]float64 `json:"wallet_usd_by_chain"` // ATOM on cosmoshub-4 at its USD value
	RouteFeeBps   map[string]int64   `json:"topup_fee_bps_by_chain"`
	ChainNames    map[string]string  `json:"chain_names"`
	Promos        []promoFact        `json:"promos_today"`
	NowKST        string             `json:"now_kst"`
}

type storePoints struct {
	Name   string `json:"name"`
	Points int64  `json:"points"`
}

type promoFact struct {
	MerchantID string  `json:"merchant_id"`
	Name       string  `json:"name"`
	Area       string  `json:"area"`
	Window     string  `json:"window_kst"`
	ExtraPct   float64 `json:"extra_points_pct"`
	ActiveNow  bool    `json:"active_now"`
}

type adviceDecision struct {
	Topup struct {
		Needed    bool    `json:"needed"`
		AmountUSD float64 `json:"amount_usd"`
		Chain     string  `json:"chain"`
		Reason    string  `json:"reason"`
	} `json:"topup"`
	Promo struct {
		MerchantID string `json:"merchant_id"`
		Reason     string `json:"reason"`
	} `json:"promo"`
}

// Advise returns the agent's three suggestions for a user. It asks Claude when
// ANTHROPIC_API_KEY is set and falls back to rules otherwise or on any failure.
func (e *Engine) Advise(req DisconnectRequest) (*Advice, error) {
	e.mu.Lock()
	u, err := e.user(req.User)
	if err != nil {
		e.mu.Unlock()
		return nil, err
	}
	facts := e.facts(u)
	names := map[string]string{}
	for _, m := range e.merchants {
		names[m.ID] = m.Name
	}
	e.mu.Unlock()

	adv := &Advice{Source: "rules"}
	dec := ruleDecision(facts)
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		adv.Note = "No ANTHROPIC_API_KEY set; showing rule-based advice."
	} else {
		model := os.Getenv("AGENT_MODEL")
		if model == "" {
			model = "claude-opus-5-5"
		}
		ai, err := askClaude(model, facts)
		if err == nil {
			err = validDecision(ai, facts)
		}
		if err != nil {
			adv.Note = "AI advice unavailable (" + err.Error() + "); showing rule-based advice."
		} else {
			dec, adv.Source, adv.Model = ai, "ai", model
		}
	}
	adv.Items = decisionItems(dec, facts, names)
	return adv, nil
}

func (e *Engine) facts(u *User) adviceFacts {
	f := adviceFacts{
		PointValueUSD: 0.01,
		BalanceUSD:    (float64(u.USDC) + float64(u.ATOM)*e.atomUSD()) / float64(Micro), AvgOrderUSD: float64(u.Profile.AvgOrder) / float64(Micro),
		OrdersPerWeek: u.Profile.PerWeek, Area: u.Area,
		HoldingsUSD: map[string]float64{}, RouteFeeBps: map[string]int64{}, ChainNames: map[string]string{},
		NowKST: e.now().In(kst).Format("15:04"),
	}
	for id, r := range topupChains {
		f.HoldingsUSD[id] = float64(u.Holdings[id]) / float64(Micro)
		f.RouteFeeBps[id] = r.FeeBps
		f.ChainNames[id] = r.Name
	}
	for _, m := range e.merchants {
		if p := u.Points[m.ID]; p > 0 {
			f.Points = append(f.Points, storePoints{Name: m.Name, Points: p})
		}
	}
	now := minuteOfDay(e.now())
	for _, m := range e.merchants {
		if m.Promo == nil || m.Status != StatusActive || now >= m.Promo.EndMin {
			continue
		}
		f.Promos = append(f.Promos, promoFact{
			MerchantID: m.ID, Name: m.Name, Area: m.Area,
			Window:   hhmm(m.Promo.StartMin) + "-" + hhmm(m.Promo.EndMin),
			ExtraPct: float64(m.Promo.ExtraBps) / 100, ActiveNow: promoActive(m.Promo, e.now()),
		})
	}
	return f
}

// ruleDecision is the deterministic baseline: top up to a week of spending from the
// cheapest chain that covers it, and pick the best happy hour.
func ruleDecision(f adviceFacts) adviceDecision {
	var d adviceDecision
	weekly := f.AvgOrderUSD * float64(f.OrdersPerWeek)
	if f.BalanceUSD < weekly {
		need := float64(int((weekly-f.BalanceUSD)/10)+1) * 10
		d.Topup.Needed, d.Topup.AmountUSD = true, need
		chains := make([]string, 0, len(f.RouteFeeBps))
		for id := range f.RouteFeeBps {
			chains = append(chains, id)
		}
		slices.SortFunc(chains, func(a, b string) int { return int(f.RouteFeeBps[a] - f.RouteFeeBps[b]) })
		for _, id := range chains {
			if f.HoldingsUSD[id] >= need {
				d.Topup.Chain = id
				break
			}
		}
		if d.Topup.Chain == "" { // no single chain covers it: use the largest holding
			for _, id := range chains {
				if d.Topup.Chain == "" || f.HoldingsUSD[id] > f.HoldingsUSD[d.Topup.Chain] {
					d.Topup.Chain = id
				}
			}
		}
		d.Topup.Reason = fmt.Sprintf("Your balance covers about %.0f of your %d orders a week.", f.BalanceUSD/max(f.AvgOrderUSD, 0.01), f.OrdersPerWeek)
	} else {
		d.Topup.Reason = "Your balance covers a week of your usual orders."
	}

	best, score := "", -1
	for _, p := range f.Promos {
		s := 0
		if p.ActiveNow {
			s += 2
		}
		if p.Area == f.Area {
			s++
		}
		if s > score {
			best, score = p.MerchantID, s
		}
	}
	d.Promo.MerchantID = best
	if best != "" {
		d.Promo.Reason = "Extra points during a quiet hour; stores open now and nearby come first."
	}
	return d
}

func validDecision(d adviceDecision, f adviceFacts) error {
	if d.Topup.Needed {
		if _, ok := f.RouteFeeBps[d.Topup.Chain]; !ok {
			return fmt.Errorf("unknown chain %q", d.Topup.Chain)
		}
		if d.Topup.AmountUSD <= 0 || d.Topup.AmountUSD > 500 {
			return fmt.Errorf("top-up amount out of range")
		}
	}
	if d.Promo.MerchantID != "" && !slices.ContainsFunc(f.Promos, func(x promoFact) bool { return x.MerchantID == d.Promo.MerchantID }) {
		return fmt.Errorf("unknown promo store %q", d.Promo.MerchantID)
	}
	return nil
}

func decisionItems(d adviceDecision, f adviceFacts, names map[string]string) []AdviceItem {
	var items []AdviceItem
	if len(f.Points) > 0 {
		top := slices.MaxFunc(f.Points, func(a, b storePoints) int { return int(a.Points - b.Points) })
		items = append(items, AdviceItem{
			Kind: "points", Title: fmt.Sprintf("$%.2f off at %s", float64(top.Points)*f.PointValueUSD, top.Name),
			Detail: fmt.Sprintf("%d points, usable on your next order there.", top.Points),
			Reason: "Points work only at the store that gave them.",
		})
	}

	tu := AdviceItem{Kind: "topup", Reason: d.Topup.Reason}
	if d.Topup.Needed {
		fee := d.Topup.AmountUSD * float64(f.RouteFeeBps[d.Topup.Chain]) / 10_000
		tu.Title = fmt.Sprintf("Top up $%.0f before your next visit", d.Topup.AmountUSD)
		tu.Detail = fmt.Sprintf("From %s, fee about $%.2f.", f.ChainNames[d.Topup.Chain], fee)
		tu.Action, tu.Amount, tu.Chain = "topup", int64(d.Topup.AmountUSD*float64(Micro)), d.Topup.Chain
	} else {
		tu.Title = "No top-up needed"
		tu.Detail = fmt.Sprintf("Balance $%.2f.", f.BalanceUSD)
	}
	items = append(items, tu)

	for _, p := range f.Promos {
		if p.MerchantID != d.Promo.MerchantID {
			continue
		}
		when := "today " + p.Window
		if p.ActiveNow {
			when = "now until " + p.Window[6:]
		}
		near := ""
		if p.Area == f.Area {
			near = " · near you"
		}
		items = append(items, AdviceItem{
			Kind: "promo", Title: fmt.Sprintf("%s: +%.0f%% points %s", names[p.MerchantID], p.ExtraPct, when),
			Detail: p.Area + near, Reason: d.Promo.Reason, MerchantID: p.MerchantID,
		})
	}
	return items
}

const advisorSystem = `You are the advisor inside a payment app. You only advise; the user decides and executes.
Given facts about one user, decide:
1. topup: whether the balance is likely to run short within a week of usual orders, and if so how much (whole dollars, at most 500) and from which chain id. Pick a chain where the wallet holds at least the amount, preferring the lowest fee.
2. promo: one merchant_id from promos_today worth visiting (prefer active now and the same area), or "" if none.
Points are discounts usable only at the store that gave them. Do not predict prices. Never suggest buying or holding an asset for gains, and never mention returns or price movements. Each reason is at most 12 words a customer understands, using numbers from the facts.`

var adviceSchema = map[string]any{
	"type": "object", "additionalProperties": false,
	"required": []string{"topup", "promo"},
	"properties": map[string]any{
		"topup": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"needed", "amount_usd", "chain", "reason"},
			"properties": map[string]any{
				"needed": map[string]any{"type": "boolean"}, "amount_usd": map[string]any{"type": "number"},
				"chain": map[string]any{"type": "string"}, "reason": map[string]any{"type": "string"},
			},
		},
		"promo": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"merchant_id", "reason"},
			"properties": map[string]any{"merchant_id": map[string]any{"type": "string"}, "reason": map[string]any{"type": "string"}},
		},
	},
}

// askClaude sends only the computed facts (no addresses or keys) and parses the structured reply.
func askClaude(model string, f adviceFacts) (adviceDecision, error) {
	var d adviceDecision
	return d, claudeJSON(model, advisorSystem, f, adviceSchema, &d)
}

// claudeJSON sends facts as JSON and decodes a structured (schema-constrained) reply into out.
func claudeJSON(model, system string, facts any, schema map[string]any, out any) error {
	factsJSON, err := json.Marshal(facts)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := anthropic.NewClient()
	resp, err := client.Beta.Messages.New(ctx, anthropic.BetaMessageNewParams{
		Model:     model,
		MaxTokens: 4000,
		System:    []anthropic.BetaTextBlockParam{{Text: system}},
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock("Facts:\n" + string(factsJSON))),
		},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Effort: anthropic.BetaOutputConfigEffortLow,
			Format: anthropic.BetaJSONOutputFormatParam{Schema: schema},
		},
		Fallbacks: anthropic.BetaFallbacksParamOfDefault(),
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
	})
	if err != nil {
		return fmt.Errorf("request failed")
	}
	if resp.StopReason == anthropic.BetaStopReasonRefusal {
		return fmt.Errorf("model declined")
	}
	for _, b := range resp.Content {
		if t, ok := b.AsAny().(anthropic.BetaTextBlock); ok {
			if err := json.Unmarshal([]byte(t.Text), out); err != nil {
				return fmt.Errorf("unreadable reply")
			}
			return nil
		}
	}
	return fmt.Errorf("empty reply")
}
