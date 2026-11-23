package engine

import (
	"fmt"
	"strings"
)

func (e *Engine) quote(points int64, a Asset) RedeemQuote {
	value := points * PointValue
	boost := value * e.boostBps[a] / 10_000
	gross := value + boost
	net := gross - gross*routeFeeBps[a]/10_000
	return RedeemQuote{
		Asset: a, Label: assetLabel[a], Points: points, BoostBps: e.boostBps[a],
		Value: value, Boost: boost, Gross: gross, Net: net,
		Amount: float64(net) / float64(Micro) / price[a],
	}
}

// QuoteRedeem returns the value per target asset; the highest net value is marked best.
func (e *Engine) QuoteRedeem(points int64) []RedeemQuote {
	e.mu.Lock()
	defer e.mu.Unlock()
	points = max(0, points)
	out := make([]RedeemQuote, 0, len(Assets))
	best := 0
	for i, a := range Assets {
		out = append(out, e.quote(points, a))
		if out[i].Net > out[best].Net {
			best = i
		}
	}
	out[best].Best = true
	return out
}

type RedeemRequest struct {
	User   int    `json:"user"`
	Points int64  `json:"points"`
	Target Asset  `json:"target"`
	Source string `json:"source"`
}

func (e *Engine) Redeem(req RedeemRequest) (RedeemQuote, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	u, err := e.user(req.User)
	if err != nil {
		return RedeemQuote{}, err
	}
	src := req.Source
	if src != "agent card" {
		src = "manual"
	}
	return e.redeem(u, req.Points, req.Target, src)
}

// redeem mirrors RewardPool.RedeemPoints: burn POINT, pay from pool, route via Skip Go.
func (e *Engine) redeem(u *User, points int64, a Asset, source string) (RedeemQuote, error) {
	signer := u.Addr + " (wallet)"
	if strings.HasPrefix(source, "agent") {
		signer = "session key"
	}
	if !validAsset(a) {
		return RedeemQuote{}, errf("ERR_BAD_TARGET", "target must be ATOM, USDC_INJ or INJ")
	}
	if points <= 0 {
		return RedeemQuote{}, errf("ERR_BAD_AMOUNT", "enter a positive number of POINT")
	}
	if points > u.Points {
		return RedeemQuote{}, errf("ERR_INSUFFICIENT_POINT", "%s has %d P", u.Name, u.Points)
	}
	q := e.quote(points, a)
	e.newTx()
	var x *Error
	switch {
	case e.pool.Paused:
		x = errf("ERR_POOL_PAUSED", "redemptions are paused")
	case u.RedeemedToday+q.Gross > e.pool.DailyCap:
		x = errf("ERR_DAILY_REDEEM_CAP", "$%s > $%s per day", FormatUSDC(u.RedeemedToday+q.Gross), FormatUSDC(e.pool.DailyCap))
	case q.Gross > e.pool.Balance:
		x = errf("ERR_POOL_INSUFFICIENT", "pool $%s < $%s", FormatUSDC(e.pool.Balance), FormatUSDC(q.Gross))
	}
	if x != nil {
		e.log("bad", signer, "RewardPool.RedeemPoints", fmt.Sprintf("%s · %s · %s · POINT not burned", u.Name, x.Code, x.Message))
		return RedeemQuote{}, x
	}

	u.Points -= points
	u.RedeemedToday += q.Gross
	st := e.stats.Redeemed[a]
	st.Count++
	st.Points += points
	st.Value += q.Gross
	st.Amount += q.Amount
	e.stats.PointsBurned += points
	e.pool.Balance -= q.Gross
	e.pool.RedeemOut += q.Gross
	var route, note string
	switch a {
	case USDCINJ:
		u.USDC += q.Net
		u.NextEarnBonusBps = 100
		route = "credited to " + u.Name + "'s Vault"
		note = "+1% earn on next payment"
	case ATOM:
		u.ATOM += q.Amount
		route = "Skip Go injective-1 USDC.inj -> cosmoshub-4 uatom, IBC to " + u.CosmosAddr
		note = "boost +$" + FormatUSDC(q.Boost)
	case INJ:
		u.INJ += q.Amount
		route = "Skip Go USDC.inj -> INJ on injective-1 to " + u.Addr
		note = "boost +$" + FormatUSDC(q.Boost)
	}
	e.addHistory(u, &HistoryEntry{
		Type: "REDEEM", Status: "DONE", Desc: fmt.Sprintf("%d P -> %s %s", points, fmtAmount(a, q.Amount), q.Label),
		Note: note, Delta: fmt.Sprintf("-%d P", points),
	})
	e.log("skip", signer, "RewardPool.RedeemPoints", fmt.Sprintf("%s · %s · burn %d POINT · pool out $%s (boost $%s)\n%s · out %s %s",
		u.Name, source, points, FormatUSDC(q.Gross), FormatUSDC(q.Boost), route, fmtAmount(a, q.Amount), q.Label))
	return q, nil
}

func fmtAmount(a Asset, v float64) string {
	if a == USDCINJ {
		return fmt.Sprintf("%.2f", v)
	}
	return fmt.Sprintf("%.4f", v)
}

// runAgent decides the post-payment action from the user's policy.
func (e *Engine) runAgent(u *User) AgentCard {
	P, pol := u.Points, u.Policy
	if P < pol.Threshold {
		return AgentCard{Kind: "info", Message: fmt.Sprintf("%d P now. %d P more until the %d P threshold. Next order with POINT first saves $%s.",
			P, pol.Threshold-P, pol.Threshold, FormatUSDC(P*PointValue))}
	}
	qa, qu := e.quote(P, ATOM), e.quote(P, USDCINJ)
	if !pol.Auto {
		return AgentCard{Kind: "suggest", Points: P, Message: fmt.Sprintf("Your %d P to ATOM now: %.4f ATOM ($%s) with +%d%% boost. $%s more than USDC.inj.",
			P, qa.Amount, FormatUSDC(qa.Net), e.boostBps[ATOM]/100, FormatUSDC(qa.Net-qu.Net))}
	}
	parts := map[Asset]int64{}
	var used int64
	for _, a := range Assets {
		parts[a] = int64(float64(P) * pol.Weights[a])
		used += parts[a]
	}
	parts[ATOM] += P - used
	e.log("pt", "agent", "auto-redeem", fmt.Sprintf("%s · %d P >= threshold %d P", u.Name, P, pol.Threshold))
	card := AgentCard{Kind: "auto", Message: fmt.Sprintf("Auto-redeem at %d P threshold", pol.Threshold)}
	for _, a := range Assets {
		if parts[a] <= 0 {
			continue
		}
		q, err := e.redeem(u, parts[a], a, "agent auto-redeem")
		if err != nil {
			card.Lines = append(card.Lines, assetLabel[a]+": "+err.Error())
			continue
		}
		card.Lines = append(card.Lines, fmt.Sprintf("%d P -> %s %s", parts[a], fmtAmount(a, q.Amount), q.Label))
	}
	return card
}
