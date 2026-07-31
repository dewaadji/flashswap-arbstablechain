package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
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
	Token           common.Address // arb token (non-stable)
	StableToken     common.Address // USDT0 or WgUSDT
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
	testFireFlag := flag.Bool("testfire", false, "Force one tiny trade to verify tx pipeline")
	verboseFlag := flag.Bool("verbose", false, "Log all pairs (not just near-profitable ones)")
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
		t, err = trader.New(client, big.NewInt(cfg.ChainID), pk, common.HexToAddress(cfg.ArbContract), cfg.DryRun, cfg.MaxGasPrice)
		if err != nil {
			log.Fatalf("trader: %v", err)
		}
	} else if !cfg.DryRun {
		log.Fatal("V3_PRIVATE_KEY required when V3_DRY_RUN=false")
	}

	fmt.Println("Discovering pairs & pools...")
	cached := loadPairCache("paircache.json")
	if cached != nil {
		fmt.Printf("Loaded %d pairs from cache\n", len(cached))
	} else {
		cached = discoverCache(ctx, client, cfg)
		savePairCache("paircache.json", cached)
	}
	fmt.Printf("Cached %d liquid pairs ready for monitoring\n", len(cached))
	time.Sleep(1 * time.Second)

	if *onceFlag {
		runOnce(ctx, client, cfg, t, cached, nil, *verboseFlag)
		return
	}

	if *loopFlag {
		runLoop(ctx, client, cfg, t, cached, *durationMin, *verboseFlag)
		return
	}

	if *testFireFlag {
		runTestFire(ctx, client, cfg, t, cached)
		return
	}

	flag.Usage()
}

func discoverCache(ctx context.Context, client *ethclient.Client, cfg *config.Config) []cachedPair {
	usdt0 := common.HexToAddress(cfg.USDT0)
	wgusdt := common.HexToAddress(cfg.WgUSDT)
	v2Factory := common.HexToAddress(cfg.V2Factory)
	v2Factory2 := common.HexToAddress(cfg.V2Factory2)
	v3Factory := common.HexToAddress(cfg.V3Factory)

	factories := []struct {
		addr  common.Address
		label string
	}{{v2Factory, "canonical"}}
	if v2Factory2 != (common.Address{}) {
		factories = append(factories, struct {
			addr  common.Address
			label string
		}{v2Factory2, "DYOR"})
	}

	// Phase 1: full scan of canonical factory (fast — few pairs)
	start := time.Now()
	canonPairs, err := discover.V2Pairs(ctx, client, v2Factory, usdt0, wgusdt)
	if err != nil {
		log.Fatalf("v2 pairs [canonical]: %v", err)
	}
	fmt.Printf("  [canonical] %d pairs (%s)\n", len(canonPairs), time.Since(start).Round(time.Millisecond))

	allPairs := make([]discover.PairInfo, 0, len(canonPairs)+100)
	allPairs = append(allPairs, canonPairs...)

	// Collect unique tokens
	seenTok := make(map[common.Address]bool)
	for _, p := range allPairs {
		tok := arbToken(p.Token0, p.Token1, p.StableToken)
		seenTok[tok] = true
	}

	// Phase 2: query DYOR factory via getPair for each known token (fast — O(tokens))
	if v2Factory2 != (common.Address{}) {
		tokens := make([]common.Address, 0, len(seenTok))
		for tok := range seenTok {
			tokens = append(tokens, tok)
		}
		start2 := time.Now()
		dyorPairs, err := discover.V2PairsForTokens(ctx, client, v2Factory2, usdt0, wgusdt, tokens)
		if err != nil {
			log.Fatalf("v2 pairs [DYOR]: %v", err)
		}
		fmt.Printf("  [DYOR]     %d pairs (%s)\n", len(dyorPairs), time.Since(start2).Round(time.Millisecond))
		for _, p := range dyorPairs {
			tok := arbToken(p.Token0, p.Token1, p.StableToken)
			seenTok[tok] = true
		}
		allPairs = append(allPairs, dyorPairs...)
	}

	tokens := make([]common.Address, 0, len(seenTok))
	for tok := range seenTok {
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

	decCache := make(map[common.Address]int64)
	poolT0Cache := make(map[common.Address]common.Address)

	cached := make([]cachedPair, 0)
	for _, pair := range allPairs {
		token, isToken0 := arbTokenIs0(pair.Token0, pair.Token1, pair.StableToken)
		tokenPools, ok := poolsByToken[token]
		if !ok {
			continue
		}

		dec, ok := decCache[token]
		if !ok {
			dec = discover.TokenDecimals(ctx, client, token)
			decCache[token] = dec
		}

		for _, pool := range tokenPools {
			poolTok0, ok := poolT0Cache[pool.Address]
			if !ok {
				t0Raw, _ := client.CallContract(ctx, ethereum.CallMsg{To: &pool.Address, Data: selToken0}, nil)
				poolTok0 = common.BytesToAddress(t0Raw)
				poolT0Cache[pool.Address] = poolTok0
			}

			cached = append(cached, cachedPair{
				PairAddr:        pair.Address,
				Token:           token,
				StableToken:     pair.StableToken,
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
		common.HexToAddress(cfg.WgUSDT),
	)
	if err != nil {
		log.Fatalf("deploy: %v", err)
	}

	fmt.Printf("\nDEPLOYED at: %s\n", addr.Hex())
	fmt.Println("Set V3_ARB_CONTRACT=" + addr.Hex() + " in .env")
}

func runDiscover(ctx context.Context, client *ethclient.Client, cfg *config.Config) {
	usdt0 := common.HexToAddress(cfg.USDT0)
	wgusdt := common.HexToAddress(cfg.WgUSDT)
	v2Factory := common.HexToAddress(cfg.V2Factory)
	v2Factory2 := common.HexToAddress(cfg.V2Factory2)
	v3Factory := common.HexToAddress(cfg.V3Factory)

	factories := []struct {
		addr  common.Address
		label string
	}{{v2Factory, "canonical"}}
	if v2Factory2 != (common.Address{}) {
		factories = append(factories, struct {
			addr  common.Address
			label string
		}{v2Factory2, "DYOR"})
	}

	fmt.Println("Scanning V2 pairs [canonical]...")
	canonPairs, err := discover.V2Pairs(ctx, client, v2Factory, usdt0, wgusdt)
	if err != nil {
		log.Fatalf("v2 pairs [canonical]: %v", err)
	}
	fmt.Printf("  Found %d pairs with USDT0 or WgUSDT\n", len(canonPairs))
	allPairs := make([]discover.PairInfo, 0, len(canonPairs)+100)
	allPairs = append(allPairs, canonPairs...)

	seenTok := make(map[common.Address]bool)
	for _, p := range allPairs {
		tok := arbToken(p.Token0, p.Token1, p.StableToken)
		seenTok[tok] = true
	}

	if v2Factory2 != (common.Address{}) {
		tokens := make([]common.Address, 0, len(seenTok))
		for tok := range seenTok {
			tokens = append(tokens, tok)
		}
		fmt.Printf("Scanning V2 pairs [DYOR] via getPair (%d tokens)...\n", len(tokens))
		dyorPairs, err := discover.V2PairsForTokens(ctx, client, v2Factory2, usdt0, wgusdt, tokens)
		if err != nil {
			log.Fatalf("v2 pairs [DYOR]: %v", err)
		}
		fmt.Printf("  Found %d additional pairs\n", len(dyorPairs))
		for _, p := range dyorPairs {
			tok, is0 := arbTokenIs0(p.Token0, p.Token1, p.StableToken)
			stableLabel := "USDT0"
			if p.StableToken == wgusdt {
				stableLabel = "WgUSDT"
			}
			fmt.Printf("    %s  token=%s  isToken0=%v  stable=%s\n",
				p.Address.Hex(), tok.Hex(), is0, stableLabel)
		}
		allPairs = append(allPairs, dyorPairs...)
		for _, p := range dyorPairs {
			tok := arbToken(p.Token0, p.Token1, p.StableToken)
			seenTok[tok] = true
		}
	}

	for _, p := range canonPairs {
		tok, is0 := arbTokenIs0(p.Token0, p.Token1, p.StableToken)
		stableLabel := "USDT0"
		if p.StableToken == wgusdt {
			stableLabel = "WgUSDT"
		}
		fmt.Printf("    %s  token=%s  isToken0=%v  stable=%s\n",
			p.Address.Hex(), tok.Hex(), is0, stableLabel)
	}

	tokens := make([]common.Address, 0, len(seenTok))
	for tok := range seenTok {
		tokens = append(tokens, tok)
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

func runOnce(ctx context.Context, client *ethclient.Client, cfg *config.Config, t *trader.Trader, cached []cachedPair, st *Stats, verbose bool) {
	usdt0Addr := common.HexToAddress(cfg.USDT0)
	quoterAddr := common.HexToAddress(cfg.V3Quoter)
	quoterFailed := false

	// Fetch gas price once per cycle for profitability calculation.
	gasPrice, _ := client.SuggestGasPrice(ctx)
	// Conservative estimate: 350k gas for a flash-swap tx.
	// Gas cost = gasPrice * 350000, converted from 18-dec native to 6-dec USDT0.
	gasCostUSD := big.NewInt(0)
	if gasPrice != nil && gasPrice.Sign() > 0 {
		gasCostUSD = new(big.Int).Div(
			new(big.Int).Mul(gasPrice, big.NewInt(350000)),
			price.Ten12,
		)
	}

	// Track the best pair this cycle for the sample log line.
	var bestCP cachedPair
	bestProfit := new(big.Int).SetInt64(-1e18) // sentinel: impossibly bad
	bestDir := 0
	bestBorrow := big.NewInt(0)

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

		// 3. Dir 1: borrow 0.01% of token reserves
		borrowAmt := new(big.Int).Div(resTok, big.NewInt(10000))
		if borrowAmt.Sign() == 0 {
			borrowAmt = big.NewInt(1e6)
		}

		// 4. Quote V3 output: sell token → USDT0 (Quoter first, spot fallback)
		v3Out, err := price.QuoteV3(ctx, client, quoterAddr, cp.Token, usdt0Addr, cp.Fee, borrowAmt)
		if err != nil {
			if !quoterFailed {
				fmt.Printf("  [WARN] Quoter unavailable, using spot estimate: %v\n", err)
				quoterFailed = true
			}
			v3Out = price.EstimateV3Output(borrowAmt, poolState.SqrtPriceX96, cp.Fee, cp.PoolTokenIsTok0)
		}
		v3OutSlipped := price.AddSlippage(v3Out, cfg.SlippageBPS)

		// 5. V2 repayment
		repayNative := price.V2AmountIn(borrowAmt, resUSD, resTok)
		var repayUSD, profit *big.Int
		if cp.StableToken == common.HexToAddress(cfg.WgUSDT) {
			repayUSD = price.WgUSDTToUSDT0(repayNative)
		} else {
			repayUSD = repayNative
		}
		profit = new(big.Int).Sub(v3OutSlipped, repayUSD)
			profit.Sub(profit, gasCostUSD)

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

		// Track best pair this cycle (regardless of threshold)
		if profit.Cmp(bestProfit) > 0 {
			bestProfit.Set(profit)
			bestCP = cp
			bestDir = 1
			bestBorrow.Set(borrowAmt)
		}

		// Only log pairs near profitable
		threshold := new(big.Int).Neg(big.NewInt(100000))
		if profit.Cmp(threshold) >= 0 {
			marker := "  "
			if profit.Cmp(cfg.MinProfit) >= 0 {
				marker = "**"
			}
			fmt.Printf("%s %-12s borrow=%-10s v3=%-10s repay=%-10s profit=%-10s [resTok=%s] D1\n",
				marker,
				short(cp.Token.Hex()),
				price.FormatToken(borrowAmt, cp.TokenDecimals),
				price.FormatUSD(v3Out),
				price.FormatUSD(repayUSD),
				price.FormatUSD(profit),
				price.FormatToken(resTok, cp.TokenDecimals),
			)

			if profit.Cmp(cfg.MinProfit) >= 0 && !cfg.DryRun && t != nil {
				txHash, txErr := t.FlashArb(ctx, cp.PairAddr, cp.Token, cp.Fee, 1, borrowAmt, cfg.MinProfit)
				if txErr != nil {
					if txErr == trader.ErrSimRevert {
						fmt.Printf("  sim reverted: %s D1\n", short(cp.Token.Hex()))
					} else {
						fmt.Printf("  tx error: %v\n", txErr)
					}
				} else {
					fmt.Printf("  tx: %s\n", txHash)
				}
			}
		}

		// Dir 2: borrow stable, buy token on V3, repay token, sell leftover.
		// Skip WgUSDT pairs — contract has a known bug with Dir 2 on WgUSDT.
		if cp.StableToken == usdt0Addr {
			borrowStable := new(big.Int).Div(resUSD, big.NewInt(10000))
			if borrowStable.Sign() == 0 {
				borrowStable = big.NewInt(1e6)
			}

			tokenBought, err := price.QuoteV3(ctx, client, quoterAddr, usdt0Addr, cp.Token, cp.Fee, borrowStable)
			if err != nil {
				if !quoterFailed {
					fmt.Printf("  [WARN] Quoter unavailable, using spot estimate: %v\n", err)
					quoterFailed = true
				}
				tokenBought = price.EstimateV3Buy(borrowStable, poolState.SqrtPriceX96, cp.Fee, cp.PoolTokenIsTok0)
			}
			repayToken := price.V2AmountIn(borrowStable, resTok, resUSD)

			if tokenBought.Cmp(repayToken) > 0 {
				leftover := new(big.Int).Sub(tokenBought, repayToken)
				leftoverUSD, err := price.QuoteV3(ctx, client, quoterAddr, cp.Token, usdt0Addr, cp.Fee, leftover)
				if err != nil {
					if !quoterFailed {
						fmt.Printf("  [WARN] Quoter unavailable, using spot estimate: %v\n", err)
						quoterFailed = true
					}
					leftoverUSD = price.EstimateV3Output(leftover, poolState.SqrtPriceX96, cp.Fee, cp.PoolTokenIsTok0)
				}
				profit2 := price.AddSlippage(leftoverUSD, cfg.SlippageBPS)
					profit2.Sub(profit2, gasCostUSD)

				if st != nil {
					st.TotalChecks++
					if profit2.Cmp(cfg.MinProfit) >= 0 {
						st.Profitable++
					}
					if profit2.Cmp(st.BestProfit) > 0 {
						st.BestProfit = new(big.Int).Set(profit2)
						st.BestPair = short(cp.Token.Hex())
						st.BestBorrow = new(big.Int).Set(borrowStable)
					}
				}

				// Track best pair this cycle
				if profit2.Cmp(bestProfit) > 0 {
					bestProfit.Set(profit2)
					bestCP = cp
					bestDir = 2
					bestBorrow.Set(borrowStable)
				}

				if profit2.Cmp(threshold) >= 0 {
					marker2 := "  "
					if profit2.Cmp(cfg.MinProfit) >= 0 {
						marker2 = "**"
					}
					fmt.Printf("%s %-12s borrow=%-10s buy=%-10s repay=%-10s leftover=%-10s profit=%-10s [resUSD=%s] D2\n",
						marker2,
						short(cp.Token.Hex()),
						price.FormatUSD(borrowStable),
						price.FormatToken(tokenBought, cp.TokenDecimals),
						price.FormatToken(repayToken, cp.TokenDecimals),
						price.FormatUSD(leftoverUSD),
						price.FormatUSD(profit2),
						price.FormatUSD(resUSD),
					)

					if profit2.Cmp(cfg.MinProfit) >= 0 && !cfg.DryRun && t != nil {
						txHash, txErr := t.FlashArb(ctx, cp.PairAddr, cp.Token, cp.Fee, 2, borrowStable, cfg.MinProfit)
						if txErr != nil {
							if txErr == trader.ErrSimRevert {
								fmt.Printf("  sim reverted: %s D2\n", short(cp.Token.Hex()))
							} else {
								fmt.Printf("  tx error (D2): %v\n", txErr)
							}
						} else {
							fmt.Printf("  tx (D2): %s\n", txHash)
						}
					}
				}
			}
		}
	}

	if st == nil {
		fmt.Printf("  Done checking %d pairs.\n", len(cached))
	}

	// Always log best (least-worst) pair as a sample
	if st != nil && bestDir > 0 {
		dirLabel := fmt.Sprintf("D%d", bestDir)
		fmt.Printf("  sample: %s %s profit=%s borrow=%s\n",
			dirLabel,
			short(bestCP.Token.Hex()),
			price.FormatUSD(bestProfit),
			price.FormatUSD(bestBorrow),
		)
	}
}

func runLoop(ctx context.Context, client *ethclient.Client, cfg *config.Config, t *trader.Trader, cached []cachedPair, durationMin int, verbose bool) {
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

		runOnce(ctx, client, cfg, t, cached, st, verbose)
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

// arbToken returns the non-stable token from a V2 pair.
func arbToken(t0, t1, stable common.Address) common.Address {
	if t0 == stable {
		return t1
	}
	return t0
}

// arbTokenIs0 returns the non-stable token and whether it is token0.
func arbTokenIs0(t0, t1, stable common.Address) (common.Address, bool) {
	if t0 == stable {
		return t1, false
	}
	return t0, true
}

func savePairCache(path string, pairs []cachedPair) {
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	json.NewEncoder(f).Encode(pairs)
}

func loadPairCache(path string) []cachedPair {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var pairs []cachedPair
	if err := json.NewDecoder(f).Decode(&pairs); err != nil {
		return nil
	}
	return pairs
}

func runTestFire(ctx context.Context, client *ethclient.Client, cfg *config.Config, t *trader.Trader, cached []cachedPair) {
	if cfg.DryRun {
		log.Fatal("testfire requires V3_DRY_RUN=false")
	}
	if t == nil {
		log.Fatal("testfire requires a configured private key")
	}

	// Sort: prefer higher-fee pools (3000 > 500 > 100 → more liquid).
	sorted := make([]cachedPair, len(cached))
	copy(sorted, cached)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].Fee > sorted[i].Fee {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	wgusdtAddr := common.HexToAddress(cfg.WgUSDT)
	isWgUSDT := func(cp cachedPair) bool { return cp.StableToken == wgusdtAddr }

	// executeArb: own-capital fallback. 1e14 wei → spend ≈ 0.0001 USDT0.
	execValue := big.NewInt(1_00_000_000_000_000) // 1e14 wei
	deadline := big.NewInt(time.Now().Unix() + 300)
	zero := big.NewInt(0)

	for i, cp := range sorted {
		raw, err := client.CallContract(ctx, ethereum.CallMsg{To: &cp.PairAddr, Data: selGetReserves}, nil)
		if err != nil {
			continue
		}
		r0 := new(big.Int).SetBytes(raw[0:32])
		r1 := new(big.Int).SetBytes(raw[32:64])
		var resTok, resStable *big.Int
		if cp.IsToken0 {
			resTok = r0
			resStable = r1
		} else {
			resTok = r1
			resStable = r0
		}
		if resTok.Sign() == 0 || resStable.Sign() == 0 {
			continue
		}

		borrowAmt := new(big.Int).Div(resTok, big.NewInt(1000000)) // 0.0001%
		if borrowAmt.Sign() == 0 {
			borrowAmt = big.NewInt(10)
		}
		borrowStable := new(big.Int).Div(resStable, big.NewInt(1000000))
		if borrowStable.Sign() == 0 {
			borrowStable = big.NewInt(10)
		}

		stableLabel := "USDT0"
		if isWgUSDT(cp) {
			stableLabel = "WgUSDT"
		}

		fmt.Printf("\n=== TESTFIRE attempt %d/%d ===\n", i+1, len(sorted))
		fmt.Printf("Pair:      %s\n", cp.PairAddr.Hex())
		fmt.Printf("Token:     %s (decimals=%d)\n", cp.Token.Hex(), cp.TokenDecimals)
		fmt.Printf("Stable:    %s (%s)\n", cp.StableToken.Hex(), stableLabel)
		fmt.Printf("V3 Pool:   %s (fee=%d)\n", cp.PoolAddr.Hex(), cp.Fee)

		// 1. flashArb dir=1: borrow token, repay stable
		fmt.Printf("  [1/4] flashArb dir=1 borrow=%s...", price.FormatToken(borrowAmt, cp.TokenDecimals))
		txHash, err := t.FlashArb(ctx, cp.PairAddr, cp.Token, cp.Fee, 1, borrowAmt, zero)
		if err == nil {
			fmt.Printf("\nSUCCESS — TX SENT: %s\n", txHash)
			fmt.Printf("Check: https://stablescan.xyz/tx/%s\n", txHash)
			return
		}
		fmt.Printf(" %v\n", err)

		// 2. flashArb dir=2: borrow stable, repay token (skip WgUSDT — known bug)
		if !isWgUSDT(cp) {
			fmt.Printf("  [2/4] flashArb dir=2 borrow=%s...", price.FormatUSD(borrowStable))
			txHash, err = t.FlashArb(ctx, cp.PairAddr, cp.Token, cp.Fee, 2, borrowStable, zero)
			if err == nil {
				fmt.Printf("\nSUCCESS — TX SENT: %s\n", txHash)
				fmt.Printf("Check: https://stablescan.xyz/tx/%s\n", txHash)
				return
			}
			fmt.Printf(" %v\n", err)
		} else {
			fmt.Printf("  [2/4] flashArb dir=2 skipped (WgUSDT)\n")
		}

		// 3. executeArb dir=1: own capital → buy V2, sell V3
		fmt.Printf("  [3/4] executeArb dir=1 value=%s...", price.FormatUSD(execValue))
		txHash, err = t.ExecuteArb(ctx, cp.Token, cp.Fee, 1, zero, deadline, execValue)
		if err == nil {
			fmt.Printf("\nSUCCESS — TX SENT: %s\n", txHash)
			fmt.Printf("Check: https://stablescan.xyz/tx/%s\n", txHash)
			return
		}
		fmt.Printf(" %v\n", err)

		// 4. executeArb dir=2: own capital → buy V3, sell V2 (skip WgUSDT)
		if !isWgUSDT(cp) {
			fmt.Printf("  [4/4] executeArb dir=2 value=%s...", price.FormatUSD(execValue))
			txHash, err = t.ExecuteArb(ctx, cp.Token, cp.Fee, 2, zero, deadline, execValue)
			if err == nil {
				fmt.Printf("\nSUCCESS — TX SENT: %s\n", txHash)
				fmt.Printf("Check: https://stablescan.xyz/tx/%s\n", txHash)
				return
			}
			fmt.Printf(" %v\n", err)
		} else {
			fmt.Printf("  [4/4] executeArb dir=2 skipped (WgUSDT)\n")
		}

		fmt.Printf("  → all 4 paths failed for this pair\n")
	}

	log.Fatal("all pairs failed — none could execute")
}
