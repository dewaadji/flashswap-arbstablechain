package simulate

import (
	"context"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/flashswap/bot/internal/contract"
)

// Dir2Result holds simulation output.
type Dir2Result struct {
	Success bool
	Profit  *big.Int
	Error   string
}

// Dir2 simulates a direction-2 flashArb via eth_call.
// We call from the owner address so onlyOwner passes.
func Dir2(
	ctx context.Context,
	client *ethclient.Client,
	cfg Config,
	pair, token common.Address,
	v3Fee int64,
	borrowAmt, minProfit *big.Int,
) (*Dir2Result, error) {
	arbABI, err := abi.JSON(strings.NewReader(contract.StableArbV2V3ABI))
	if err != nil {
		return nil, err
	}

	input, err := arbABI.Pack("flashArb", pair, token, big.NewInt(v3Fee), uint8(2), borrowAmt, minProfit)
	if err != nil {
		return nil, err
	}

	arbAddr := common.HexToAddress(cfg.ArbContract)
	ownerAddr := common.HexToAddress(cfg.Owner)

	msg := ethereum.CallMsg{
		From: ownerAddr,
		To:   &arbAddr,
		Data: input,
	}

	_, err = client.CallContract(ctx, msg, nil)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "execution reverted") || strings.Contains(errStr, "revert") {
			return &Dir2Result{Success: false, Error: errStr}, nil
		}
		return nil, err
	}

	return &Dir2Result{Success: true, Profit: minProfit}, nil
}

// Config for simulate package.
type Config struct {
	ArbContract string
	Owner       string
	USDT0       string
}
