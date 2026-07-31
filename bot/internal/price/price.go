package price

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/flashswap/bot/internal/contract"
)

// V2 constants (0.3% fee = 997 / 1000)
var (
	B997   = big.NewInt(997)
	B1000  = big.NewInt(1000)
	Q96    = new(big.Int).Lsh(big.NewInt(1), 96)
	Q192   = new(big.Int).Lsh(big.NewInt(1), 192)
	Ten18  = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	Ten6   = new(big.Int).Exp(big.NewInt(10), big.NewInt(6), nil)
	Ten12  = new(big.Int).Exp(big.NewInt(10), big.NewInt(12), nil)
)

// V2AmountOut returns output from constant-product AMM for amountIn of reserveIn→reserveOut.
func V2AmountOut(amountIn, reserveIn, reserveOut *big.Int) *big.Int {
	numerator := new(big.Int).Mul(amountIn, B997)
	numerator.Mul(numerator, reserveOut)
	denominator := new(big.Int).Mul(reserveIn, B1000)
	denominator.Add(denominator, new(big.Int).Mul(amountIn, B997))
	return new(big.Int).Div(numerator, denominator)
}

// V2AmountIn returns required input given desired output.
func V2AmountIn(amountOut, reserveIn, reserveOut *big.Int) *big.Int {
	numerator := new(big.Int).Mul(reserveIn, amountOut)
	numerator.Mul(numerator, B1000)
	denominator := new(big.Int).Sub(reserveOut, amountOut)
	denominator.Mul(denominator, B997)
	result := new(big.Int).Div(numerator, denominator)
	return result.Add(result, big.NewInt(1))
}

// V2Reserves holds pair reserves.
type V2Reserves struct {
	Reserve0 *big.Int
	Reserve1 *big.Int
}

// PoolState holds V3 pool on-chain data for price computation.
type PoolState struct {
	SqrtPriceX96 *big.Int
	Liquidity    *big.Int
	Fee          int64
	Token0       common.Address // needed to know which token is which
}

// FetchPoolState reads slot0 and liquidity from a V3 pool.
func FetchPoolState(ctx context.Context, client *ethclient.Client, poolAddr common.Address) (*PoolState, bool) {
	// slot0()
	raw, err := client.CallContract(ctx, ethereum.CallMsg{
		To:   &poolAddr,
		Data: []byte{0x38, 0x50, 0xc7, 0xbd},
	}, nil)
	if err != nil {
		return nil, false
	}
	if len(raw) < 32 {
		return nil, false
	}

	sqrtPriceX96 := new(big.Int).SetBytes(raw[0:32])
	if sqrtPriceX96.Sign() == 0 {
		return nil, false
	}

	// liquidity()
	rawLiq, err := client.CallContract(ctx, ethereum.CallMsg{
		To:   &poolAddr,
		Data: []byte{0x1a, 0x68, 0x65, 0x02},
	}, nil)
	if err != nil {
		return nil, false
	}

	liquidity := new(big.Int).SetBytes(rawLiq)
	if liquidity.Sign() == 0 {
		return nil, false
	}

	return &PoolState{
		SqrtPriceX96: sqrtPriceX96,
		Liquidity:    liquidity,
	}, true
}

// V3SpotPrice returns spot price: how many USDT0 for 1 token (already in 6 decimals).
// tokenIsToken0: true if pool token0 is the arb token, USDT0 is token1.
func V3SpotPrice(sqrtPriceX96 *big.Int, tokenIsToken0 bool) *big.Float {
	// sqrtPrice = sqrtPriceX96 / 2^96
	// price_token0_in_token1 = sqrtPrice^2
	// If token is token0: price_token_in_USDT0 = price_token0_in_token1 = sqrtPrice^2
	// If token is token1: price_token_in_USDT0 = 1 / price_token0_in_token1 = 1 / sqrtPrice^2

	sqrtPrice := new(big.Float).SetInt(sqrtPriceX96)
	q96f := new(big.Float).SetInt(Q96)
	sqrtPrice.Quo(sqrtPrice, q96f) // sqrtPrice = sqrtPriceX96 / 2^96

	price := new(big.Float).Mul(sqrtPrice, sqrtPrice) // price = sqrtPrice^2

	if !tokenIsToken0 {
		// Token is token1, so price of token in USDT0 = 1/price
		one := new(big.Float).SetFloat64(1.0)
		price.Quo(one, price)
	}

	return price
}

// EstimateV3Output approximates V3 output for amountIn.
// This is a spot-price approximation: output ≈ amountIn * spotPrice * (1 - fee).
func EstimateV3Output(amountIn *big.Int, sqrtPriceX96 *big.Int, fee int64, tokenIsToken0 bool) *big.Int {
	if amountIn.Sign() == 0 {
		return big.NewInt(0)
	}

	spotPrice := V3SpotPrice(sqrtPriceX96, tokenIsToken0)

	// Convert amountIn (6 decimal token) to float for multiplication
	amountInFloat := new(big.Float).SetInt(amountIn)
	ten6f := new(big.Float).SetInt(Ten6)
	amountInFloat.Quo(amountInFloat, ten6f) // normalize to whole tokens

	outputFloat := new(big.Float).Mul(amountInFloat, spotPrice)

	// Apply fee: output * (1 - fee/1e6)
	feeRatio := new(big.Float).Quo(
		new(big.Float).SetInt64(1000000-fee),
		new(big.Float).SetInt64(1000000),
	)
	outputFloat.Mul(outputFloat, feeRatio)

	// Convert back to 6-decimal USDT0
	outputFloat.Mul(outputFloat, ten6f)

	output, _ := outputFloat.Int(nil)
	return output
}

// AddSlippage subtracts slippage (in bps) from an amount.
func AddSlippage(amount *big.Int, slippageBPS int64) *big.Int {
	num := new(big.Int).Mul(amount, big.NewInt(10000-slippageBPS))
	return num.Div(num, big.NewInt(10000))
}

// EstimateV3Buy estimates token output from selling USDT0 on V3.
// usdt0In is in USDT0 6-decimal units; returns token amount in token native decimals.
func EstimateV3Buy(usdt0In *big.Int, sqrtPriceX96 *big.Int, fee int64, tokenIsToken0 bool) *big.Int {
	if usdt0In.Sign() == 0 {
		return big.NewInt(0)
	}

	spotPrice := V3SpotPrice(sqrtPriceX96, tokenIsToken0)

	usdt0Float := new(big.Float).SetInt(usdt0In)

	// token = usdt0 / spotPrice (raw ratio handles decimal conversion)
	tokenFloat := new(big.Float).Quo(usdt0Float, spotPrice)

	// Apply fee
	feeRatio := new(big.Float).Quo(
		new(big.Float).SetInt64(1000000-fee),
		new(big.Float).SetInt64(1000000),
	)
	tokenFloat.Mul(tokenFloat, feeRatio)

	output, _ := tokenFloat.Int(nil)
	return output
}

// WgUSDTToUSDT0 converts a WgUSDT amount (18 decimals) to USDT0 (6 decimals).
func WgUSDTToUSDT0(wgusdtAmt *big.Int) *big.Int {
	return new(big.Int).Div(wgusdtAmt, Ten12)
}

// USDT0ToWgUSDT converts a USDT0 amount (6 decimals) to WgUSDT (18 decimals).
func USDT0ToWgUSDT(usdt0Amt *big.Int) *big.Int {
	return new(big.Int).Mul(usdt0Amt, Ten12)
}

// NonUSDT0 returns the non-USDT0 token from a pair and whether it's token0.
func NonUSDT0(token0, token1, usdt0 common.Address) (token common.Address, isToken0 bool) {
	if token0 == usdt0 {
		return token1, false
	}
	return token0, true
}

// FormatUSD formats a 6-decimal USDT0 value to string.
func FormatUSD(amount *big.Int) string {
	return formatDec(amount, 6)
}

// FormatToken formats a token amount with given decimals.
func FormatToken(amount *big.Int, decimals int64) string {
	return formatDec(amount, int(decimals))
}

func formatDec(amount *big.Int, decimals int) string {
	if amount == nil || amount.Sign() == 0 {
		return "0.00"
	}
	neg := amount.Sign() < 0
	s := new(big.Int).Abs(amount).String()
	if len(s) <= decimals {
		s = fmt.Sprintf("%0*s", decimals+1, s)
	}
	split := len(s) - decimals
	intPart := s[:split]
	decPart := s[split:]
	if intPart == "" {
		intPart = "0"
	}
	if len(decPart) > 2 {
		decPart = decPart[:2]
	}
	result := fmt.Sprintf("%s.%s", intPart, decPart)
	if neg {
		result = "-" + result
	}
	return result
}

// QuoteV3 calls Quoter V2 (tuple-style interface) for exact V3 swap output.
func QuoteV3(ctx context.Context, client *ethclient.Client, quoterAddr, tokenIn, tokenOut common.Address, fee int64, amountIn *big.Int) (*big.Int, error) {
	quoterABI, err := abi.JSON(strings.NewReader(contract.QuoterABI))
	if err != nil {
		return nil, err
	}

	// V2 tuple param order: (tokenIn, tokenOut, amountIn, fee, sqrtPriceLimitX96)
	input, err := quoterABI.Pack("quoteExactInputSingle", tokenIn, tokenOut, amountIn, big.NewInt(fee), common.Big0)
	if err != nil {
		return nil, err
	}

	raw, err := client.CallContract(ctx, ethereum.CallMsg{To: &quoterAddr, Data: input}, nil)
	if err != nil {
		return nil, err
	}

	// V2 returns (uint256 amountOut, uint160 sqrtPriceX96After, uint32 ticksCrossed, uint256 gasEstimate)
	// Extract first 32 bytes = amountOut.
	if len(raw) < 32 {
		return nil, fmt.Errorf("quoter: short response")
	}
	result := new(big.Int).SetBytes(raw[0:32])
	return result, nil
}
