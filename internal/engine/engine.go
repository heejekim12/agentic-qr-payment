// Package engine simulates the on-chain contracts: Prepaid Vault, Whitelist Registry,
// and soulbound POINT. State is in memory and resets on restart.
package engine

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

type Engine struct {
	mu           sync.Mutex
	users        []*User
	merchants    []*Merchant
	gasINJ       float64                       // platform feegrant budget
	merchantKeys map[string]ed25519.PrivateKey // held by each merchant POS in production
	paid         map[string]bool
	seq          int
	hid          int
	events       []Event
	secret       []byte
	now          func() time.Time
	txSeq        int
	curTx        string
	stats        Stats
	atomPrice    PriceInfo
}

func New() *Engine {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		panic(err)
	}
	e := &Engine{
		users: []*User{
			newUser("Minji", "inj1user987654321", 25),
			newUser("Jaehyun", "inj1user123450987", 80),
			newUser("Sora", "inj1user555000111", 5),
		},
		merchants: []*Merchant{
			{ID: "MRC_BAR_001", Name: "Seongsu Pub", Category: "BAR", Status: StatusActive, SettlementAddr: "inj1abc...xyz", SubscriptionKRW: 0, PointEarnBps: 500, AtomBonusBps: 200, Tier: "Small"},
			{ID: "MRC_CAFE_002", Name: "Yeonnam Drip Coffee", Category: "CAFE", Status: StatusActive, SettlementAddr: "inj1def...uvw", SubscriptionKRW: 0, PointEarnBps: 500, AtomBonusBps: 200, Tier: "Small"},
			{ID: "MRC_REST_014", Name: "Euljiro Noodle House", Category: "RESTAURANT", Status: StatusActive, SettlementAddr: "inj1ghi...rst", SubscriptionKRW: 30_000, PointEarnBps: 300, AtomBonusBps: 200, Tier: "SME 0.3-0.5B"},
			{ID: "MRC_REST_031", Name: "Mangwon Bistro", Category: "RESTAURANT", Status: StatusSuspended, SettlementAddr: "inj1jkl...opq", SubscriptionKRW: 30_000, PointEarnBps: 300, AtomBonusBps: 200, Tier: "SME 0.3-0.5B"},
		},
		gasINJ:       25,
		merchantKeys: map[string]ed25519.PrivateKey{},
		paid:         map[string]bool{},
		seq:          42,
		secret:       secret,
		now:          time.Now,
	}
	e.stats.Rejected = map[string]int{}
	e.seedSample()
	for _, m := range e.merchants {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			panic(err)
		}
		m.PubKey = hex.EncodeToString(pub)
		e.merchantKeys[m.ID] = priv
	}
	for _, u := range e.users {
		u.day = e.today()
		e.newTx()
		e.log("ok", u.Addr+" (wallet)", "Vault.RegisterSession",
			fmt.Sprintf("%s · $%s/tx · $%s/day · registry merchants only", u.Name, FormatUSDC(u.Session.PerTx), FormatUSDC(u.Session.Daily)))
	}
	return e
}

func newUser(name, addr string, usdc int64) *User {
	return &User{
		Name: name, Addr: addr, demoAddr: addr,
		USDC: usdc * Micro, Points: map[string]int64{},
		Session: Session{Active: true, PerTx: 50 * Micro, Daily: 200 * Micro},
		History: []*HistoryEntry{},
	}
}

// atomPriceView reports the price in use, including the sample fallback.
func (e *Engine) atomPriceView() PriceInfo {
	if e.atomPrice.USD > 0 {
		return e.atomPrice
	}
	return PriceInfo{USD: sampleATOMUSD, Source: "sample"}
}

func (e *Engine) today() string { return e.now().Format("2006-01-02") }

// touch resets per-user daily counters when the date changes.
func (e *Engine) touch(u *User) {
	if d := e.today(); u.day != d {
		u.day, u.Session.SpentToday = d, 0
	}
}

func (e *Engine) user(i int) (*User, error) {
	if i < 0 || i >= len(e.users) {
		return nil, errf("ERR_UNKNOWN_USER", "user %d does not exist", i)
	}
	u := e.users[i]
	e.touch(u)
	return u, nil
}

func (e *Engine) merchant(id string) *Merchant {
	for _, m := range e.merchants {
		if m.ID == id {
			return m
		}
	}
	return nil
}

// newTx starts a simulated on-chain transaction; its hash is attached to the
// contract events and history entries written until the next call.
func (e *Engine) newTx() {
	e.txSeq++
	mac := hmac.New(sha256.New, e.secret)
	fmt.Fprintf(mac, "tx|%d|%d", e.txSeq, e.now().UnixNano())
	e.curTx = strings.ToUpper(hex.EncodeToString(mac.Sum(nil)))
}

// onChain reports whether a log message is a contract call that would have a tx hash.
func onChain(msg string) bool {
	for _, p := range []string{"Vault.", "Registry."} {
		if strings.HasPrefix(msg, p) {
			return true
		}
	}
	return false
}

func (e *Engine) log(kind, signer, msg, body string) {
	ev := Event{Time: e.now(), Kind: kind, Signer: signer, Msg: msg, Body: body}
	if onChain(msg) {
		ev.TxHash = e.curTx
	}
	e.events = append([]Event{ev}, e.events...)
	if len(e.events) > maxEvents {
		e.events = e.events[:maxEvents]
	}
}

func (e *Engine) addHistory(u *User, h *HistoryEntry) *HistoryEntry {
	e.hid++
	h.ID, h.Time, h.TxHash = e.hid, e.now(), e.curTx
	u.History = append([]*HistoryEntry{h}, u.History...)
	return h
}

type totals struct {
	Liability int64   `json:"liability_micro"` // unused POINT at face value
	Vaults    int64   `json:"vaults_micro"`
	VaultATOM int64   `json:"vault_atom_micro"`
	GasINJ    float64 `json:"gas_inj"`
}

type state struct {
	Users     []*User     `json:"users"`
	Merchants []*Merchant `json:"merchants"`
	Totals    totals      `json:"totals"`
	Events    []Event     `json:"events"`
	Stats     Stats       `json:"stats"`
	AtomPrice PriceInfo   `json:"atom_price"`
}

// StateJSON returns the full simulated chain state.
func (e *Engine) StateJSON() ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	pv := totals{GasINJ: e.gasINJ}
	now := e.now()
	for _, u := range e.users {
		e.touch(u)
		for _, p := range u.Points {
			pv.Liability += p * PointValue
		}
		pv.Vaults += u.USDC
		pv.VaultATOM += u.ATOM
		for _, h := range u.History {
			h.Refundable = h.Type == "PAY" && h.Status == "PAID" && now.Sub(h.Time) < RefundWindow
		}
	}
	return json.Marshal(state{Users: e.users, Merchants: e.merchants, Totals: pv, Events: e.events, Stats: e.stats, AtomPrice: e.atomPriceView()})
}
