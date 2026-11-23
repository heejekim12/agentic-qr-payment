package engine

import "fmt"

type TopupRequest struct {
	User   int     `json:"user"`
	Chain  string  `json:"chain"`
	Amount float64 `json:"amount_usdc"`
}

func quoteTopup(chain string, amount float64) (TopupQuote, error) {
	r, ok := topupChains[chain]
	if !ok {
		return TopupQuote{}, errf("ERR_UNKNOWN_CHAIN", "chain %q is not supported", chain)
	}
	amt, err := toMicro(amount)
	if err != nil {
		return TopupQuote{}, err
	}
	fee := amt * r.FeeBps / 10_000
	return TopupQuote{Chain: chain, Name: r.Name, Denom: r.Denom, Amount: amt, Fee: fee, Out: amt - fee}, nil
}

// QuoteTopup mirrors SkipClient.route() for USDC -> USDC.inj.
func (e *Engine) QuoteTopup(req TopupRequest) (TopupQuote, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.user(req.User); err != nil {
		return TopupQuote{}, err
	}
	q, err := quoteTopup(req.Chain, req.Amount)
	if err != nil {
		return q, err
	}
	return q, nil
}

// Topup mirrors SkipClient.executeRoute() with Vault.DepositUsdc as the post-route action.
func (e *Engine) Topup(req TopupRequest) (TopupQuote, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	u, err := e.user(req.User)
	if err != nil {
		return TopupQuote{}, err
	}
	q, err := quoteTopup(req.Chain, req.Amount)
	if err != nil {
		return q, err
	}
	e.newTx()
	u.USDC += q.Out
	e.stats.TopUps++
	e.stats.TopUpVolume += q.Out
	if q.Chain == "injective-1" {
		e.addHistory(u, &HistoryEntry{Type: "TOPUP", Status: "DONE", Credit: true, Desc: "From Injective wallet", Delta: "+" + FormatUSDC(q.Out)})
		e.log("ok", u.Addr+" (wallet)", "Vault.DepositUsdc", fmt.Sprintf("%s Vault +%s USDC (direct deposit)", u.Name, FormatUSDC(q.Out)))
		return q, nil
	}
	e.addHistory(u, &HistoryEntry{Type: "TOPUP", Status: "DONE", Credit: true, Desc: "USDC on " + q.Name + " via Skip:Go", Delta: "+" + FormatUSDC(q.Out)})
	e.log("skip", u.Name+" wallet", "SkipClient.executeRoute()", fmt.Sprintf("signed on %s (%s); post-route action Vault.DepositUsdc for %s", q.Name, q.Chain, u.Addr))
	e.log("ok", "Skip:Go relayer", "Vault.DepositUsdc", fmt.Sprintf("%s Vault +%s USDC", u.Name, FormatUSDC(q.Out)))
	return q, nil
}

type WithdrawRequest struct {
	User   int     `json:"user"`
	Amount float64 `json:"amount_usdc"`
}

// Withdraw is owner-signed only; session keys and pool pause cannot affect it.
func (e *Engine) Withdraw(req WithdrawRequest) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	u, err := e.user(req.User)
	if err != nil {
		return err
	}
	amt, err := toMicro(req.Amount)
	if err != nil {
		return err
	}
	if amt > u.USDC {
		return errf("ERR_INSUFFICIENT_VAULT", "Vault balance is %s USDC.inj", FormatUSDC(u.USDC))
	}
	e.newTx()
	u.USDC -= amt
	e.addHistory(u, &HistoryEntry{Type: "WITHDRAW", Status: "DONE", Desc: "Vault -> " + u.Addr, Delta: "-" + FormatUSDC(amt)})
	e.log("ok", u.Addr+" (wallet)", "Vault.Withdraw", fmt.Sprintf("%s · %s USDC.inj -> owner. Session keys cannot call this.", u.Name, FormatUSDC(amt)))
	return nil
}

type SessionRequest struct {
	User   int  `json:"user"`
	Active bool `json:"active"`
}

func (e *Engine) SetSession(req SessionRequest) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	u, err := e.user(req.User)
	if err != nil {
		return err
	}
	e.newTx()
	u.Session.Active = req.Active
	if req.Active {
		e.log("ok", u.Addr+" (wallet)", "Vault.RegisterSession",
			fmt.Sprintf("%s · registry ACTIVE merchants only · $%s/tx · $%s/day · expires in 30d\n+ MsgGrantAllowance (AllowedMsgAllowance[MsgExecuteContract] + PeriodicAllowance)",
				u.Name, FormatUSDC(u.Session.PerTx), FormatUSDC(u.Session.Daily)))
	} else {
		e.log("sec", u.Addr+" (wallet)", "Vault.RevokeSession + MsgRevokeAllowance", u.Name+" session key removed. Funds stay in the Vault.")
	}
	return nil
}

type PolicyRequest struct {
	User      int               `json:"user"`
	PerTx     float64           `json:"per_tx_usdc"`
	Daily     float64           `json:"daily_usdc"`
	Auto      bool              `json:"auto"`
	Threshold int64             `json:"threshold"`
	Weights   map[Asset]float64 `json:"weights"`
}

func (e *Engine) SetPolicy(req PolicyRequest) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	u, err := e.user(req.User)
	if err != nil {
		return err
	}
	sum := 0.0
	for a, w := range req.Weights {
		if !validAsset(a) || !(w >= 0) {
			return errf("ERR_BAD_POLICY", "weights must be 0 or more for ATOM, USDC_INJ, INJ")
		}
		sum += w
	}
	if sum < 0.999999 || sum > 1.000001 {
		return errf("ERR_BAD_POLICY", "weights must add up to 1 (now %.2f)", sum)
	}
	if req.Threshold < 1 {
		return errf("ERR_BAD_POLICY", "threshold must be at least 1 POINT")
	}
	perTx, err := toMicro(req.PerTx)
	if err != nil {
		return err
	}
	daily, err := toMicro(req.Daily)
	if err != nil {
		return err
	}
	if daily < perTx {
		return errf("ERR_BAD_POLICY", "daily limit must be at least the per-tx limit")
	}
	if perTx != u.Session.PerTx || daily != u.Session.Daily {
		u.Session.PerTx, u.Session.Daily = perTx, daily
		e.newTx()
		e.log("ok", u.Addr+" (wallet)", "Vault.RegisterSession", fmt.Sprintf("%s session limits $%s/tx · $%s/day", u.Name, FormatUSDC(perTx), FormatUSDC(daily)))
	}
	u.Policy = Policy{Auto: req.Auto, Threshold: req.Threshold, Weights: req.Weights}
	e.log("ok", u.Addr, "PUT /api/v1/agent/policy (off-chain)",
		fmt.Sprintf("auto_redeem %t · threshold %d P · ATOM %.2f / USDC.inj %.2f / INJ %.2f", req.Auto, req.Threshold, req.Weights[ATOM], req.Weights[USDCINJ], req.Weights[INJ]))
	return nil
}
