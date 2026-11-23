package engine

import (
	"encoding/hex"
	"fmt"
	"strings"
)

type WalletRequest struct {
	User       int    `json:"user"`
	Wallet     string `json:"wallet"`      // keplr or metamask
	Addr       string `json:"addr"`        // keplr: inj1 address
	CosmosAddr string `json:"cosmos_addr"` // keplr: cosmos1 address
	EthAddr    string `json:"eth_addr"`    // metamask: 0x address
}

// ConnectWallet replaces the demo addresses with the wallet's addresses.
// Only addresses are used; balances stay simulated and nothing is signed.
func (e *Engine) ConnectWallet(req WalletRequest) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	u, err := e.user(req.User)
	if err != nil {
		return err
	}
	var label, note string
	switch req.Wallet {
	case "keplr":
		if !validBech32(req.Addr, "inj") {
			return errf("ERR_BAD_ADDRESS", "addr must be a valid inj1 address")
		}
		if !validBech32(req.CosmosAddr, "cosmos") {
			return errf("ERR_BAD_ADDRESS", "cosmos_addr must be a valid cosmos1 address")
		}
		u.Addr, u.CosmosAddr, label = req.Addr, req.CosmosAddr, "Keplr"
	case "metamask":
		inj, ok := injFromEth(req.EthAddr)
		if !ok {
			return errf("ERR_BAD_ADDRESS", "eth_addr must be a 0x address with 40 hex characters")
		}
		// MetaMask has no Cosmos Hub key, so the ATOM destination stays the demo cosmos1 address.
		u.Addr, u.CosmosAddr, label = inj, u.demoCosmos, "MetaMask"
		note = " ATOM still goes to the demo cosmos1 address."
	default:
		return errf("ERR_UNSUPPORTED_WALLET", "wallet must be keplr or metamask")
	}
	u.Wallet = req.Wallet
	e.log("ok", u.Addr+" ("+label+")", "wallet connect", fmt.Sprintf("%s now uses %s. Address only, no signature requested.%s", u.Name, u.Addr, note))
	return nil
}

type DisconnectRequest struct {
	User int `json:"user"`
}

func (e *Engine) DisconnectWallet(req DisconnectRequest) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	u, err := e.user(req.User)
	if err != nil {
		return err
	}
	u.Addr, u.CosmosAddr, u.Wallet = u.demoAddr, u.demoCosmos, ""
	e.log("sec", u.Addr, "wallet disconnect", u.Name+" back to the demo address")
	return nil
}

// injFromEth converts an EVM address to its Injective bech32 form (same 20 bytes).
func injFromEth(s string) (string, bool) {
	if len(s) != 42 || !strings.HasPrefix(s, "0x") {
		return "", false
	}
	b, err := hex.DecodeString(s[2:])
	if err != nil {
		return "", false
	}
	return bech32Encode("inj", b), true
}

const bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

// validBech32 checks the HRP, charset, length and BIP-173 checksum.
func validBech32(s, hrp string) bool {
	if len(s) > 90 || s != strings.ToLower(s) || !strings.HasPrefix(s, hrp+"1") {
		return false
	}
	data := s[len(hrp)+1:]
	if len(data) < 38 { // 20-byte address (32 chars) + 6-char checksum
		return false
	}
	values := hrpExpand(hrp)
	for _, c := range data {
		i := strings.IndexRune(bech32Charset, c)
		if i < 0 {
			return false
		}
		values = append(values, i)
	}
	return polymod(values) == 1
}

// bech32Encode implements BIP-173 encoding of 8-bit data.
func bech32Encode(hrp string, data []byte) string {
	var conv []int
	acc, bits := 0, 0
	for _, b := range data {
		acc = acc<<8 | int(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			conv = append(conv, (acc>>bits)&31)
		}
	}
	if bits > 0 {
		conv = append(conv, (acc<<(5-bits))&31)
	}
	mod := polymod(append(append(hrpExpand(hrp), conv...), 0, 0, 0, 0, 0, 0)) ^ 1
	var sb strings.Builder
	sb.WriteString(hrp)
	sb.WriteByte('1')
	for _, v := range conv {
		sb.WriteByte(bech32Charset[v])
	}
	for i := range 6 {
		sb.WriteByte(bech32Charset[(mod>>(5*(5-i)))&31])
	}
	return sb.String()
}

func hrpExpand(hrp string) []int {
	v := make([]int, 0, len(hrp)*2+1)
	for _, c := range hrp {
		v = append(v, int(c)>>5)
	}
	v = append(v, 0)
	for _, c := range hrp {
		v = append(v, int(c)&31)
	}
	return v
}

func polymod(values []int) int {
	gen := []int{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
	chk := 1
	for _, v := range values {
		top := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ v
		for i := range 5 {
			if (top>>i)&1 == 1 {
				chk ^= gen[i]
			}
		}
	}
	return chk
}
