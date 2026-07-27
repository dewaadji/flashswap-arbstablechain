package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/flashswap/bot/internal/config"
	"github.com/flashswap/bot/internal/contract"
	"github.com/flashswap/bot/internal/discover"
	"github.com/flashswap/bot/internal/price"
	"github.com/flashswap/bot/internal/trader"
)

var (
	selGetReserves = []byte{0x09, 0x02, 0xf1, 0xac}
	selToken0      = []byte{0x0d, 0xfe, 0x16, 0x81}
)

type cachedPair struct {
	PairAddr        common.Address // V2 pair
	Token           common.Address // arb token (non-USDT0)
	TokenDecimals   int64          // token decimals
	IsToken0        bool           // true = token is V2 pair's token0
	PoolAddr        common.Address // V3 pool
	PoolTokenIsTok0 bool           // true = token is V3 pool's token0
	Fee             int64
	}

type Stats struct {
	Cycles       int
	TotalChecks  int
	Profitable   int
	BestProfit   *big.Int
	BestPair     string
	BestBorrow   *big.Int
	StartTime    time.Time
	TotalRPCErrs int
	}

func (s *Stats) Print(w io.Writer) {
	elapsed := time.Since(s.StartTime).Round(time.Second)
	fmt.Fprintf(w, "\n=== SESSION STATS ===\n")
	fmt.Fprintf(w, "Duration:       %s\n", elapsed)
	fmt.Fprintf(w, "Cycles:         %d\n", s.Cycles)
	fmt.Fprintf(w, "Total checks:   %d\n", s.TotalChecks)
	fmt.Fprintf(w, "Profitable:     %d\n", s.Profitable)
	fmt.Fprintf(w, "RPC errors:     %d\n", s.TotalRPCErrs)
	if s.BestProfit != nil && s.BestProfit.Sign() > 0 {
		fmt.Fprintf(w, "Best profit:    %s USDT0 (pair=%s, borrow=%s)\n",
			price.FormatUSD(s.BestProfit), s.BestPair, price.FormatUSD(s.BestBorrow))
	} else {
		fmt.Fprintf(w, "Best profit:    none found\n")
	}
	fmt.Fprintf(w, "======================\n")
	}

func main() {
	deployFlag := flag.Bool("deploy", false, "Deploy the arb contract")
	discoverFlag := flag.Bool("discover", false, "Discover V2 pairs and V3 pools")
	onceFlag := flag.Bool("once", false, "Run one arb check cycle")
	loopFlag := flag.Bool("loop", false, "Run arb loop continuously")
	durationMin := flag.Int("duration", 0, "Stop after N minutes (0 = run forever)")
	flag.Parse()

	cfg, err := config.Load(".env")
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx := context.Background()

	client, err := ethclient.Dial(cfg.RPCHTTP)
	if err != nil {
		log.Fatalf("rpc: %v", err)
	}
	defer client.Close()

	if *deployFlag {
		runDeploy(ctx, client, cfg)
		return
	}

	if *discoverFlag {
		runDiscover(ctx, client, cfg)
		return
	}

	// Trader is optional when dry-running
	var t *trader.Trader
	if cfg.PrivateKey != "" {
		pk, err := contract.HexToPrivateKey(cfg.PrivateKey)
		if err != nil {
			log.Fatalf("private key: %v", err)
		}
		t, err = trader.New(client, big.NewInt(cfg.ChainID), pk, common.HexToAddress(cfg.ArbContract), cfg.DryRun)
		if err != nil {
			log.Fatalf("trader: %v", err)
		}
	} else if !cfg.DryRun {
		log.Fatal("V3_PRIVATE_KEY required when V3_DRY_RUN=false")
	}

	fmt.Println("Discovering pairs & pools (one-time)...")
	cached := discoverCache(ctx, client, cfg)
	fmt.Printf("Cached %d liquid pairs ready for monitoring\n", len(cached))
	time.Sleep(1 * time.Second)

	if *onceFlag {
		runOnce(ctx, client, cfg, t, cached, nil)
		return
	}

	if *loopFlag {
		runLoop(ctx, client, cfg, t, cached, *durationMin)
		return
	}

	flag.Usage()
	}

func discoverCache(ctx context.Context, client *ethclient.Client, cfg *config.Config) []cachedPair {
	usdt0 := common.HexToAddress(cfg.USDT0)
	v2Factory := common.HexToAddress(cfg.V2Factory)
	v3Factory := common.HexToAddress(cfg.V3Factory)

	pairs, err := discover.V2Pairs(ctx, client, v2Factory, usdt0)
	if err != nil {
		log.Fatalf("v2 pairs: %v", err)
	}

	tokens := make([]common.Address, 0)
	for _, p := range pairs {
		tok, _ := price.NonUSDT0(p.Token0, p.Token1, usdt0)
		tokens = append(tokens, tok)
	}

	pools, err := discover.V3Pools(ctx, client, v3Factory, usdt0, tokens)
	if err != nil {
		log.Fatalf("v3 pools: %v", err)
	}

	poolsByToken := make(map[common.Address][]discover.PoolInfo)
	for _, p := range pools {
		poolsByToken[p.Token] = append(poolsByToken[p.Token], p)
	}

	cached := make([]cachedPair, 0)
	for _, pair := range pairs {
		token, isToken0 := price.NonUSDT0(pair.Token0, pair.Token1, usdt0)
		tokenPools, ok := poolsByToken[token]
		if !ok {
			continue
		}

		// Get token decimals once
		dec := discover.TokenDecimals(ctx, client, token)

		for _, pool := range tokenPools {
			t0Raw, _ := client.CallContract(ctx, ethereum.CallMsg{To: &pool.Address, Data: selToken0}, nil)
			poolTok0 := common.BytesToAddress(t0Raw)

			cached = append(cached, cachedPair{
				PairAddr:        pair.Address,
				Token:           token,
				TokenDecimals:   dec,
				IsToken0:        isToken0,
				PoolAddr:        pool.Address,
				PoolTokenIsTok0: poolTok0 == token,
				Fee:             pool.Fee,
			})
	}
	}
	return cached
	}

func runDeploy(ctx context.Context, client *ethclient.Client, cfg *config.Config) {
	pk, err := contract.HexToPrivateKey(cfg.PrivateKey)
	if err != nil {
		log.Fatalf("private key: %v", err)
	}

	binBytes, err := os.ReadFile("contract.bin")
	if err != nil {
		log.Fatalf("read contract.bin: %v", err)
	}

	hexStr := strings.TrimSpace(string(binBytes))
	hexStr = strings.TrimPrefix(hexStr, "0x")
	raw, err := hex.DecodeString(hexStr)
	if err != nil {
		log.Fatalf("decode bytecode: %v", err)
	}

	addr, err := contract.Deploy(
		ctx, client, big.NewInt(cfg.ChainID), pk, raw,
		common.HexToAddress(cfg.V2Router),
		common.HexToAddress(cfg.V3Router),
		common.HexToAddress(cfg.USDT0),
	)
	if err != nil {
		log.Fatalf("deploy: %v", err)
	}

	fmt.Printf("\nDEPLOYED at: %s\n", addr.Hex())
	fmt.Println("Set V3_ARB_CONTRACT=" + addr.Hex() + " in .env")
	}

func runDiscover(ctx context.Context, client *ethclient.Client, cfg *config.Config) {
	usdt0 := common.HexToAddress(cfg.USDT0)
	v2Factory := common.HexToAddress(cfg.V2Factory)
	v3Factory := common.HexToAddress(cfg.V3Factory)

	fmt.Println("Scanning V2 pairs...")
	pairs, err := discover.V2Pairs(ctx, client, v2Factory, usdt0)
	if err != nil {
		log.Fatalf("v2 pairs: %v", err)
	}
	fmt.Printf("Found %d V2 pairs with USDT0:\n", len(pairs))
	tokens := make([]common.Address, 0)
	for _, p := range pairs {
		tok, is0 := price.NonUSDT0(p.Token0, p.Token1, usdt0)
		tokens = append(tokens, tok)
		fmt.Printf("  %s  token=%s  (isToken0=%v)\n", p.Address.Hex(), tok.Hex(), is0)
	}

	fmt.Println("\nScanning V3 pools (liquid only)...")
	pools, err := discover.V3Pools(ctx, client, v3Factory, usdt0, tokens)
	if err != nil {
		log.Fatalf("v3 pools: %v", err)
	}
	fmt.Printf("Found %d V3 pools with liquidity:\n", len(pools))
	for _, p := range pools {
		fmt.Printf("  %s  token=%s  fee=%d\n", p.Address.Hex(), p.Token.Hex(), p.Fee)
	}
	}

func runOnce(ctx context.Context, client *ethclient.Client, cfg *config.Config, t *trader.Trader, cached []cachedPair, st *Stats) {
	for _, cp := range cached {
		// 1. Get V2 reserves
		raw, err := client.CallContract(ctx, ethereum.CallMsg{To: &cp.PairAddr, Data: selGetReserves}, nil)
		if err != nil {
			if st != nil {
				st.TotalRPCErrs++
			}
			continue
		}

		r0 := new(big.Int).SetBytes(raw[0:32])
		r1 := new(big.Int).SetBytes(raw[32:64])

		var resTok, resUSD *big.Int
		if cp.IsToken0 {
			resTok = r0
			resUSD = r1
		} else {
			resTok = r1
			resUSD = r0
		}

		if resTok.Sign() == 0 || resUSD.Sign() == 0 {
			continue
		}

		// 2. Get V3 pool state
		poolState, ok := price.FetchPoolState(ctx, client, cp.PoolAddr)
		if !ok {
			if st != nil {
				st.TotalRPCErrs++
			}
			continue
		}

		// 3. Dir 1: borrow 1% of token reserves
		borrowAmt := new(big.Int).Div(resTok, big.NewInt(100))
		if borrowAmt.Sign() == 0 {
			borrowAmt = big.NewInt(1e6)
		}

		// 4. Estimate V3 output: sell token → USDT0
		v3Out := price.EstimateV3Output(borrowAmt, poolState.SqrtPriceX96, cp.Fee, cp.PoolTokenIsTok0)
		v3OutSlipped := price.AddSlippage(v3Out, cfg.SlippageBPS)

		// 5. V2 repayment
		repay := price.V2AmountIn(borrowAmt, resUSD, resTok)
		profit := new(big.Int).Sub(v3OutSlipped, repay)

		if st != nil {
			st.TotalChecks++
			if profit.Cmp(cfg.MinProfit) >= 0 {
				st.Profitable++
			}
			if profit.Cmp(st.BestProfit) > 0 {
				st.BestProfit = new(big.Int).Set(profit)
				st.BestPair = short(cp.Token.Hex())
				st.BestBorrow = new(big.Int).Set(borrowAmt)
			}
		}

		// Only log pairs near profitable
		threshold := new(big.Int).Neg(big.NewInt(100000))
		if profit.Cmp(threshold) >= 0 {
			marker := "  "
			if profit.Cmp(cfg.MinProfit) >= 0 {
				marker = "**"
			}
			fmt.Printf("%s %-12s borrow=%-10s v3=%-10s repay=%-10s profit=%-10s [resTok=%s]\n",
				marker,
				short(cp.Token.Hex()),
				price.FormatToken(borrowAmt, cp.TokenDecimals),
				price.FormatUSD(v3Out),
				price.FormatUSD(repay),
				price.FormatUSD(profit),
				price.FormatToken(resTok, cp.TokenDecimals),
			)

			if profit.Cmp(cfg.MinProfit) >= 0 && !cfg.DryRun && t != nil {
				txHash, txErr := t.FlashArb(ctx, cp.PairAddr, cp.Token, cp.Fee, 1, borrowAmt, cfg.MinProfit)
				if txErr != nil {
					fmt.Printf("  tx error: %v\n", txErr)
				} else {
					fmt.Printf("  tx: %s\n", txHash)
				}
			}
		}
	}

	if st == nil {
		fmt.Printf("  Done checking %d pairs.\n", len(cached))
	}
	}

func runLoop(ctx context.Context, client *ethclient.Client, cfg *config.Config, t *trader.Trader, cached []cachedPair, durationMin int) {
	logFile, err := os.Create("arbitrage.log")
	if err != nil {
		log.Fatalf("create log: %v", err)
	}
	defer logFile.Close()

	multiW := io.MultiWriter(os.Stdout, logFile)

	st := &Stats{
		StartTime:  time.Now(),
		BestProfit: big.NewInt(-999999999999),
		BestBorrow: big.NewInt(0),
	}

	var deadline time.Time
	if durationMin > 0 {
		deadline = time.Now().Add(time.Duration(durationMin) * time.Minute)
	}

	interval := 5 * time.Second

	fmt.Fprintf(multiW, "=== ARB BOT STARTED ===\n")
	fmt.Fprintf(multiW, "Time:       %s\n", st.StartTime.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(multiW, "Dry run:    %v\n", cfg.DryRun)
	fmt.Fprintf(multiW, "MinProfit:  %s USDT0\n", price.FormatUSD(cfg.MinProfit))
	fmt.Fprintf(multiW, "Interval:   %s\n", interval)
	fmt.Fprintf(multiW, "Pairs:      %d monitored\n", len(cached))
	if durationMin > 0 {
		fmt.Fprintf(multiW, "Duration:   %d minutes\n", durationMin)
	} else {
		fmt.Fprintf(multiW, "Duration:   forever (Ctrl+C to stop)\n")
	}
	fmt.Fprintf(multiW, "Log file:   arbitrage.log\n")
	fmt.Fprintf(multiW, "==========================\n\n")

	for {
		if durationMin > 0 && time.Now().After(deadline) {
			fmt.Fprintf(multiW, "\nDuration reached. Stopping.\n")
			break
		}

		cycleStart := time.Now()
		ts := cycleStart.Format("15:04:05")
		fmt.Fprintf(multiW, "--- %s [cycle %d] ---\n", ts, st.Cycles+1)

		runOnce(ctx, client, cfg, t, cached, st)
		st.Cycles++

		elapsed := time.Since(cycleStart)
		fmt.Fprintf(multiW, "--- cycle done in %s | profitable: %d/%d | best: %s ---\n\n",
			elapsed.Round(time.Millisecond), st.Profitable, st.TotalChecks, price.FormatUSD(st.BestProfit))

		sleepLeft := interval - elapsed
		if sleepLeft < 0 {
			sleepLeft = 0
		}
		select {
		case <-ctx.Done():
			fmt.Fprintf(multiW, "\nInterrupted. Saving stats...\n")
			st.Print(multiW)
			return
		case <-time.After(sleepLeft):
		}
	}

	st.Print(multiW)
	}

func short(s string) string {
	if len(s) > 12 {
		return s[:12] + "..."
	}
	return s
	}
