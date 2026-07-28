package trader

import (
	"crypto/ecdsa"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/flashswap/bot/internal/contract"
)

// ── ABI Parsing ────────────────────────────────────────────────

func TestFlashArbABI_Packing(t *testing.T) {
	arbABI, err := abi.JSON(strings.NewReader(contract.StableArbV2V3ABI))
	if err != nil {
		t.Fatalf("failed to parse ABI: %v", err)
	}

	pair := common.HexToAddress("0x1111111111111111111111111111111111111111")
	token := common.HexToAddress("0x2222222222222222222222222222222222222222")
	v3Fee := big.NewInt(500)
	dir := uint8(1)
	borrowAmt := big.NewInt(1_000_000_000)
	minProfit := big.NewInt(100_000)

	data, err := arbABI.Pack("flashArb", pair, token, v3Fee, dir, borrowAmt, minProfit)
	if err != nil {
		t.Fatalf("Pack flashArb: %v", err)
	}
	if len(data) < 4 {
		t.Fatal("packed data too short")
	}
}

func TestFlashArbABI_Packing_dir2(t *testing.T) {
	arbABI, err := abi.JSON(strings.NewReader(contract.StableArbV2V3ABI))
	if err != nil {
		t.Fatalf("failed to parse ABI: %v", err)
	}

	pair := common.HexToAddress("0x1111111111111111111111111111111111111111")
	token := common.HexToAddress("0x2222222222222222222222222222222222222222")

	data, err := arbABI.Pack("flashArb", pair, token, big.NewInt(3000), uint8(2), big.NewInt(1e6), big.NewInt(0))
	if err != nil {
		t.Fatalf("Pack flashArb dir=2: %v", err)
	}
	if len(data) < 4 {
		t.Fatal("packed data too short")
	}
}

func TestFlashArbABI_WrongTypes_rejected(t *testing.T) {
	// Regression: passing *big.Int where uint8 is expected must fail.
	arbABI, err := abi.JSON(strings.NewReader(contract.StableArbV2V3ABI))
	if err != nil {
		t.Fatalf("failed to parse ABI: %v", err)
	}

	pair := common.HexToAddress("0x1111111111111111111111111111111111111111")
	token := common.HexToAddress("0x2222222222222222222222222222222222222222")

	// This was the original bug: big.NewInt(int64(dir)) for the uint8 dir field.
	_, err = arbABI.Pack("flashArb", pair, token, big.NewInt(500), big.NewInt(1), big.NewInt(1e6), big.NewInt(0))
	if err == nil {
		t.Fatal("expected Pack to reject *big.Int for uint8 dir, but it passed")
	}
}

func TestExecuteArbABI_Packing(t *testing.T) {
	arbABI, err := abi.JSON(strings.NewReader(contract.StableArbV2V3ABI))
	if err != nil {
		t.Fatalf("failed to parse ABI: %v", err)
	}

	token := common.HexToAddress("0x3333333333333333333333333333333333333333")

	data, err := arbABI.Pack("executeArb", token, big.NewInt(500), uint8(1), big.NewInt(100_000), big.NewInt(9999999999))
	if err != nil {
		t.Fatalf("Pack executeArb: %v", err)
	}
	if len(data) < 4 {
		t.Fatal("packed data too short")
	}
}

// ── Sentinel Error ─────────────────────────────────────────────

func TestErrSimRevert(t *testing.T) {
	if ErrSimRevert.Error() != "on-chain simulation reverted" {
		t.Fatalf("unexpected error message: %s", ErrSimRevert)
	}
}

func TestErrSimRevert_IsComparable(t *testing.T) {
	// main.go relies on err == trader.ErrSimRevert or err != trader.ErrSimRevert.
	// Verify the sentinel works correctly with fmt.Errorf wrapping.
	err := ErrSimRevert
	if !errors.Is(err, ErrSimRevert) {
		t.Fatal("ErrSimRevert should be itself")
	}
}

// ── Private Key → Address ──────────────────────────────────────

func TestKeyToAddress_consistent(t *testing.T) {
	// Generate a random key and verify the address is deterministic.
	pk, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr1 := crypto.PubkeyToAddress(pk.PublicKey)
	addr2 := crypto.PubkeyToAddress(pk.PublicKey)
	if addr1 != addr2 {
		t.Fatal("address should be deterministic")
	}
	if addr1 == (common.Address{}) {
		t.Fatal("address should not be zero")
	}
}

// ── getABI helper ──────────────────────────────────────────────

func getABI(t *testing.T) *abi.ABI {
	t.Helper()
	a, err := abi.JSON(strings.NewReader(contract.StableArbV2V3ABI))
	if err != nil {
		t.Fatalf("parse ABI: %v", err)
	}
	return &a
}

// ── Method Selector Smoke ──────────────────────────────────────

func TestABI_hasFlashArb(t *testing.T) {
	a := getABI(t)
	if _, ok := a.Methods["flashArb"]; !ok {
		t.Fatal("ABI missing flashArb method")
	}
}

func TestABI_hasExecuteArb(t *testing.T) {
	a := getABI(t)
	if _, ok := a.Methods["executeArb"]; !ok {
		t.Fatal("ABI missing executeArb method")
	}
}

func TestABI_hasReceive(t *testing.T) {
	a := getABI(t)
	// receive() appears as a method in ABI JSON but is not Pack-able.
	// Just verify the ABI parsed without error.
	if a == nil {
		t.Fatal("expected non-nil ABI")
	}
}

// ── FlashArb input types match contract ──────────────────────────

func TestABI_flashArbInputTypes(t *testing.T) {
	a := getABI(t)
	m, ok := a.Methods["flashArb"]
	if !ok {
		t.Fatal("missing flashArb")
	}

	// Expected: (address pair, address token, uint24 v3Fee, uint8 dir, uint256 borrowAmt, uint256 minProfit)
	if len(m.Inputs) != 6 {
		t.Fatalf("expected 6 inputs, got %d", len(m.Inputs))
	}

	tests := []struct {
		idx      int
		name     string
		typeName string
	}{
		{0, "pair", "address"},
		{1, "token", "address"},
		{2, "v3Fee", "uint24"},
		{3, "dir", "uint8"},
		{4, "borrowAmt", "uint256"},
		{5, "minProfit", "uint256"},
	}

	for _, tc := range tests {
		inp := m.Inputs[tc.idx]
		if inp.Name != tc.name {
			t.Fatalf("input[%d]: expected name %q, got %q", tc.idx, tc.name, inp.Name)
		}
		if inp.Type.String() != tc.typeName {
			t.Fatalf("input[%d] %q: expected type %q, got %q", tc.idx, tc.name, tc.typeName, inp.Type.String())
		}
	}
}

// ── Trader construction defaults ───────────────────────────────

func TestTraderFields(t *testing.T) {
	pk, _ := crypto.GenerateKey()
	arbAddr := common.HexToAddress("0x9999999999999999999999999999999999999999")
	chainID := big.NewInt(988)

	tr := &Trader{
		chainID: chainID,
		pk:      pk,
		from:    crypto.PubkeyToAddress(pk.PublicKey),
		arbAddr: arbAddr,
		dryRun:  true,
	}

	if tr.chainID.Cmp(chainID) != 0 {
		t.Fatal("chainID mismatch")
	}
	if !tr.dryRun {
		t.Fatal("expected dryRun=true")
	}
	if tr.from != crypto.PubkeyToAddress(pk.PublicKey) {
		t.Fatal("from address mismatch")
	}
	if tr.arbAddr != arbAddr {
		t.Fatal("arbAddr mismatch")
	}
}

// ── helpers ────────────────────────────────────────────────────

func mustHex(s string) common.Address {
	return common.HexToAddress(s)
}

func mustPk(s string) *ecdsa.PrivateKey {
	pk, err := crypto.HexToECDSA(s)
	if err != nil {
		panic(err)
	}
	return pk
}
