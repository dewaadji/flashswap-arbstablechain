package discover

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

var feeTiers = []int64{100, 500, 3000, 10000}

type PairInfo struct {
	Address common.Address
	Token0  common.Address
	Token1  common.Address
}

type PoolInfo struct {
	Address common.Address
	Token   common.Address // the non-USDT0 token
	Fee     int64
}

// Function selectors
var (
	selAllPairsLength = []byte{0x57, 0x4f, 0x2b, 0xa3}
	selToken0         = []byte{0x0d, 0xfe, 0x16, 0x81}
	selToken1         = []byte{0xd2, 0x12, 0x20, 0xa7}
	selLiquidity      = []byte{0x1a, 0x68, 0x65, 0x02}
	selDecimals       = []byte{0x31, 0x3c, 0xe5, 0x67}
)

// TokenDecimals fetches token decimals. Returns 18 as default on error.
func TokenDecimals(ctx context.Context, client *ethclient.Client, token common.Address) int64 {
	raw, err := client.CallContract(ctx, ethereum.CallMsg{To: &token, Data: selDecimals}, nil)
	if err != nil || len(raw) < 32 {
		return 18
	}
	return new(big.Int).SetBytes(raw).Int64()
}

func V2Pairs(ctx context.Context, client *ethclient.Client, factoryAddr, usdt0 common.Address) ([]PairInfo, error) {
	factoryABI, err := abi.JSON(strings.NewReader(contract.V2FactoryABI))
	if err != nil {
		return nil, err
	}

	raw, err := client.CallContract(ctx, ethereum.CallMsg{To: &factoryAddr, Data: selAllPairsLength}, nil)
	if err != nil {
		return nil, fmt.Errorf("allPairsLength: %w", err)
	}
	total := new(big.Int).SetBytes(raw)

	pairs := make([]PairInfo, 0)
	for i := int64(0); i < total.Int64(); i++ {
		input, err := factoryABI.Pack("allPairs", big.NewInt(i))
		if err != nil {
			continue
		}
		raw, err := client.CallContract(ctx, ethereum.CallMsg{To: &factoryAddr, Data: input}, nil)
		if err != nil {
			continue
		}
		pairAddr := common.BytesToAddress(raw)

		t0Raw, _ := client.CallContract(ctx, ethereum.CallMsg{To: &pairAddr, Data: selToken0}, nil)
		t1Raw, _ := client.CallContract(ctx, ethereum.CallMsg{To: &pairAddr, Data: selToken1}, nil)
		if len(t0Raw) < 32 || len(t1Raw) < 32 {
			continue
		}
		t0 := common.BytesToAddress(t0Raw)
		t1 := common.BytesToAddress(t1Raw)

		if t0 == usdt0 || t1 == usdt0 {
			pairs = append(pairs, PairInfo{Address: pairAddr, Token0: t0, Token1: t1})
		}
	}

	return pairs, nil
}

func V3Pools(ctx context.Context, client *ethclient.Client, factoryAddr, usdt0 common.Address, tokens []common.Address) ([]PoolInfo, error) {
	factoryABI, err := abi.JSON(strings.NewReader(contract.V3FactoryABI))
	if err != nil {
		return nil, err
	}

	seen := make(map[common.Address]bool)
	pools := make([]PoolInfo, 0)

	for _, token := range tokens {
		if token == usdt0 {
			continue
		}
		for _, fee := range feeTiers {
			input, err := factoryABI.Pack("getPool", token, usdt0, big.NewInt(fee))
			if err != nil {
				continue
			}
			raw, err := client.CallContract(ctx, ethereum.CallMsg{To: &factoryAddr, Data: input}, nil)
			if err != nil {
				continue
			}
			poolAddr := common.BytesToAddress(raw)
			if poolAddr == (common.Address{}) {
				continue
			}
			if seen[poolAddr] {
				continue
			}
			seen[poolAddr] = true

			// Check liquidity — skip dead pools
			liq, _ := client.CallContract(ctx, ethereum.CallMsg{To: &poolAddr, Data: selLiquidity}, nil)
			if liq == nil || new(big.Int).SetBytes(liq).Sign() == 0 {
				continue
			}

			pools = append(pools, PoolInfo{Address: poolAddr, Token: token, Fee: fee})
			// don't break — check remaining fee tiers for additional liquid pools
		}
	}

	return pools, nil
}
