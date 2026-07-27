package contract

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

func Deploy(
	ctx context.Context,
	client *ethclient.Client,
	chainID *big.Int,
	pk *ecdsa.PrivateKey,
	creationBytecode []byte,
	v2Router, v3Router, usdt0 common.Address,
) (common.Address, error) {
	from := crypto.PubkeyToAddress(pk.PublicKey)

	// ABI-encode constructor args: (address, address, address)
	// Each address is 32 bytes (left-padded)
	args := make([]byte, 96)
	copy(args[12:32], v2Router.Bytes())
	copy(args[44:64], v3Router.Bytes())
	copy(args[76:96], usdt0.Bytes())

	data := make([]byte, 0, len(creationBytecode)+len(args))
	data = append(data, creationBytecode...)
	data = append(data, args...)

	nonce, err := client.PendingNonceAt(ctx, from)
	if err != nil {
		return common.Address{}, fmt.Errorf("nonce: %w", err)
	}

	gasPrice, err := client.SuggestGasPrice(ctx)
	if err != nil {
		return common.Address{}, fmt.Errorf("gasprice: %w", err)
	}

	tx := types.NewTx(&types.LegacyTx{
		Nonce:    nonce,
		GasPrice: gasPrice,
		Gas:      3000000,
		To:       nil, // contract creation
		Data:     data,
	})

	signedTx, err := types.SignTx(tx, types.NewEIP155Signer(chainID), pk)
	if err != nil {
		return common.Address{}, fmt.Errorf("sign: %w", err)
	}

	err = client.SendTransaction(ctx, signedTx)
	if err != nil {
		return common.Address{}, fmt.Errorf("send: %w", err)
	}

	fmt.Printf("deploy tx sent: %s\n", signedTx.Hash().Hex())

	// Compute predicted address
	addr := crypto.CreateAddress(from, nonce)
	fmt.Printf("predicted address: %s\n", addr.Hex())

	return addr, nil
}

func HexToPrivateKey(hexKey string) (*ecdsa.PrivateKey, error) {
	hexKey = strip0x(hexKey)
	b, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("decode privkey: %w", err)
	}
	return crypto.ToECDSAUnsafe(b), nil
}

func strip0x(s string) string {
	if len(s) >= 2 && s[0:2] == "0x" {
		return s[2:]
	}
	return s
}
