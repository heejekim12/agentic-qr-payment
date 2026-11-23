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
