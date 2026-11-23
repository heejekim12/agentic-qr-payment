package engine

type MerchantStatusRequest struct {
	MerchantID string `json:"merchant_id"`
}

// ToggleMerchant mirrors Registry.SetStatus signed by the registry admin multisig.
func (e *Engine) ToggleMerchant(req MerchantStatusRequest) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	m := e.merchant(req.MerchantID)
	if m == nil {
		return errf("ERR_UNKNOWN_MERCHANT", "%s is not in the registry", req.MerchantID)
	}
	e.newTx()
	if m.Status == StatusActive {
		m.Status = StatusSuspended
	} else {
		m.Status = StatusActive
	}
	e.log("sec", "registry admin (multisig)", "Registry.SetStatus", m.ID+" -> "+m.Status)
	return nil
}

const poolFundStep = 200 * Micro

func (e *Engine) FundPool() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pool.Balance += poolFundStep
	e.newTx()
	e.log("ok", "marketing wallet", "RewardPool.Fund", "+$"+FormatUSDC(poolFundStep)+" USDC.inj ecosystem budget")
	return nil
}

// TogglePause affects only the Reward Pool; user Vaults are never frozen.
func (e *Engine) TogglePause() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pool.Paused = !e.pool.Paused
	e.newTx()
	if e.pool.Paused {
		e.log("sec", "guardian key", "RewardPool.Pause", "POINT discount and redemptions halted. User Vaults unaffected.")
	} else {
		e.log("sec", "guardian key", "RewardPool.Unpause", "pool resumed")
	}
	return nil
}
