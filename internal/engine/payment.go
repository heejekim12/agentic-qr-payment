package engine

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type QRRequest struct {
	MerchantID string  `json:"merchant_id"`
	Amount     float64 `json:"amount_usdc"`
}

// qrMessage is the canonical byte string the merchant signs; every payload field is covered.
func qrMessage(q *QR) []byte {
	return fmt.Appendf(nil, "%s|%s|%s|%s|%s|%d", q.Version, q.MerchantID, q.OrderID, q.AmountUSDC, q.Currency, q.ExpiresAt)
}

// CreateQR builds an MPM v2.0 payload signed with the merchant's ed25519 key.
func (e *Engine) CreateQR(req QRRequest) (*QR, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	m := e.merchant(req.MerchantID)
	if m == nil {
		return nil, errf("ERR_UNKNOWN_MERCHANT", "%s is not in the registry", req.MerchantID)
	}
	amt, err := toMicro(req.Amount)
	if err != nil {
		return nil, err
	}
	e.seq++
	now := e.now()
	q := &QR{
		Version: "2.0", MerchantID: m.ID, Currency: PayCurrency,
		OrderID:    fmt.Sprintf("ORD_%s_%04d", now.Format("20060102"), e.seq),
		AmountUSDC: FormatUSDC(amt), ExpiresAt: now.Add(QRTTL).Unix(),
	}
	q.MerchantSig = "0x" + hex.EncodeToString(ed25519.Sign(e.merchantKeys[m.ID], qrMessage(q)))
	return q, nil
}

// verifySig checks merchant_sig against the public key registered for merchant_id.
func (e *Engine) verifySig(q *QR) bool {
	m := e.merchant(q.MerchantID)
	if m == nil {
		return false
	}
	pub, err := hex.DecodeString(m.PubKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(q.MerchantSig, "0x"))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pub, qrMessage(q), sig)
}

func parseQRAmount(s string) (int64, *Error) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, errf("ERR_BAD_QR", "amount_usdc %q is not a number", s)
	}
	m, err := toMicro(v)
	if err != nil {
		return 0, errf("ERR_BAD_QR", "amount_usdc %q is out of range", s)
	}
	return m, nil
}

type VerifyRequest struct {
	QR *QR `json:"qr"`
}

// VerifyQR runs every authentication check and reports each one, without short-circuiting.
func (e *Engine) VerifyQR(req VerifyRequest) (*QRVerification, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	q := req.QR
	if q == nil {
		return nil, errf("ERR_BAD_QR", "qr is required")
	}
	m := e.merchant(q.MerchantID)
	name := q.MerchantID
	if m != nil {
		name = m.Name
	}
	sigOK := e.verifySig(q)
	sigDetail := "Signature does not match the store's registered key. The QR was altered or not issued by this store."
	if sigOK {
		sigDetail = fmt.Sprintf("Signed by %s · ed25519 key %s…", name, m.PubKey[:12])
	}
	active := m != nil && m.Status == StatusActive
	activeDetail := "Store is not in the registry"
	if m != nil {
		activeDetail = name + " is " + strings.ToLower(m.Status) + " in the merchant registry"
	}
	left := q.ExpiresAt - e.now().Unix()
	expDetail := fmt.Sprintf("Valid for %d more seconds", left)
	if left <= 0 {
		expDetail = "Expired at " + time.Unix(q.ExpiresAt, 0).Format("15:04:05")
	}
	paid := e.paid[q.OrderID]
	orderDetail := q.OrderID + " has not been paid"
	if paid {
		orderDetail = q.OrderID + " is already paid"
	}
	v := &QRVerification{Checks: []QRCheck{
		{Name: "Store signature", OK: sigOK, Detail: sigDetail},
		{Name: "Approved store", OK: active, Detail: activeDetail},
		{Name: "Pays in " + PayCurrency, OK: q.Currency == PayCurrency, Detail: "Currency in QR: " + q.Currency},
		{Name: "Not expired", OK: left > 0, Detail: expDetail},
		{Name: "Order not paid yet", OK: !paid, Detail: orderDetail},
	}}
	v.Valid = sigOK && active && q.Currency == PayCurrency && left > 0 && !paid
	return v, nil
}

type PayRequest struct {
	User      int  `json:"user"`
	QR        *QR  `json:"qr"`
	UsePoints bool `json:"use_points"`
}

type payCalc struct {
	q                              *QR
	m                              *Merchant
	amount, pUsed, aPt, aUsdc, fee int64
	earnBps, pEarned, promoBps     int64
	poolFee                        int64
	bonus                          bool
}

// calcPay runs every ExecuteQrPayment check and computes the split without changing state.
func (e *Engine) calcPay(u *User, req PayRequest) (*payCalc, *Error) {
	q := req.QR
	if q == nil {
		return nil, errf("ERR_UNKNOWN_QR", "no QR payload was scanned")
	}
	if !e.verifySig(q) {
		return nil, errf("ERR_BAD_SIGNATURE", "merchant_sig does not match the key registered for %s", q.MerchantID)
	}
	if q.Currency != PayCurrency {
		return nil, errf("ERR_BAD_CURRENCY", "currency must be %s, got %q", PayCurrency, q.Currency)
	}
	amount, x := parseQRAmount(q.AmountUSDC)
	if x != nil {
		return nil, x
	}
	m := e.merchant(q.MerchantID)
	if m == nil || m.Status != StatusActive {
		status := "UNKNOWN"
		if m != nil {
			status = m.Status
		}
		return nil, errf("ERR_MERCHANT_NOT_WHITELISTED", "%s is %s", q.MerchantID, status)
	}
	if e.now().Unix() >= q.ExpiresAt {
		return nil, errf("ERR_QR_EXPIRED", "expired at %s", time.Unix(q.ExpiresAt, 0).Format("15:04:05"))
	}
	if e.paid[q.OrderID] {
		return nil, errf("ERR_ORDER_ALREADY_PAID", "%s already settled", q.OrderID)
	}
	c := &payCalc{q: q, m: m, amount: amount}
	if req.UsePoints && !e.pool.Paused {
		c.pUsed = min(u.Points, c.amount/PointValue)
	}
	c.aPt = c.pUsed * PointValue
	c.aUsdc = c.amount - c.aPt
	s := u.Session
	if s.Active && c.aUsdc > s.PerTx {
		return nil, errf("ERR_SESSION_TX_LIMIT", "$%s > session limit $%s/tx", FormatUSDC(c.aUsdc), FormatUSDC(s.PerTx))
	}
	if s.Active && s.SpentToday+c.aUsdc > s.Daily {
		return nil, errf("ERR_SESSION_DAILY_LIMIT", "$%s > $%s/day", FormatUSDC(s.SpentToday+c.aUsdc), FormatUSDC(s.Daily))
	}
	if u.USDC < c.aUsdc {
		return nil, errf("ERR_INSUFFICIENT_VAULT", "need %s USDC.inj, Vault has %s", FormatUSDC(c.aUsdc), FormatUSDC(u.USDC))
	}
	if c.aPt > e.pool.Balance {
		return nil, errf("ERR_POOL_INSUFFICIENT", "Reward Pool cannot cover the POINT discount")
	}
	c.fee = c.aUsdc * m.FeeBps / 10_000
	c.poolFee = c.aUsdc * m.PoolBps / 10_000
	if m.Promo != nil && promoActive(m.Promo, e.now()) {
		// Happy-hour points are paid by the merchant as an extra fee into the Reward Pool.
		c.promoBps = m.Promo.ExtraBps
		c.fee += c.amount * c.promoBps / 10_000
		c.poolFee += c.amount * c.promoBps / 10_000
	}
	c.earnBps = m.PointEarnBps + u.NextEarnBonusBps + c.promoBps
	// Base earn applies to the USDC-paid part only; happy-hour points apply to the whole order.
	c.pEarned = (c.aUsdc*(c.earnBps-c.promoBps) + c.amount*c.promoBps) / 10_000 / PointValue
	c.bonus = u.NextEarnBonusBps > 0
	return c, nil
}

func receipt(c *payCalc, sessionActive bool) Receipt {
	signedBy := "wallet popup"
	if sessionActive {
		signedBy = "session key, no popup"
	}
	return Receipt{
		OrderID: c.q.OrderID, Merchant: c.m.Name, Amount: c.amount, PointsUsed: c.pUsed, PointDiscount: c.aPt,
		Paid: c.aUsdc, PointsEarned: c.pEarned, EarnBps: c.earnBps, PromoBps: c.promoBps, BonusApplied: c.bonus, SignedBy: signedBy,
	}
}

// PreviewPay shows what Pay would do; it changes nothing and logs nothing.
func (e *Engine) PreviewPay(req PayRequest) (*Receipt, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	u, err := e.user(req.User)
	if err != nil {
		return nil, err
	}
	c, x := e.calcPay(u, req)
	if x != nil {
		return nil, x
	}
	r := receipt(c, u.Session.Active)
	return &r, nil
}

// Pay mirrors Vault.ExecuteQrPayment: every check passes or nothing changes.
func (e *Engine) Pay(req PayRequest) (*PayResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	u, err := e.user(req.User)
	if err != nil {
		return nil, err
	}
	s := &u.Session
	signer := u.Addr + " (wallet popup)"
	if s.Active {
		signer = "session key (feegrant)"
	}
	e.newTx() // a reverted tx still has a hash
	c, x := e.calcPay(u, req)
	if x != nil {
		e.stats.Rejected[x.Code]++
		e.log("bad", signer, "Vault.ExecuteQrPayment", fmt.Sprintf("%s · %s · %s · tx reverted, nothing moved", u.Name, x.Code, x.Message))
		return nil, x
	}

	u.USDC -= c.aUsdc
	u.Points += c.pEarned - c.pUsed
	u.NextEarnBonusBps = 0
	if s.Active {
		s.SpentToday += c.aUsdc
	}
	e.pool.Balance += c.poolFee - c.aPt
	e.pool.FeesIn += c.poolFee
	e.stats.OperatorFees += c.fee - c.poolFee
	e.pool.DiscountOut += c.aPt
	e.pool.GasINJ = max(0, e.pool.GasINJ-gasPerTxINJ)
	c.m.Received += c.aUsdc - c.fee + c.aPt
	c.m.Orders++
	c.m.Sales += c.amount
	c.m.Fees += c.fee
	c.m.PointComp += c.aPt
	e.stats.Payments++
	e.stats.GMV += c.amount
	e.stats.PaidUSDC += c.aUsdc
	e.stats.PointDiscount += c.aPt
	e.stats.PointsMinted += c.pEarned
	e.stats.PointsBurned += c.pUsed
	e.paid[c.q.OrderID] = true

	e.addHistory(u, &HistoryEntry{
		Type: "PAY", Status: "PAID", Desc: c.m.Name,
		Note:  fmt.Sprintf("%d P used · +%d P earned", c.pUsed, c.pEarned),
		Delta: "-" + FormatUSDC(c.aUsdc) + " " + PayCurrency, OrderID: c.q.OrderID, MerchantID: c.m.ID,
		Amount: c.amount, PointsUsed: c.pUsed, PaidUSDC: c.aUsdc, Fee: c.fee, PoolFee: c.poolFee, PointsEarned: c.pEarned,
	})
	e.log("ok", signer, "Vault.ExecuteQrPayment", fmt.Sprintf(
		"%s · %s · registry %s ACTIVE · sig/expiry/order ok · session limits ok\nburn %d POINT (pool pays $%s) · Vault -%s · merchant +%s · pool fee +%s · mint %d POINT",
		u.Name, c.q.OrderID, c.m.ID, c.pUsed, FormatUSDC(c.aPt), FormatUSDC(c.aUsdc), FormatUSDC(c.aUsdc-c.fee+c.aPt), FormatUSDC(c.fee), c.pEarned))

	return &PayResult{Receipt: receipt(c, s.Active)}, nil
}

type RefundRequest struct {
	User      int `json:"user"`
	HistoryID int `json:"history_id"`
}

// Refund mirrors a merchant-signed Vault.RefundPayment within RefundWindow.
func (e *Engine) Refund(req RefundRequest) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	u, err := e.user(req.User)
	if err != nil {
		return err
	}
	var h *HistoryEntry
	for _, x := range u.History {
		if x.ID == req.HistoryID {
			h = x
			break
		}
	}
	if h == nil || h.Type != "PAY" || h.Status != "PAID" {
		return errf("ERR_NOT_REFUNDABLE", "entry %d is not a paid payment", req.HistoryID)
	}
	if e.now().Sub(h.Time) >= RefundWindow {
		return errf("ERR_REFUND_WINDOW_CLOSED", "refund window of %s has passed", RefundWindow)
	}
	m := e.merchant(h.MerchantID)
	e.newTx()

	// Burn earned POINT; if some were already redeemed, deduct their value from the refund.
	claw := min(u.Points, h.PointsEarned)
	short := h.PointsEarned - claw
	refund := max(0, h.PaidUSDC-short*PointValue)

	u.Points += h.PointsUsed - claw
	u.USDC += refund
	u.Session.SpentToday = max(0, u.Session.SpentToday-h.PaidUSDC)
	e.pool.Balance += h.PointsUsed*PointValue - h.PoolFee + short*PointValue
	e.pool.FeesIn -= h.PoolFee
	e.stats.OperatorFees -= h.Fee - h.PoolFee
	e.pool.DiscountOut -= h.PointsUsed * PointValue
	m.Received -= h.Amount - h.Fee
	m.Refunds++
	e.stats.Refunds++
	e.stats.Refunded += refund
	e.stats.PointsMinted += h.PointsUsed
	e.stats.PointsBurned += claw
	h.Status = "CANCELLED"

	note := fmt.Sprintf("%d P returned · %d P earned removed", h.PointsUsed, claw)
	if short > 0 {
		note += fmt.Sprintf(" · %d P already redeemed, -$%s from refund", short, FormatUSDC(short*PointValue))
	}
	e.addHistory(u, &HistoryEntry{Type: "REFUND", Status: "DONE", Credit: true, Desc: "Refund · " + m.Name, Note: note, Delta: "+" + FormatUSDC(refund) + " " + PayCurrency})
	e.log("ok", m.ID+" (merchant key)", "Vault.RefundPayment", fmt.Sprintf(
		"%s · %s · merchant returns $%s · pool returns fee $%s · Vault +%s · POINT +%d -%d",
		u.Name, h.OrderID, FormatUSDC(h.Amount-h.Fee), FormatUSDC(h.Fee), FormatUSDC(refund), h.PointsUsed, claw))
	return nil
}
