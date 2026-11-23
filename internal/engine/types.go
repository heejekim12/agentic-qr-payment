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

type Asset string

const (
	ATOM    Asset = "ATOM"
	USDCINJ Asset = "USDC_INJ"
	INJ     Asset = "INJ"
)

var Assets = []Asset{ATOM, USDCINJ, INJ}

var assetLabel = map[Asset]string{ATOM: "ATOM", USDCINJ: "USDC.inj", INJ: "INJ"}

// Sample USD prices and swap/IBC route cost; replace with Skip Go quotes in production.
var (
	price       = map[Asset]float64{ATOM: 4.80, INJ: 9.50, USDCINJ: 1}
	routeFeeBps = map[Asset]int64{ATOM: 60, INJ: 30, USDCINJ: 0}
)

type chainRoute struct {
	Name   string
	Denom  string
	FeeBps int64
}

// injective-1 already holds USDC.inj, so a deposit from it needs no route.
var topupChains = map[string]chainRoute{
	"injective-1": {"Injective", "USDC.inj", 0},
	"8453":        {"Base", "USDC", 10},
	"1":           {"Ethereum", "USDC", 40},
	"solana":      {"Solana", "USDC", 20},
}

const (
	StatusActive    = "ACTIVE"
	StatusSuspended = "SUSPENDED"
)

type Merchant struct {
	ID             string `json:"merchant_id"`
	Name           string `json:"name"`
	Category       string `json:"category"`
	Status         string `json:"status"`
	SettlementAddr string `json:"settlement_addr"`
	FeeBps         int64  `json:"fee_bps"`
	PoolBps        int64  `json:"pool_bps"` // part of FeeBps that goes to the Reward Pool; the rest is Skip:Go revenue
	Tier           string `json:"tier"`
	PointEarnBps   int64  `json:"point_earn_bps"`
	Received       int64  `json:"received_micro"`
	PubKey         string `json:"pubkey"` // ed25519 public key that verifies this merchant's QR
	Area           string `json:"area"`
	Promo          *Promo `json:"promo,omitempty"` // merchant-approved happy hour, paid by the merchant
	Orders         int    `json:"orders"`
	Sales          int64  `json:"sales_micro"`
	Fees           int64  `json:"fees_micro"`
	PointComp      int64  `json:"point_comp_micro"` // POINT discounts paid to the merchant by the Reward Pool
	Refunds        int    `json:"refunds"`
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
	Payments      int                  `json:"payments"`
	GMV           int64                `json:"gmv_micro"`
	PaidUSDC      int64                `json:"paid_usdc_micro"`
	PointDiscount int64                `json:"point_discount_micro"`
	Refunds       int                  `json:"refunds"`
	Refunded      int64                `json:"refunded_micro"`
	OperatorFees  int64                `json:"operator_fees_micro"`
	TopUps        int                  `json:"topups"`
	TopUpVolume   int64                `json:"topup_micro"`
	PointsMinted  int64                `json:"points_minted"`
	PointsBurned  int64                `json:"points_burned"`
	Rejected      map[string]int       `json:"rejected"`
	Redeemed      map[Asset]*AssetStat `json:"redeemed"`
}

type AssetStat struct {
	Count  int     `json:"count"`
	Points int64   `json:"points"`
	Value  int64   `json:"value_micro"`
	Amount float64 `json:"amount"`
}

type Session struct {
	Active     bool  `json:"active"`
	PerTx      int64 `json:"per_tx_micro"`
	Daily      int64 `json:"daily_micro"`
	SpentToday int64 `json:"spent_today_micro"`
}

// Policy only tunes the agent's advice; it never authorizes the agent to act.
type Policy struct {
	Threshold int64 `json:"threshold"` // POINT balance at which the agent suggests redeeming
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
	Fee          int64  `json:"-"`
	PointsEarned int64  `json:"-"`
	PoolFee      int64  `json:"-"`
}

type User struct {
	Name             string           `json:"name"`
	Addr             string           `json:"addr"`
	CosmosAddr       string           `json:"cosmos_addr"`
	USDC             int64            `json:"usdc_micro"`
	Points           int64            `json:"points"`
	ATOM             float64          `json:"atom"`
	INJ              float64          `json:"inj"`
	Session          Session          `json:"session"`
	Policy           Policy           `json:"policy"`
	RedeemedToday    int64            `json:"redeemed_today_micro"`
	NextEarnBonusBps int64            `json:"next_earn_bonus_bps"`
	History          []*HistoryEntry  `json:"history"`
	Wallet           string           `json:"wallet,omitempty"`
	Area             string           `json:"area"`
	Profile          Profile          `json:"profile"`
	Holdings         map[string]int64 `json:"holdings_micro"` // sample USDC held per source chain
	day              string
	demoAddr         string
	demoCosmos       string
}

type Pool struct {
	Balance     int64   `json:"balance_micro"`
	FeesIn      int64   `json:"fees_in_micro"`
	DiscountOut int64   `json:"discount_out_micro"`
	RedeemOut   int64   `json:"redeem_out_micro"`
	Paused      bool    `json:"paused"`
	DailyCap    int64   `json:"daily_cap_micro"`
	GasINJ      float64 `json:"gas_inj"`
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

type RedeemQuote struct {
	Asset    Asset   `json:"asset"`
	Label    string  `json:"label"`
	Points   int64   `json:"points"`
	BoostBps int64   `json:"boost_bps"`
	Value    int64   `json:"value_micro"`
	Boost    int64   `json:"boost_micro"`
	Gross    int64   `json:"gross_micro"`
	Net      int64   `json:"net_micro"`
	Amount   float64 `json:"amount"`
	Best     bool    `json:"best"`
}

type TopupQuote struct {
	Chain  string `json:"chain"`
	Name   string `json:"name"`
	Denom  string `json:"denom"`
	Amount int64  `json:"amount_micro"`
	Fee    int64  `json:"fee_micro"`
	Out    int64  `json:"out_micro"`
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
	BonusApplied  bool   `json:"bonus_applied"`
	SignedBy      string `json:"signed_by"`
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

func validAsset(a Asset) bool {
	_, ok := assetLabel[a]
	return ok
}
