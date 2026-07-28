# arb/ — Solidity Smart Contract

Zero-capital flash-swap arbitrage contract targeting Stable Chain (chain ID 988).

## Build & Test

```bash
forge build                          # compile
forge test                           # run all tests
forge test --fork-url $RPC --fork-block-number <N> --match-contract ForkTest -vvvv  # fork test
forge script script/Deploy.s.sol --rpc-url $RPC --broadcast -vvvv                    # deploy
```

Set env vars for deploy: `V2_ROUTER`, `V3_ROUTER`, `USDT0` (required); `WGUSDT` (optional, defaults to `address(0)`).

## Architecture

Single-file contract (`src/StableArbV2V3.sol`) — no inheritance, no external deps beyond `forge-std`.

**Two entrypoints (both onlyOwner):**

| Function | Capital source | Mechanism |
|---|---|---|
| `flashArb()` | V2 flash swap (0.3% fee) | `pair.swap()` → `uniswapV2Call` callback → repay pair → sweep profit |
| `executeArb()` | Own capital (`msg.value`) | Direct swap — no callback, requires `deadline >= block.timestamp` |

**Two directions:**

- **Dir 1**: Borrow token from V2, sell on V3 for USDT0, repay V2 with USDT0
- **Dir 2**: Borrow USDT0 from V2, buy token on V3, repay V2 with token, sell leftover on V3

**Stable Chain quirks handled:**
- Native token = USDT0 (ERC-20 with 6 decimals). `address(this).balance` and ERC-20 balance are shared.
- Some V2 pairs use **WgUSDT** (18-dec wrapped USDT0) instead of native USDT0 — `_dir1Callback` auto-detects and wraps USDT0 → WgUSDT for repayment.
- Constructor accepts optional `wgusdt` address; pass `address(0)` if unused.

**Key design decisions:**
- `amountOutMinimum = 0` on all V3 swaps — profit protection relies on `minProfit` check after callback
- `balBefore` captured before swap and checked after to enforce `profit >= minProfit`
- All swap helpers (`_sellTokenOnV3`, `_buyTokenOnV3`, `_buyTokenOnV2`, `_sellTokenOnV2`) are internal

## Test structure

- `test/StableArbV2V3.t.sol` — unit tests (constructor, onlyOwner, receive, deadline) + `ForkTest` scaffold
- `ForkTest` is a template — fill real Stable Chain addresses to run against forked state
- `cache/test-failures` lists old tests that no longer exist; ignore it

## Deployment

Two deployments on Stable Chain mainnet recorded in `broadcast/Deploy.s.sol/988/`. Latest at `run-latest.json`.
