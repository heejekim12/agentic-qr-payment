package engine

import (
	"fmt"
	"time"
)

const (
	Micro        int64 = 1_000_000 // 1 USDC.inj
	PointValue   int64 = 10_000    // 1 POINT = 0.01 USDC.inj in micro
	QRTTL              = 120 * time.Second
	RefundWindow       = 10 * time.Minute
	gasPerTxINJ        = 0.0004
	maxEvents          = 300
	maxAmount    int64 = 100_000 * Micro
)

// PayCurrency is the currency of the QR price and of merchant settlement.
const PayCurrency = "USDC.inj"

// Assets a user can pay with. ATOM is swapped to USDC.inj by Skip:Go inside the payment.
const (
	PayUSDC = "USDC_INJ"
	PayATOM = "ATOM"
)

const (
	atomSwapFeeBps = 30  // sample Skip:Go ATOM -> USDC.inj swap cost on injective-1
	maxSlippageBps = 100 // the payment reverts if ATOM needed rises more than 1% over the preview
)

type chainRoute struct {
	Name   string
	Denom  string
	FeeBps int64
}

// injective-1 already holds USDC.inj, so a deposit from it needs no route.
// cosmoshub-4 ATOM is moved over IBC by Skip:Go and kept as ATOM in the Vault.
var topupChains = map[string]chainRoute{
	"injective-1": {"Injective", "USDC.inj", 0},
	"cosmoshub-4": {"Cosmos Hub", "ATOM", 10},
	"8453":        {"Base", "USDC", 10},
	"1":           {"Ethereum", "USDC", 40},
	"solana":      {"Solana", "USDC", 20},
}

const (
	StatusActive    = "ACTIVE"
	StatusSuspended = "SUSPENDED"
)

type Merchant struct {
	ID              string `json:"merchant_id"`
	Name            string `json:"name"`
	Category        string `json:"category"`
	Status          string `json:"status"`
	SettlementAddr  string `json:"settlement_addr"`
	SubscriptionKRW int64  `json:"subscription_krw"` // monthly software fee billed in KRW off-chain; payments carry no fee
	Tier            string `json:"tier"`
	PointEarnBps    int64  `json:"point_earn_bps"`
	AtomBonusBps    int64  `json:"atom_bonus_bps"` // extra store POINT when paying with ATOM, funded by the merchant
	Received        int64  `json:"received_micro"`
	PubKey          string `json:"pubkey"` // ed25519 public key that verifies this merchant's QR
	Area            string `json:"area"`
	Promo           *Promo `json:"promo,omitempty"` // merchant-approved happy hour, paid by the merchant
	Orders          int    `json:"orders"`
	Sales           int64  `json:"sales_micro"`
	Discounts       int64  `json:"discount_micro"` // POINT discounts this merchant gave on its own points
	Refunds         int    `json:"refunds"`
}

// Promo is a daily happy-hour window in KST minutes since midnight.
type Promo struct {
	StartMin int   `json:"start_min"`
	EndMin   int   `json:"end_min"`
	ExtraBps int64 `json:"extra_bps"`
}

// Profile is sample spending history used by the agent's advice.
type Profile struct {
	AvgOrder int64 `json:"avg_order_micro"`
	PerWeek  int64 `json:"orders_per_week"`
}

// Stats are platform-wide totals for the overview screen; payment figures are gross of refunds.
type Stats struct {
	Payments      int            `json:"payments"`
	GMV           int64          `json:"gmv_micro"`
	PaidUSDC      int64          `json:"paid_usdc_micro"`
	PointDiscount int64          `json:"point_discount_micro"`
	Refunds       int            `json:"refunds"`
	Refunded      int64          `json:"refunded_micro"`
	TopUps        int            `json:"topups"`
	TopUpVolume   int64          `json:"topup_micro"`
	PointsMinted  int64          `json:"points_minted"`
	PointsBurned  int64          `json:"points_burned"`
	Rejected      map[string]int `json:"rejected"`
}

type Session struct {
	Active     bool  `json:"active"`
	PerTx      int64 `json:"per_tx_micro"`
	Daily      int64 `json:"daily_micro"`
	SpentToday int64 `json:"spent_today_micro"`
}

type HistoryEntry struct {
	ID         int       `json:"id"`
	Type       string    `json:"type"`
	Status     string    `json:"status"`
	Time       time.Time `json:"time"`
	Desc       string    `json:"desc"`
	Note       string    `json:"note,omitempty"`
	Delta      string    `json:"delta"`
	Credit     bool      `json:"credit"`
	Refundable bool      `json:"refundable"`
	TxHash     string    `json:"tx_hash,omitempty"`
	// payment fields used by refund
	OrderID      string `json:"order_id,omitempty"`
	MerchantID   string `json:"merchant_id,omitempty"`
	Amount       int64  `json:"order_micro,omitempty"`
	PointsUsed   int64  `json:"-"`
	PaidUSDC     int64  `json:"-"`
	PointsEarned int64  `json:"-"`
}

type User struct {
	Name     string           `json:"name"`
	Addr     string           `json:"addr"`
	USDC     int64            `json:"usdc_micro"`
	ATOM     int64            `json:"atom_micro"` // uatom held in the Vault
	Points   map[string]int64 `json:"points"`     // per merchant ID; usable only at that merchant
	Session  Session          `json:"session"`
	History  []*HistoryEntry  `json:"history"`
	Wallet   string           `json:"wallet,omitempty"`
	Area     string           `json:"area"`
	Profile  Profile          `json:"profile"`
	Holdings map[string]int64 `json:"holdings_micro"` // sample USDC held per source chain
	day      string
	demoAddr string
}

type Event struct {
	Time   time.Time `json:"time"`
	Kind   string    `json:"kind"`
	Signer string    `json:"signer"`
	Msg    string    `json:"msg"`
	Body   string    `json:"body"`
	TxHash string    `json:"tx_hash,omitempty"`
}

type QR struct {
	Version     string `json:"qr_version"`
	MerchantID  string `json:"merchant_id"`
	OrderID     string `json:"order_id"`
	AmountUSDC  string `json:"amount_usdc"`
	Currency    string `json:"currency"`
	ExpiresAt   int64  `json:"expires_at"`
	MerchantSig string `json:"merchant_sig"`
}

// QRCheck is one step of QR authentication shown to the payer.
type QRCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type QRVerification struct {
	Valid  bool      `json:"valid"`
	Checks []QRCheck `json:"checks"`
}

type TopupQuote struct {
	Chain  string `json:"chain"`
	Name   string `json:"name"`
	Denom  string `json:"denom"`
	Amount int64  `json:"amount_micro"`
	Fee    int64  `json:"fee_micro"`
	Out    int64  `json:"out_micro"`
	// ATOM top-ups arrive as ATOM; AtomOut is Out at AtomUSD.
	AtomOut int64   `json:"atom_out_micro,omitempty"`
	AtomUSD float64 `json:"atom_usd,omitempty"`
}

type Receipt struct {
	OrderID       string `json:"order_id"`
	Merchant      string `json:"merchant"`
	Amount        int64  `json:"amount_micro"`
	PointsUsed    int64  `json:"points_used"`
	PointDiscount int64  `json:"point_discount_micro"`
	Paid          int64  `json:"paid_micro"`
	PointsEarned  int64  `json:"points_earned"`
	EarnBps       int64  `json:"earn_bps"`
	PromoBps      int64  `json:"promo_bps"`
	AtomBonusBps  int64  `json:"atom_bonus_bps"`
	SignedBy      string `json:"signed_by"`
	// Set when paying with ATOM.
	PayWith string  `json:"pay_with"`
	AtomIn  int64   `json:"atom_in_micro,omitempty"`
	AtomMax int64   `json:"atom_max_micro,omitempty"` // preview only: ATOM in plus slippage limit
	AtomUSD float64 `json:"atom_usd,omitempty"`
}

type PayResult struct {
	Receipt Receipt `json:"receipt"`
}

// Error is a contract-style rejection with a stable code.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func errf(code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...)}
}

// FormatUSDC renders micro units as a 2-decimal string (truncated).
func FormatUSDC(m int64) string {
	sign := ""
	if m < 0 {
		sign, m = "-", -m
	}
	return fmt.Sprintf("%s%d.%02d", sign, m/Micro, (m%Micro)/10_000)
}

// FormatAtom renders uatom as a 2-decimal ATOM string (truncated).
func FormatAtom(m int64) string {
	return fmt.Sprintf("%d.%02d", m/Micro, (m%Micro)/10_000)
}

func toMicro(v float64) (int64, error) {
	if !(v > 0) {
		return 0, errf("ERR_BAD_AMOUNT", "amount must be above 0")
	}
	m := int64(v*float64(Micro) + 0.5)
	if m > maxAmount {
		return 0, errf("ERR_BAD_AMOUNT", "amount exceeds %s", FormatUSDC(maxAmount))
	}
	return m, nil
}
