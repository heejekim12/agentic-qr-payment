package engine

import (
	"fmt"
	"math"
	"time"
)

// Redeem bonus guardrails enforced on every AI update.
const (
	boostMaxPct  = 15 // highest bonus the agent may set
	boostStepPct = 2  // largest change per update, in percentage points
)

// BoostState tells the operator how the current bonus was set.
type BoostState struct {
	Source  string    `json:"source"` // ai or rules
	Model   string    `json:"model,omitempty"`
	Reason  string    `json:"reason"`
	Note    string    `json:"note,omitempty"`
	Kind    string    `json:"kind"` // daily or emergency
	Updated time.Time `json:"updated"`
	// NextUpdate is the next scheduled daily update (00:00 KST); the bonus is fixed until then
	// unless an emergency cut is needed.
	NextUpdate time.Time `json:"next_update"`
}

type boostSuggestion struct {
	ATOM   int64  `json:"atom_pct"`
	INJ    int64  `json:"inj_pct"`
	Reason string `json:"reason"`
}

type boostFacts struct {
	PoolUSD      float64           `json:"reward_pool_usd"`
	LiabilityUSD float64           `json:"outstanding_points_usd"`
	CoveragePct  float64           `json:"coverage_pct"`
	RedeemedUSD  map[Asset]float64 `json:"redeemed_value_usd_by_asset"`
	RouteCostPct map[Asset]float64 `json:"route_cost_pct"`
	CurrentPct   map[Asset]int64   `json:"current_bonus_pct"`
	MaxPct       int               `json:"max_bonus_pct"`
	MaxStepPct   int               `json:"max_change_per_update_pct"`
}

const boostSystem = `You set redemption bonuses for a rewards program. Points redeem to ATOM, INJ or USDC.inj.
Goals, in order: keep the reward pool solvent, keep ATOM the best value after route cost, then INJ, then USDC.inj, and give ATOM a clear edge (about 5% net over USDC.inj) when the pool can afford it.
Constraints: integers; 0 <= inj_pct < atom_pct <= max_bonus_pct; reward_pool_usd >= outstanding_points_usd * (1 + atom_pct/100); each value may differ from current_bonus_pct by at most max_change_per_update_pct.
If ATOM's share of redeemed value is low, lean toward a larger ATOM edge. Reason: one short sentence with numbers from the facts.`

var boostSchema = map[string]any{
	"type": "object", "additionalProperties": false, "required": []string{"atom_pct", "inj_pct", "reason"},
	"properties": map[string]any{
		"atom_pct": map[string]any{"type": "integer"}, "inj_pct": map[string]any{"type": "integer"},
		"reason": map[string]any{"type": "string"},
	},
}

// boostFactsLocked collects the pool state; caller holds e.mu.
func (e *Engine) boostFactsLocked() boostFacts {
	f := boostFacts{
		PoolUSD: float64(e.pool.Balance) / float64(Micro), RedeemedUSD: map[Asset]float64{},
		RouteCostPct: map[Asset]float64{}, CurrentPct: map[Asset]int64{}, MaxPct: boostMaxPct, MaxStepPct: boostStepPct,
	}
	for _, u := range e.users {
		f.LiabilityUSD += float64(u.Points*PointValue) / float64(Micro)
	}
	if f.LiabilityUSD > 0 {
		f.CoveragePct = f.PoolUSD / f.LiabilityUSD * 100
	}
	for _, a := range Assets {
		f.RedeemedUSD[a] = float64(e.stats.Redeemed[a].Value) / float64(Micro)
		f.RouteCostPct[a] = float64(routeFeeBps[a]) / 100
		f.CurrentPct[a] = e.boostBps[a] / 100
	}
	return f
}

func netValue(bonusPct int64, routePct float64) float64 {
	return (1 + float64(bonusPct)/100) * (1 - routePct/100)
}

func validBoost(atom, inj int64, f boostFacts) error {
	switch {
	case atom < 0 || inj < 0 || atom > boostMaxPct:
		return fmt.Errorf("bonus out of range")
	case abs64(atom-f.CurrentPct[ATOM]) > boostStepPct || abs64(inj-f.CurrentPct[INJ]) > boostStepPct:
		return fmt.Errorf("change larger than %d points", boostStepPct)
	case !(netValue(atom, f.RouteCostPct[ATOM]) > netValue(inj, f.RouteCostPct[INJ]) && netValue(inj, f.RouteCostPct[INJ]) > 1):
		return fmt.Errorf("ATOM > INJ > USDC.inj order broken")
	case f.LiabilityUSD > 0 && f.PoolUSD < f.LiabilityUSD*(1+float64(atom)/100):
		return fmt.Errorf("pool would not cover outstanding points")
	}
	return nil
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

func clampStep(target, cur int64) int64 {
	return max(cur-boostStepPct, min(cur+boostStepPct, target))
}

// ruleBoost is the fallback: ATOM about 6 points over its route cost, capped by what the
// pool covers with a 20% buffer; INJ 4 points below ATOM; each moves at most boostStepPct.
func ruleBoost(f boostFacts) boostSuggestion {
	afford := int64(boostMaxPct)
	if f.LiabilityUSD > 0 {
		afford = int64(math.Floor((f.PoolUSD/(f.LiabilityUSD*1.2) - 1) * 100))
	}
	target := int64(math.Round(f.RouteCostPct[ATOM] + 6))
	total := 0.0
	for _, v := range f.RedeemedUSD {
		total += v
	}
	if total > 0 && f.RedeemedUSD[ATOM]/total < 0.5 {
		target++
	}
	target = max(0, min(target, boostMaxPct, afford))
	atom := clampStep(target, f.CurrentPct[ATOM])
	inj := clampStep(max(0, atom-4), f.CurrentPct[INJ])
	for inj > 0 && validBoost(atom, inj, f) != nil {
		inj--
	}
	return boostSuggestion{ATOM: atom, INJ: inj, Reason: fmt.Sprintf(
		"Coverage %.0f%% allows up to %d%%; ATOM kept clear of its %.1f%% route cost.", f.CoveragePct, max(afford, 0), f.RouteCostPct[ATOM])}
}

// quoteGrace protects users who saw a bonus just before it changed.
const quoteGrace = 2 * time.Minute

func nextMidnightKST(t time.Time) time.Time {
	k := t.In(kst)
	return time.Date(k.Year(), k.Month(), k.Day()+1, 0, 0, 0, 0, kst)
}

// effectiveBoostLocked returns the bonus to apply: right after a change, the better of
// the old and new value. Caller holds e.mu.
func (e *Engine) effectiveBoostLocked(a Asset) int64 {
	b := e.boostBps[a]
	if e.now().Sub(e.boostState.Updated) < quoteGrace {
		b = max(b, e.prevBoost[a])
	}
	return b
}

// maybeBoost runs on every price tick: the daily update at 00:00 KST (and at start),
// or an immediate cut when the pool no longer covers outstanding POINT plus the bonus.
func (e *Engine) maybeBoost() {
	e.mu.Lock()
	f := e.boostFactsLocked()
	due := e.boostState.NextUpdate.IsZero() || !e.now().Before(e.boostState.NextUpdate)
	short := f.LiabilityUSD > 0 && f.PoolUSD < f.LiabilityUSD*(1+float64(f.CurrentPct[ATOM])/100)
	e.mu.Unlock()
	switch {
	case due:
		e.autoBoost("daily", false)
	case short:
		e.autoBoost("emergency", true)
	}
}

// autoBoost lets the agent set the bonus: Claude when a key is set, rules otherwise or on any
// failure. An emergency update may only lower bonuses and keeps the next daily time.
func (e *Engine) autoBoost(kind string, lowerOnly bool) {
	e.mu.Lock()
	f := e.boostFactsLocked()
	e.mu.Unlock()

	s, st := ruleBoost(f), BoostState{Source: "rules", Kind: kind}
	if model, ok := agentModel(); !ok {
		st.Note = "No ANTHROPIC_API_KEY set; rule-based."
	} else {
		var ai boostSuggestion
		err := claudeJSON(model, boostSystem, f, boostSchema, &ai)
		if err == nil {
			err = validBoost(ai.ATOM, ai.INJ, f)
		}
		if err != nil {
			st.Note = "AI unavailable (" + err.Error() + "); rule-based."
		} else {
			s, st.Source, st.Model = ai, "ai", model
		}
	}
	if lowerOnly {
		s.ATOM, s.INJ = min(s.ATOM, f.CurrentPct[ATOM]), min(s.INJ, f.CurrentPct[INJ])
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	st.Reason, st.Updated = s.Reason, e.now()
	st.NextUpdate = nextMidnightKST(e.now())
	if lowerOnly {
		st.NextUpdate = e.boostState.NextUpdate
	}
	e.prevBoost = e.boostBps
	e.boostState = st
	if e.boostBps[ATOM] == s.ATOM*100 && e.boostBps[INJ] == s.INJ*100 {
		return
	}
	e.boostBps = map[Asset]int64{ATOM: s.ATOM * 100, USDCINJ: 0, INJ: s.INJ * 100}
	e.newTx()
	e.log("sec", "bonus agent ("+st.Source+")", "RewardPool.SetBoost", fmt.Sprintf("%s: ATOM +%d%% · INJ +%d%% · %s", kind, s.ATOM, s.INJ, s.Reason))
}
