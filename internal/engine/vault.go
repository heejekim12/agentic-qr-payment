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

// quoteTopupLocked adds the ATOM amount for Cosmos Hub top-ups; caller holds e.mu.
func (e *Engine) quoteTopupLocked(chain string, amount float64) (TopupQuote, error) {
	q, err := quoteTopup(chain, amount)
	if err == nil && q.Denom == "ATOM" {
		q.AtomUSD = e.atomUSD()
		q.AtomOut = int64(float64(q.Out) / q.AtomUSD)
	}
	return q, err
}

// QuoteTopup mirrors SkipClient.route(): USDC -> USDC.inj, or ATOM over IBC; amounts are in USD.
func (e *Engine) QuoteTopup(req TopupRequest) (TopupQuote, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	u, err := e.user(req.User)
	if err != nil {
		return TopupQuote{}, err
	}
	q, err := e.quoteTopupLocked(req.Chain, req.Amount)
	if err != nil {
		return q, err
	}
	return q, walletCovers(u, q)
}

// Topup mirrors SkipClient.executeRoute() with Vault.DepositUsdc as the post-route action.
func (e *Engine) Topup(req TopupRequest) (TopupQuote, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	u, err := e.user(req.User)
	if err != nil {
		return TopupQuote{}, err
	}
	q, err := e.quoteTopupLocked(req.Chain, req.Amount)
	if err != nil {
		return q, err
	}
	if err := walletCovers(u, q); err != nil {
		return q, err
	}
	e.newTx()
	u.Holdings[q.Chain] -= q.Amount
	e.stats.TopUps++
	e.stats.TopUpVolume += q.Out
	if q.Denom == "ATOM" {
		u.ATOM += q.AtomOut
		e.addHistory(u, &HistoryEntry{Type: "TOPUP", Status: "DONE", Credit: true, Desc: "ATOM on Cosmos Hub via Skip:Go", Delta: "+" + FormatAtom(q.AtomOut) + " ATOM"})
		e.log("skip", u.Name+" wallet", "SkipClient.executeRoute()", fmt.Sprintf("signed on Cosmos Hub (cosmoshub-4); IBC ATOM to injective-1, post-route action Vault.DepositAtom for %s", u.Addr))
		e.log("ok", "Skip:Go relayer", "Vault.DepositAtom", fmt.Sprintf("%s Vault +%s ATOM", u.Name, FormatAtom(q.AtomOut)))
		return q, nil
	}
	u.USDC += q.Out
	if q.Chain == "injective-1" {
		e.addHistory(u, &HistoryEntry{Type: "TOPUP", Status: "DONE", Credit: true, Desc: "From Injective wallet", Delta: "+" + FormatUSDC(q.Out) + " " + PayCurrency})
		e.log("ok", u.Addr+" (wallet)", "Vault.DepositUsdc", fmt.Sprintf("%s Vault +%s USDC (direct deposit)", u.Name, FormatUSDC(q.Out)))
		return q, nil
	}
	e.addHistory(u, &HistoryEntry{Type: "TOPUP", Status: "DONE", Credit: true, Desc: q.Denom + " on " + q.Name + " via Skip:Go", Delta: "+" + FormatUSDC(q.Out) + " " + PayCurrency})
	e.log("skip", u.Name+" wallet", "SkipClient.executeRoute()", fmt.Sprintf("signed on %s (%s); post-route action Vault.DepositUsdc for %s", q.Name, q.Chain, u.Addr))
	e.log("ok", "Skip:Go relayer", "Vault.DepositUsdc", fmt.Sprintf("%s Vault +%s USDC.inj (from %s)", u.Name, FormatUSDC(q.Out), q.Denom))
	return q, nil
}

// walletCovers checks the user's (sample) wallet balance on the source chain.
func walletCovers(u *User, q TopupQuote) error {
	if have := u.Holdings[q.Chain]; q.Amount > have {
		return errf("ERR_INSUFFICIENT_WALLET", "your %s wallet has %s %s", q.Name, FormatUSDC(have), q.Denom)
	}
	return nil
}

type WithdrawRequest struct {
	User   int     `json:"user"`
	Asset  string  `json:"asset"`       // USDC_INJ (default) or ATOM
	Amount float64 `json:"amount_usdc"` // in units of the asset
}

// Withdraw is owner-signed only; session keys cannot affect it.
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
	if req.Asset == PayATOM {
		if amt > u.ATOM {
			return errf("ERR_INSUFFICIENT_VAULT", "Vault balance is %s ATOM", FormatAtom(u.ATOM))
		}
		e.newTx()
		u.ATOM -= amt
		e.addHistory(u, &HistoryEntry{Type: "WITHDRAW", Status: "DONE", Desc: "Vault -> " + u.Addr, Delta: "-" + FormatAtom(amt) + " ATOM"})
		e.log("ok", u.Addr+" (wallet)", "Vault.Withdraw", fmt.Sprintf("%s · %s ATOM -> owner. Session keys cannot call this.", u.Name, FormatAtom(amt)))
		return nil
	}
	if req.Asset != "" && req.Asset != PayUSDC {
		return errf("ERR_BAD_ASSET", "asset must be USDC_INJ or ATOM")
	}
	if amt > u.USDC {
		return errf("ERR_INSUFFICIENT_VAULT", "Vault balance is %s USDC.inj", FormatUSDC(u.USDC))
	}
	e.newTx()
	u.USDC -= amt
	e.addHistory(u, &HistoryEntry{Type: "WITHDRAW", Status: "DONE", Desc: "Vault -> " + u.Addr, Delta: "-" + FormatUSDC(amt) + " " + PayCurrency})
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
	User  int     `json:"user"`
	PerTx float64 `json:"per_tx_usdc"`
	Daily float64 `json:"daily_usdc"`
}

func (e *Engine) SetPolicy(req PolicyRequest) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	u, err := e.user(req.User)
	if err != nil {
		return err
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
	return nil
}
