# bot/ — Go Arbitrage Bot

Off-chain bot that discovers and executes V2→V3 flash-swap arbitrage on Stable Chain (chain ID 988).

## Build & Test

```bash
go test ./...                       # run all tests
go build -o stablearb ./cmd/stablearb  # build binary
make bytecode                       # extract bytecode from ../arb/ Forge output
make build                          # go build shortcut
make deploy                         # deploy contract via -deploy flag
```

## Entrypoints

All in `cmd/stablearb/main.go`:

| Flag | Behavior |
|---|---|
| `-deploy` | Deploy `contract.bin` on-chain, print address |
| `-discover` | Scan all factories, print pairs/pools found |
| `-once` | One arb check cycle across all cached pairs |
| `-loop` | Continuous loop (5s interval), logs to `arbitrage.log` |
| `-testfire` | Force one minimum-size trade (Dir 1, 0.0001% borrow) |
| `-duration N` | Stop after N minutes (used with `-loop`) |

## Architecture

```
main.go (discovery, caching, loop, profit calc)
  └── internal/
      ├── config/     — Load .env → Config struct
      ├── contract/   — Hardcoded ABIs (abi.go) + deploy helper (deploy.go)
      ├── discover/   — On-chain pair/pool scanning (2 V2 factories + V3 factory)
      ├── price/      — Pure math: V2/V3 formulas, conversions, formatting (no RPC)
      ├── simulate/   — Off-chain eth_call for Dir 2 simulation
      └── trader/     — ABI pack → eth_call sim → send LegacyTx
```

### Discovery flow (`discoverCache`)

1. Full scan canonical V2 factory (`V2Factory`) via `V2Pairs()` — iterates `allPairsLength()`
2. Targeted scan DYOR factory (`V2Factory2`, optional) via `V2PairsForTokens()` — O(tokens) using `getPair()`
3. For each unique non-stable token, query V3 factory (`V3Factory`) for pools at 4 fee tiers (100, 500, 3000, 10000), skip zero-liquidity pools
4. Build `cachedPair` list joining V2 pair ↔ V3 pool, persist to `paircache.json`

### Arb cycle (`runOnce`)

For each cached pair, check two directions:

**Dir 1** — borrow 1% of V2 token reserves → sell on V3 → repay V2 with stable:
- If stable = WgUSDT: convert repayment via `WgUSDTToUSDT0()`
- `profit = v3OutputSlipped - repayUSD - gasCost`

**Dir 2** — borrow 1% of V2 stable reserves → buy token on V3 → repay V2 with token → sell leftover:
- **Skipped for WgUSDT pairs** (known contract bug)
- `profit = leftoverUSDSlipped - gasCost`

### Transaction pipeline (`trader`)

1. ABI-pack `flashArb` call
2. Pre-flight `eth_call` simulation — returns `ErrSimRevert` sentinel on revert (silently skipped)
3. If simulation passes: fetch nonce, gas price (reject if > `MaxGasPrice`), estimate gas (+30% buffer), sign EIP155 `LegacyTx`, send, wait for receipt (120s timeout)

## Key design decisions

- **Spot-price V3 estimation**: `EstimateV3Output`/`EstimateV3Buy` use `sqrtPriceX96` spot price, not tick simulation. Fast (one RPC call) but approximate for large swaps.
- **Two stable tokens**: USDT0 (6 dec, native) and WgUSDT (18 dec, wrapped). Both can be the stable side of a V2 pair.
- **1% borrow ratio**: Fixed — borrows 1% of V2 reserves per check. `testfire` uses 0.0001% for safety.
- **Dry run by default**: `V3_DRY_RUN=true` — prints what it would do, never sends real transactions.
- **Gas cost**: Hardcoded 350k gas units × gasPrice, converted 18→6 decimals for profit calculation.
- **No MEV protection**: Public mempool, no flashbots/private relay.
- **Cache invalidation**: `paircache.json` only refreshed when file deleted or `-discover` flag used. No auto-refresh.

## Config (.env)

Required: `V3_RPC_HTTP`, `V3_V2_ROUTER`, `V3_V2_FACTORY`, `V3_V3_ROUTER`, `V3_V3_FACTORY`, `V3_V3_QUOTER`, `V3_USDT0`.

Optional with defaults: `V3_CHAIN_ID` (988), `V3_DRY_RUN` (true), `V3_MIN_PROFIT` (100000 = $0.10), `V3_MAX_GAS_PRICE` (5 gwei), `V3_SLIPPAGE_BPS` (50 = 0.50%), `V3_V2_FACTORY2` (DYOR), `V3_WGUSDT`, `V3_ARB_CONTRACT`, `V3_PRIVATE_KEY`.
