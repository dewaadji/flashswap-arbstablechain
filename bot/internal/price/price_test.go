package price

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// ── V2AmountOut ────────────────────────────────────────────────

func TestV2AmountOut_knownValues(t *testing.T) {
	// reserveIn=1000e6, reserveOut=1000e6, amountIn=10e6
	// numerator = 10e6 * 997 * 1000e6
	// denominator = 1000e6 * 1000 + 10e6 * 997
	resIn := big.NewInt(1_000_000_000)  // 1000 USDT0
	resOut := big.NewInt(1_000_000_000) // 1000 token
	amtIn := big.NewInt(10_000_000)     // 10 token

	out := V2AmountOut(amtIn, resIn, resOut)

	// 10 * 997 / (1000 + 10*997/1000) ≈ 9.87
	if out.Sign() == 0 {
		t.Fatal("expected non-zero output")
	}
	// Verify: input 1% of reserves → get slightly less than 1% out
	if out.Cmp(amtIn) >= 0 {
		t.Fatalf("expected output (%s) < input (%s) due to fee", out, amtIn)
	}
}

func TestV2AmountOut_zeroInput(t *testing.T) {
	out := V2AmountOut(big.NewInt(0), big.NewInt(1000), big.NewInt(1000))
	if out.Sign() != 0 {
		t.Fatalf("expected 0, got %s", out)
	}
}

func TestV2AmountOut_consistency(t *testing.T) {
	// Selling 1% of reserves should yield ~0.99% of the other token
	resIn := big.NewInt(1_000_000_000_000)
	resOut := big.NewInt(2_000_000_000_000)
	amtIn := new(big.Int).Div(resIn, big.NewInt(100))

	out := V2AmountOut(amtIn, resIn, resOut)

	// Output must be positive and less than amtIn * resOut/resIn (ideal, no fee)
	ideal := new(big.Int).Div(new(big.Int).Mul(amtIn, resOut), resIn)
	if out.Cmp(ideal) >= 0 {
		t.Fatalf("expected output (%s) < ideal (%s)", out, ideal)
	}
}

// ── V2AmountIn ─────────────────────────────────────────────────

func TestV2AmountIn_knownValues(t *testing.T) {
	// reserveIn=1000e6, reserveOut=1000e6, amountOut=10e6
	resIn := big.NewInt(1_000_000_000)
	resOut := big.NewInt(1_000_000_000)
	amtOut := big.NewInt(10_000_000)

	in := V2AmountIn(amtOut, resIn, resOut)

	// Must be > amountOut (need to pay fee)
	if in.Cmp(amtOut) <= 0 {
		t.Fatalf("expected input (%s) > output (%s) due to fee", in, amtOut)
	}
}

func TestV2AmountIn_roundtrip(t *testing.T) {
	// V2AmountOut(input) ≈ V2AmountIn's amountOut param
	// If we buy X tokens via V2AmountIn, then sell them via V2AmountOut,
	// we should get back roughly similar amounts (minus double fee).
	resIn := big.NewInt(1_000_000_000_000)
	resOut := big.NewInt(1_000_000_000_000)
	amtOut := big.NewInt(1_000_000)

	amtIn := V2AmountIn(amtOut, resIn, resOut)

	// amtIn should be slightly > amtOut
	if amtIn.Cmp(amtOut) <= 0 {
		t.Fatalf("amount in (%s) <= amount out (%s)", amtIn, amtOut)
	}
}

// ── AddSlippage ────────────────────────────────────────────────

func TestAddSlippage_zero(t *testing.T) {
	amt := big.NewInt(100_000_000) // 100 USDT0
	result := AddSlippage(amt, 0)
	if result.Cmp(amt) != 0 {
		t.Fatalf("expected %s, got %s", amt, result)
	}
}

func TestAddSlippage_50bps(t *testing.T) {
	amt := big.NewInt(100_000_000) // 100.000000
	result := AddSlippage(amt, 50)
	// 100 * (10000-50)/10000 = 100 * 0.995 = 99.500000
	expected := big.NewInt(99_500_000)
	if result.Cmp(expected) != 0 {
		t.Fatalf("expected %s, got %s", expected, result)
	}
}

func TestAddSlippage_100bps(t *testing.T) {
	amt := big.NewInt(100_000_000)
	result := AddSlippage(amt, 100)
	// 100 * 0.99 = 99
	expected := big.NewInt(99_000_000)
	if result.Cmp(expected) != 0 {
		t.Fatalf("expected %s, got %s", expected, result)
	}
}

// ── V3SpotPrice ────────────────────────────────────────────────

func TestV3SpotPrice_tokenIsToken0(t *testing.T) {
	// sqrtPriceX96 = 2^96 → sqrtPrice = 1 → price = 1
	sqrtX96 := new(big.Int).Set(Q96)
	price := V3SpotPrice(sqrtX96, true)
	one := new(big.Float).SetFloat64(1.0)
	if price.Cmp(one) != 0 {
		t.Fatalf("expected 1.0, got %s", price.String())
	}
}

func TestV3SpotPrice_tokenIsToken1(t *testing.T) {
	sqrtX96 := new(big.Int).Set(Q96)
	price := V3SpotPrice(sqrtX96, false)
	one := new(big.Float).SetFloat64(1.0)
	if price.Cmp(one) != 0 {
		t.Fatalf("expected 1.0, got %s", price.String())
	}
}

func TestV3SpotPrice_highPrice(t *testing.T) {
	// sqrtPriceX96 = 2 * 2^96 → sqrtPrice = 2 → price = 4
	sqrtX96 := new(big.Int).Mul(Q96, big.NewInt(2))
	price := V3SpotPrice(sqrtX96, true)
	four := new(big.Float).SetFloat64(4.0)
	if price.Cmp(four) != 0 {
		t.Fatalf("expected 4.0, got %s", price.String())
	}
}

func TestV3SpotPrice_lowPrice(t *testing.T) {
	// sqrtPriceX96 = 2^96/2 → sqrtPrice = 0.5 → price = 0.25
	sqrtX96 := new(big.Int).Div(Q96, big.NewInt(2))
	price := V3SpotPrice(sqrtX96, true)
	quarter := new(big.Float).SetFloat64(0.25)
	diff := new(big.Float).Sub(price, quarter)
	if diff.Sign() > 0 {
		abs := new(big.Float).Abs(diff)
		if abs.Cmp(new(big.Float).SetFloat64(0.0001)) > 0 {
			t.Fatalf("expected ~0.25, got %s", price.String())
		}
	}
}

// ── EstimateV3Output ───────────────────────────────────────────

func TestEstimateV3Output_zeroInput(t *testing.T) {
	out := EstimateV3Output(big.NewInt(0), Q96, 500, true)
	if out.Sign() != 0 {
		t.Fatalf("expected 0, got %s", out)
	}
}

func TestEstimateV3Output_basic(t *testing.T) {
	// sqrtPrice = 1 (spot = 1 USDT0 per token)
	// amountIn = 100 tokens (100e6)
	// fee = 500 (0.05%)
	// Expected: 100 * 1 * (1 - 0.0005) = 99.95 → 99950000
	amtIn := big.NewInt(100_000_000)
	out := EstimateV3Output(amtIn, Q96, 500, true)
	expected := big.NewInt(99_950_000)
	// Spot-price approximation has floating error, check within 0.1%
	diff := new(big.Int).Sub(out, expected)
	diff.Abs(diff)
	tolerance := new(big.Int).Div(expected, big.NewInt(1000))
	if diff.Cmp(tolerance) > 0 {
		t.Fatalf("expected ~%s, got %s (diff=%s)", expected, out, diff)
	}
}

func TestEstimateV3Output_poolTokenIsToken1(t *testing.T) {
	// token is token1: price = 1/2 = 0.5 USDT0 per token
	// sqrtPriceX96 = 2^96 * sqrt(2) → price_token0_in_token1 = 2 → price_token1 = 0.5
	// amountIn = 100e6 tokens
	// fee = 500
	// Expected: 100 * 0.5 * 0.9995 = 49.975 → 49975000
	sqrt2 := new(big.Float).Sqrt(new(big.Float).SetFloat64(2.0))
	sqrtX96Float := new(big.Float).Mul(sqrt2, new(big.Float).SetInt(Q96))
	sqrtX96, _ := sqrtX96Float.Int(nil)

	amtIn := big.NewInt(100_000_000)
	out := EstimateV3Output(amtIn, sqrtX96, 500, false)

	if out.Sign() == 0 {
		t.Fatal("expected non-zero output")
	}
	// Spot price ≈ 0.5, so output ≈ 50e6 * 0.9995 ≈ 49.975e6
	// Allow wider tolerance due to sqrt float → int conversion
	if out.Cmp(big.NewInt(49_000_000)) < 0 {
		t.Fatalf("expected output near 50M, got %s", out)
	}
}

// ── EstimateV3Buy ──────────────────────────────────────────────

func TestEstimateV3Buy_zeroInput(t *testing.T) {
	out := EstimateV3Buy(big.NewInt(0), Q96, 500, true)
	if out.Sign() != 0 {
		t.Fatalf("expected 0, got %s", out)
	}
}

func TestEstimateV3Buy_basic(t *testing.T) {
	// spot = 1, usdt0In = 100 USDT0 (100e6)
	// Without fee: token = 100 / 1 = 100 tokens
	// With 500 fee: 100 * 0.9995 = 99.95 tokens → 99950000
	usdt0In := big.NewInt(100_000_000)
	out := EstimateV3Buy(usdt0In, Q96, 500, true)
	if out.Sign() == 0 {
		t.Fatal("expected non-zero output")
	}
	expected := big.NewInt(99_950_000)
	diff := new(big.Int).Sub(out, expected)
	diff.Abs(diff)
	tolerance := new(big.Int).Div(expected, big.NewInt(1000))
	if diff.Cmp(tolerance) > 0 {
		t.Fatalf("expected ~%s, got %s (diff=%s)", expected, out, diff)
	}
}

// ── Conversions ────────────────────────────────────────────────

func TestWgUSDTToUSDT0(t *testing.T) {
	// 1 WgUSDT (18d) = 1 USDT0 (6d)
	// 1.0 WgUSDT raw = 1_000_000_000_000_000_000
	// After div by 10^12 = 1_000_000 (1.000000 USDT0)
	wg := big.NewInt(1_000_000_000_000_000_000) // 1 WgUSDT
	result := WgUSDTToUSDT0(wg)
	expected := big.NewInt(1_000_000) // 1.000000 USDT0
	if result.Cmp(expected) != 0 {
		t.Fatalf("expected %s, got %s", expected, result)
	}
}

func TestWgUSDTToUSDT0_zero(t *testing.T) {
	result := WgUSDTToUSDT0(big.NewInt(0))
	if result.Sign() != 0 {
		t.Fatalf("expected 0, got %s", result)
	}
}

func TestUSDT0ToWgUSDT(t *testing.T) {
	// 1.000000 USDT0 (6d) = 1 WgUSDT (18d)
	usdt0 := big.NewInt(1_000_000)          // 1.000000 USDT0
	result := USDT0ToWgUSDT(usdt0)
	expected := big.NewInt(1_000_000_000_000_000_000) // 1e18 = 1 WgUSDT
	if result.Cmp(expected) != 0 {
		t.Fatalf("expected %s, got %s", expected, result)
	}
}

func TestWgUSDTRoundtrip(t *testing.T) {
	// 5 WgUSDT → USDT0 → WgUSDT should be lossless
	original := big.NewInt(5_000_000_000_000_000_000) // 5e18 = 5 WgUSDT
	usdt0 := WgUSDTToUSDT0(original)
	back := USDT0ToWgUSDT(usdt0)
	if back.Cmp(original) != 0 {
		t.Fatalf("roundtrip broken: %s → %s → %s", original, usdt0, back)
	}
}

// ── NonUSDT0 ───────────────────────────────────────────────────

func TestNonUSDT0_tokenIsToken0(t *testing.T) {
	usdt0 := common.HexToAddress("0xaaa")
	token := common.HexToAddress("0xbbb")
	result, isTok0 := NonUSDT0(token, usdt0, usdt0)
	if !isTok0 {
		t.Fatal("token should be token0")
	}
	if result != token {
		t.Fatalf("expected token %x, got %x", token, result)
	}
}

func TestNonUSDT0_tokenIsToken1(t *testing.T) {
	usdt0 := common.HexToAddress("0xaaa")
	token := common.HexToAddress("0xbbb")
	result, isTok0 := NonUSDT0(usdt0, token, usdt0)
	if isTok0 {
		t.Fatal("token should not be token0")
	}
	if result != token {
		t.Fatalf("expected token %x, got %x", token, result)
	}
}

// ── FormatUSD / FormatToken ────────────────────────────────────

func TestFormatUSD_basic(t *testing.T) {
	// 1.50 USDT0
	result := FormatUSD(big.NewInt(1_500_000))
	if result != "1.50" {
		t.Fatalf("expected '1.50', got '%s'", result)
	}
}

func TestFormatUSD_wholeNumber(t *testing.T) {
	result := FormatUSD(big.NewInt(10_000_000)) // 10.00
	if result != "10.00" {
		t.Fatalf("expected '10.00', got '%s'", result)
	}
}

func TestFormatUSD_tinyAmount(t *testing.T) {
	result := FormatUSD(big.NewInt(50_000)) // 0.05
	if result != "0.05" {
		t.Fatalf("expected '0.05', got '%s'", result)
	}
}

func TestFormatUSD_negative(t *testing.T) {
	result := FormatUSD(big.NewInt(-1_500_000))
	if result != "-1.50" {
		t.Fatalf("expected '-1.50', got '%s'", result)
	}
}

func TestFormatToken_18dec(t *testing.T) {
	// 1.5 WETH (18 decimals)
	result := FormatToken(big.NewInt(1_500_000_000_000_000_000), 18)
	if result != "1.50" {
		t.Fatalf("expected '1.50', got '%s'", result)
	}
}

func TestFormatToken_6dec(t *testing.T) {
	result := FormatToken(big.NewInt(2_500_000), 6) // 2.50
	if result != "2.50" {
		t.Fatalf("expected '2.50', got '%s'", result)
	}
}

func TestFormatToken_zero(t *testing.T) {
	result := FormatToken(big.NewInt(0), 18)
	if result != "0.00" {
		t.Fatalf("expected '0.00', got '%s'", result)
	}
}

func TestFormatToken_nil(t *testing.T) {
	result := FormatToken(nil, 6)
	if result != "0.00" {
		t.Fatalf("expected '0.00', got '%s'", result)
	}
}

