package engine

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"math"
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
	User      int    `json:"user"`
	QR        *QR    `json:"qr"`
	UsePoints bool   `json:"use_points"`
	PayWith   string `json:"pay_with"`       // USDC_INJ (default) or ATOM
	MaxAtom   int64  `json:"max_atom_micro"` // from the preview; required when paying with ATOM
}

type payCalc struct {
	q                          *QR
	m                          *Merchant
	amount, pUsed, aPt, aUsdc  int64
	earnBps, pEarned, promoBps int64
	atomBonusBps               int64
	payWith                    string
	atomIn, atomMax            int64
	atomUSD                    float64
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
	if req.UsePoints {
		c.pUsed = min(u.Points[m.ID], c.amount/PointValue)
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
	switch req.PayWith {
	case "", PayUSDC:
		c.payWith = PayUSDC
		if u.USDC < c.aUsdc {
			return nil, errf("ERR_INSUFFICIENT_VAULT", "need %s USDC.inj, Vault has %s", FormatUSDC(c.aUsdc), FormatUSDC(u.USDC))
		}
	case PayATOM:
		// Exact-out swap: Skip:Go sells enough ATOM to deliver aUsdc USDC.inj to the merchant.
		c.payWith, c.atomUSD = PayATOM, e.atomUSD()
		c.atomIn = int64(math.Ceil(float64(c.aUsdc) / c.atomUSD * 10_000 / float64(10_000-atomSwapFeeBps)))
		c.atomMax = c.atomIn * (10_000 + maxSlippageBps) / 10_000
		if req.MaxAtom > 0 && c.atomIn > req.MaxAtom {
			return nil, errf("ERR_SLIPPAGE", "ATOM needed %s > limit %s; review the new price", FormatAtom(c.atomIn), FormatAtom(req.MaxAtom))
		}
		if u.ATOM < c.atomIn {
			return nil, errf("ERR_INSUFFICIENT_VAULT", "need %s ATOM, Vault has %s", FormatAtom(c.atomIn), FormatAtom(u.ATOM))
		}
	default:
		return nil, errf("ERR_BAD_ASSET", "pay_with must be USDC_INJ or ATOM")
	}
	if m.Promo != nil && promoActive(m.Promo, e.now()) {
		// Happy-hour points are a merchant-approved promotion; no extra fee is charged.
		c.promoBps = m.Promo.ExtraBps
	}
	if c.payWith == PayATOM {
		c.atomBonusBps = m.AtomBonusBps
	}
	c.earnBps = m.PointEarnBps + c.atomBonusBps + c.promoBps
	// Base earn applies to the USDC-paid part only; happy-hour points apply to the whole order.
	c.pEarned = (c.aUsdc*(c.earnBps-c.promoBps) + c.amount*c.promoBps) / 10_000 / PointValue
	return c, nil
}

func receipt(c *payCalc, sessionActive bool) Receipt {
	signedBy := "wallet popup"
	if sessionActive {
		signedBy = "session key, no popup"
	}
	return Receipt{
		OrderID: c.q.OrderID, Merchant: c.m.Name, Amount: c.amount, PointsUsed: c.pUsed, PointDiscount: c.aPt,
		Paid: c.aUsdc, PointsEarned: c.pEarned, EarnBps: c.earnBps, PromoBps: c.promoBps, AtomBonusBps: c.atomBonusBps, SignedBy: signedBy,
		PayWith: c.payWith, AtomIn: c.atomIn, AtomMax: c.atomMax, AtomUSD: c.atomUSD,
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
	if x == nil && req.PayWith == PayATOM && req.MaxAtom <= 0 {
		x = errf("ERR_SLIPPAGE", "max_atom_micro from the preview is required when paying with ATOM")
	}
	if x != nil {
		e.stats.Rejected[x.Code]++
		e.log("bad", signer, "Vault.ExecuteQrPayment", fmt.Sprintf("%s · %s · %s · tx reverted, nothing moved", u.Name, x.Code, x.Message))
		return nil, x
	}

	delta := "-" + FormatUSDC(c.aUsdc) + " " + PayCurrency
	if c.payWith == PayATOM {
		u.ATOM -= c.atomIn
		delta = "-" + FormatAtom(c.atomIn) + " ATOM"
		e.log("skip", signer, "SkipClient.executeRoute()", fmt.Sprintf("%s · swap %s ATOM -> %s USDC.inj on injective-1 at $%.2f (limit %s ATOM), output to %s",
			u.Name, FormatAtom(c.atomIn), FormatUSDC(c.aUsdc), c.atomUSD, FormatAtom(req.MaxAtom), c.m.SettlementAddr))
	} else {
		u.USDC -= c.aUsdc
	}
	u.Points[c.m.ID] += c.pEarned - c.pUsed
	if s.Active {
		s.SpentToday += c.aUsdc
	}
	e.gasINJ = max(0, e.gasINJ-gasPerTxINJ)
	c.m.Received += c.aUsdc
	c.m.Orders++
	c.m.Sales += c.amount
	c.m.Discounts += c.aPt
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
		Delta: delta, OrderID: c.q.OrderID, MerchantID: c.m.ID,
		Amount: c.amount, PointsUsed: c.pUsed, PaidUSDC: c.aUsdc, PointsEarned: c.pEarned,
	})
	e.log("ok", signer, "Vault.ExecuteQrPayment", fmt.Sprintf(
		"%s · %s · registry %s ACTIVE · sig/expiry/order ok · session limits ok\nburn %d %s POINT (merchant discount $%s) · Vault -%s · merchant +%s · mint %d %s POINT",
		u.Name, c.q.OrderID, c.m.ID, c.pUsed, c.m.ID, FormatUSDC(c.aPt), FormatUSDC(c.aUsdc), FormatUSDC(c.aUsdc), c.pEarned, c.m.ID))

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

	// Burn earned POINT; if some were already spent, deduct their value from the refund.
	// Refunds are in USDC.inj, also for payments made with ATOM.
	claw := min(u.Points[m.ID], h.PointsEarned)
	short := h.PointsEarned - claw
	refund := max(0, h.PaidUSDC-short*PointValue)

	u.Points[m.ID] += h.PointsUsed - claw
	u.USDC += refund
	u.Session.SpentToday = max(0, u.Session.SpentToday-h.PaidUSDC)
	m.Received -= refund
	m.Discounts -= h.PointsUsed * PointValue
	m.Refunds++
	e.stats.Refunds++
	e.stats.Refunded += refund
	e.stats.PointsMinted += h.PointsUsed
	e.stats.PointsBurned += claw
	h.Status = "CANCELLED"

	note := fmt.Sprintf("%d P returned · %d P earned removed", h.PointsUsed, claw)
	if short > 0 {
		note += fmt.Sprintf(" · %d P already spent, -$%s from refund", short, FormatUSDC(short*PointValue))
	}
	e.addHistory(u, &HistoryEntry{Type: "REFUND", Status: "DONE", Credit: true, Desc: "Refund · " + m.Name, Note: note, Delta: "+" + FormatUSDC(refund) + " " + PayCurrency})
	e.log("ok", m.ID+" (merchant key)", "Vault.RefundPayment", fmt.Sprintf(
		"%s · %s · merchant returns $%s · Vault +%s · %s POINT +%d -%d",
		u.Name, h.OrderID, FormatUSDC(refund), FormatUSDC(refund), m.ID, h.PointsUsed, claw))
	return nil
}
