package trader

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/flashswap/bot/internal/contract"
)

var ErrSimRevert = errors.New("on-chain simulation reverted")

type Trader struct {
	client      *ethclient.Client
	chainID     *big.Int
	pk          *ecdsa.PrivateKey
	from        common.Address
	arbAddr     common.Address
	dryRun      bool
	maxGasPrice *big.Int
	arbABI      *abi.ABI
}

func New(client *ethclient.Client, chainID *big.Int, pk *ecdsa.PrivateKey, arbAddr common.Address, dryRun bool, maxGasPrice *big.Int) (*Trader, error) {
	arbABI, err := abi.JSON(strings.NewReader(contract.StableArbV2V3ABI))
	if err != nil {
		return nil, err
	}
	return &Trader{
		client:      client,
		chainID:     chainID,
		pk:          pk,
		from:        crypto.PubkeyToAddress(pk.PublicKey),
		arbAddr:     arbAddr,
		dryRun:      dryRun,
		maxGasPrice: maxGasPrice,
		arbABI:      &arbABI,
	}, nil
}

func (t *Trader) FlashArb(
	ctx context.Context,
	pair, token common.Address,
	v3Fee int64,
	dir uint8,
	borrowAmt, minProfit *big.Int,
) (string, error) {
	input, err := t.arbABI.Pack("flashArb", pair, token, big.NewInt(v3Fee), dir, borrowAmt, minProfit)
	if err != nil {
		return "", fmt.Errorf("pack: %w", err)
	}

	ok, simErr := t.simCall(ctx, input, big.NewInt(0))
	if simErr != nil {
		return "", fmt.Errorf("sim: %w", simErr)
	}
	if !ok {
		return "", ErrSimRevert
	}

	return t.sendTx(ctx, input, big.NewInt(0))
}

func (t *Trader) simCall(ctx context.Context, data []byte, value *big.Int) (bool, error) {
	msg := ethereum.CallMsg{
		From:  t.from,
		To:    &t.arbAddr,
		Value: value,
		Data:  data,
	}
	_, err := t.client.CallContract(ctx, msg, nil)
	if err != nil {
		if strings.Contains(err.Error(), "execution reverted") || strings.Contains(err.Error(), "revert") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (t *Trader) ExecuteArb(
	ctx context.Context,
	token common.Address,
	v3Fee int64,
	dir uint8,
	minProfit *big.Int,
	deadline *big.Int,
	value *big.Int,
) (string, error) {
	input, err := t.arbABI.Pack("executeArb", token, big.NewInt(v3Fee), dir, minProfit, deadline)
	if err != nil {
		return "", fmt.Errorf("pack: %w", err)
	}

	ok, simErr := t.simCall(ctx, input, value)
	if simErr != nil {
		return "", fmt.Errorf("sim: %w", simErr)
	}
	if !ok {
		return "", ErrSimRevert
	}

	return t.sendTx(ctx, input, value)
}

func (t *Trader) sendTx(ctx context.Context, data []byte, value *big.Int) (string, error) {
	if t.dryRun {
		fmt.Println("[DRY RUN] would send tx:")
		fmt.Printf("  from:  %s\n", t.from.Hex())
		fmt.Printf("  to:    %s\n", t.arbAddr.Hex())
		fmt.Printf("  value: %s\n", value.String())
		fmt.Printf("  data:  0x%x\n", data)
		return "dry-run", nil
	}

	nonce, err := t.client.PendingNonceAt(ctx, t.from)
	if err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}

	gasPrice, err := t.client.SuggestGasPrice(ctx)
	if err != nil {
		return "", fmt.Errorf("gasprice: %w", err)
	}

	if t.maxGasPrice != nil && t.maxGasPrice.Sign() > 0 && gasPrice.Cmp(t.maxGasPrice) > 0 {
		return "", fmt.Errorf("gas price %s exceeds max %s", gasPrice.String(), t.maxGasPrice.String())
	}

	msg := ethereum.CallMsg{
		From:  t.from,
		To:    &t.arbAddr,
		Value: value,
		Data:  data,
	}

	gasLimit, err := t.client.EstimateGas(ctx, msg)
	if err != nil {
		return "", fmt.Errorf("estimate gas: %w", err)
	}

	// 30% buffer
	gasLimit = gasLimit * 13 / 10

	tx := types.NewTx(&types.LegacyTx{
		Nonce:    nonce,
		GasPrice: gasPrice,
		Gas:      gasLimit,
		To:       &t.arbAddr,
		Value:    value,
		Data:     data,
	})

	signedTx, err := types.SignTx(tx, types.NewEIP155Signer(t.chainID), t.pk)
	if err != nil {
		return "", fmt.Errorf("sign: %w", err)
	}

	err = t.client.SendTransaction(ctx, signedTx)
	if err != nil {
		return "", fmt.Errorf("send: %w", err)
	}

	txHash := signedTx.Hash().Hex()
	fmt.Printf("[TX SENT] %s (gas=%d, price=%s)\n", txHash, gasLimit, gasPrice.String())

	// Wait for receipt with 120s timeout.
	waitCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	receipt, err := bind.WaitMined(waitCtx, t.client, signedTx)
	if err != nil {
		return txHash, fmt.Errorf("wait mined: %w", err)
	}
	if receipt.Status == 0 {
		return txHash, fmt.Errorf("tx reverted on-chain (gas used=%d)", receipt.GasUsed)
	}

	fmt.Printf("[TX CONFIRMED] %s (block=%d, gas=%d)\n", txHash, receipt.BlockNumber, receipt.GasUsed)
	return txHash, nil
}
