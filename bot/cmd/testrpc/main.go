package main

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

var selGetReserves = []byte{0x09, 0x02, 0xf1, 0xac}

func main() {
	ctx := context.Background()
	client, err := ethclient.Dial("https://rpc.stable.xyz")
	if err != nil {
		fmt.Printf("dial: %v\n", err)
		return
	}
	defer client.Close()

	pair := common.HexToAddress("0x632F7449b7C615406CED74C8C9f1754c55f942AE")

	fmt.Println("Testing getReserves via Go client...")
	start := time.Now()

	raw, err := client.CallContract(ctx, ethereum.CallMsg{
		To:   &pair,
		Data: selGetReserves,
	}, nil)

	elapsed := time.Since(start)
	fmt.Printf("Elapsed: %s\n", elapsed)

	if err != nil {
		fmt.Printf("ERROR: %v\n", err)
		return
	}

	fmt.Printf("Raw bytes: %d\n", len(raw))
	fmt.Printf("raw hex: 0x%x\n", raw)

	if len(raw) >= 64 {
		r0 := new(big.Int).SetBytes(raw[0:32])
		r1 := new(big.Int).SetBytes(raw[32:64])
		fmt.Printf("Reserve0: %s\n", r0.String())
		fmt.Printf("Reserve1: %s\n", r1.String())
	}
}
