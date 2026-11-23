package engine

import "fmt"

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

type BoostRequest struct {
	ATOM    int64 `json:"ATOM"`
	USDCINJ int64 `json:"USDC_INJ"`
	INJ     int64 `json:"INJ"`
}

// SetBoost takes percents and rejects any setting where ATOM does not beat USDC.inj after route cost.
func (e *Engine) SetBoost(req BoostRequest) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, v := range []int64{req.ATOM, req.USDCINJ, req.INJ} {
		if v < 0 || v > 50 {
			return errf("ERR_BAD_BOOST", "boost must be between 0 and 50%%")
		}
	}
	prev := e.boostBps
	e.boostBps = map[Asset]int64{ATOM: req.ATOM * 100, USDCINJ: req.USDCINJ * 100, INJ: req.INJ * 100}
	qa, qu := e.quote(1000, ATOM), e.quote(1000, USDCINJ)
	if qa.Net <= qu.Net {
		e.boostBps = prev
		return errf("ERR_ATOM_NOT_FAVORED", "1,000 P gives ATOM $%s vs USDC.inj $%s; ATOM must be higher after route cost", FormatUSDC(qa.Net), FormatUSDC(qu.Net))
	}
	e.newTx()
	e.log("sec", "pool admin (multisig)", "RewardPool.SetBoost", fmt.Sprintf("ATOM +%d%% · USDC.inj +%d%% · INJ +%d%% · 1,000 P: ATOM $%s vs USDC.inj $%s",
		req.ATOM, req.USDCINJ, req.INJ, FormatUSDC(qa.Net), FormatUSDC(qu.Net)))
	return nil
}

type PoolCapRequest struct {
	Cap float64 `json:"cap_usdc"`
}

func (e *Engine) SetPoolCap(req PoolCapRequest) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, err := toMicro(req.Cap)
	if err != nil {
		return err
	}
	e.pool.DailyCap = c
	e.newTx()
	e.log("sec", "pool admin (multisig)", "RewardPool.SetCap", "per-user daily redeem cap $"+FormatUSDC(c))
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
