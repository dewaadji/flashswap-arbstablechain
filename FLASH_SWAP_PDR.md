# Flash Swap — Product Design Reference (PDR)

Scope: **how the flash-swap mechanic works**, how to **obtain and deploy the executor contract**, and why the design is **portable to any EVM chain that has a Uniswap V3 deployment + a Uniswap-V2-style fork**.

This document deliberately does **not** enumerate the project's router / factory / quoter addresses. Those are chain-specific infra values that live in `.env` / `config.go`; treat them as inputs, not as part of the mechanic.

---

## 1. What a flash swap is (and why we use it)

A **flash swap** is a Uniswap-V2 primitive: the pair sends you the output tokens **before** you pay for them, then calls back into your contract. Inside that callback you can do anything — as long as, by the time the callback returns, the pair has been repaid (either the other token, or the same token plus the 0.3% fee). If it isn't repaid, the whole transaction reverts.

We exploit this to run arbitrage with **zero working capital**:

```
one atomic transaction  (revert => you lose nothing but gas)
  1. borrow asset X from the V2 pair        <- pair fronts the capital
  2. swap X -> Y on Uniswap V3              <- the price dislocation we detected
  3. repay the V2 pair (asset Y, or X+fee)
  4. whatever is left over = profit          <- swept to the owner wallet
```

The capital at risk is **only gas**. The borrowed amount is never ours to lose — if step 2 doesn't produce enough to satisfy step 3, step 3 reverts and the borrow unwinds. This is the entire safety story: **a lost race costs gas, never principal.**

### Why V2-pair-as-lender specifically
The borrow leg is a normal `swap()` on a Uniswap-V2 pair with the `data` argument non-empty — that triggers the flash callback instead of requiring pre-payment. So the "lender" is just an ordinary DEX pair; no Aave/Balancer flash-loan pool is needed, and there's no separate flash-loan fee beyond the pair's own swap fee. On a V2 **fork**, the callback is renamed (e.g. `dyorCall(...)` instead of `uniswapV2Call(...)`), but the mechanic is byte-for-byte identical.

---

## 2. The two directions

The bot prices both directions every block and fires whichever is profitable, at the best size. "Native" here is the chain's gas token; on some chains it equals the quote stable, on others you wrap/unwrap — that detail is chain-specific, the shape is not.

| Dir | Fires when | Borrow from V2 pair | Swap on V3 | Repay the pair in | Profit form |
|-----|-----------|---------------------|------------|-------------------|-------------|
| **1** | token cheap on V2 | the **token** | sell token → stable | the V2 quote asset | leftover native |
| **2** | token cheap on V3 | the **stable/wrapper** | buy token with stable | the **token** | sell leftover token |

Direction 2 chains two swaps through the same V3 pool inside one callback, which no off-chain quoter can fully model — so it is **always simulated on-chain (`eth_call`) before broadcast**, even when preflight is otherwise skipped.

---

## 3. The on-chain executor contract

All four steps happen inside one contract, `StableArbV2V3.sol`. The bot never orchestrates the legs from off-chain — it only sends **one** call and lets the contract do borrow→swap→repay→sweep atomically. Two entrypoints:

```solidity
// zero-capital path — the pair fronts everything
function flashArb(
    address pair,        // the V2 pair that lends
    address token,       // the token being arbed
    uint24  v3Fee,       // which V3 fee tier pool to route through
    uint8   dir,         // 1 or 2
    uint256 borrowAmt,   // how much to flash-borrow
    uint256 minProfit    // revert unless final native balance clears this
) external;              // onlyOwner

// own-capital fallback path (msg.value funds the trade)
function executeArb(
    address token,
    uint24  v3Fee,
    uint8   dir,
    uint256 minProfit,
    uint256 deadline
) external payable;      // onlyOwner
```

Design guarantees baked into the contract:

- **`onlyOwner`** — only the deployer wallet can call it. That's why the deployer **must** be the bot's own key (see §4).
- **`minProfit` guard** — the contract reverts unless the final native balance clears `minProfit`. Set `minProfit ≥ gas cost` and a losing fill simply reverts; you pay gas, never the borrow. This is the last line of defense even if the off-chain quote was wrong.
- **Flash callback** — the contract implements the V2 fork's callback selector (`dyorCall` / `uniswapV2Call`) so the pair can hand it the borrowed tokens mid-transaction.

---

## 4. How to obtain the smart contract

You have two supported ways to get deployable bytecode. **You do not need the Solidity source or a Foundry toolchain to deploy** — the creation bytecode is embedded in the Go binary.

### Option A — deploy the embedded bytecode (default, no toolchain)
The repo ships `internal/bot/contract.bin` = the contract's **creation bytecode**, committed on purpose. `deploy.go` embeds it via `//go:embed` and deploys it directly:

```bash
make deploy      # == go run ./cmd/stablearb -deploy
```

The deploy path (`internal/bot/deploy.go`):
1. Reads `contract.bin`, appends the ABI-encoded constructor args
   `constructor(address v2router, address v3router, address usdt0)`
   — these come from your `.env` (`V3_V2_ROUTER`, `V3_V3_ROUTER`, plus the native/stable token).
2. Signs the creation tx with **your bot key** (`V3_PK_FILE`) → **owner = bot wallet** (required, because both entrypoints are `onlyOwner`).
3. Waits for the receipt and prints the deployed address.
4. Put that address in `V3_ARB_CONTRACT` and you're armed.

```
[deploy] predicted contract address: 0x...
[deploy] ✅ DEPLOYED at 0x... (block=... gasUsed=...)
[deploy] set V3_ARB_CONTRACT=0x... in .env, then run live.
```

### Option B — rebuild the bytecode from source
The Solidity source (`StableArbV2V3.sol`) lives in a sibling Foundry project (`../arb`). If you change the contract, rebuild the embedded artifact and re-deploy:

```bash
make bytecode    # regenerates internal/bot/contract.bin from ../arb/out/...
make deploy
```

> The point of committing `contract.bin`: the operator can deploy on a fresh VPS with only the Go binary — no `forge`, no source tree.

---

## 5. Portability — any EVM chain with Uniswap

The mechanic has **no hard dependency on the specific chain (988) it ships configured for.** Everything chain-specific is env-driven; nothing about "flash-borrow on a V2 pair → swap on V3 → repay atomically" is unique to one chain.

To retarget another EVM chain, you only need that chain to have:

1. **A Uniswap V3 deployment** — router, quoter, factory (for the V3 leg + off-chain quoting).
2. **A Uniswap-V2-style DEX** (canonical V2, or any fork like DYORswap/Pancake/etc.) whose pairs support the flash-swap callback — this is the lender.
3. A `Multicall3` deployment (standard `0xcA11…` address on most chains) for the one-`eth_call`-per-block state read. Optional; falls back to JSON-RPC batching.

Then set, per chain:

| What to change | Where |
|----------------|-------|
| `chainId`, HTTP/WS RPC | `V3_CHAIN_ID`, `V3_RPC_HTTP`, `V3_RPC_WS` |
| V2 router + factory (the lender fork) | `V3_V2_ROUTER`, `V3_V2_FACTORY` |
| V3 router + quoter + factory | `V3_V3_ROUTER`, `V3_V3_QUOTER`, `V3_V3_FACTORY` |
| stable / wrapper / native token | `V3_USDT0`, `V3_WGUSDT` |
| a funded, isolated bot wallet | `V3_PK_FILE` |

Two things that are **not** just config and must be checked when porting:

- **The flash callback selector.** The contract implements the fork's specific callback name. Canonical V2 uses `uniswapV2Call`; a fork may rename it (this build targets `dyorCall`). If the target chain's V2 fork uses a different selector, the **contract** must implement that selector (Option B rebuild) — env alone won't fix it.
- **Native ↔ stable relationship.** This build assumes the native gas token *is* the quote stable (so V3 proceeds land as native with no bridge). On a chain where native ≠ stable (e.g. ETH-gas chains arbing a USDC pair), the unwrap/settle logic in the contract needs adjusting. The Go pricing layer is direction-agnostic; the **contract's** settlement path is the part to review.

Everything else — discovery, local AMM math, both directions, the `minProfit` guard, the deploy flow — is chain-agnostic.

---

## 6. Minimal go-live checklist

1. `cp .env.example .env`, fill RPC + a **dedicated** `V3_PK_FILE` wallet (its own key; two live bots on one key = nonce collisions).
2. Fund that wallet with a little native token — **gas only**; trades are flash-funded.
3. `make deploy` → copy the address into `V3_ARB_CONTRACT`.
4. Run with `V3_DRY_RUN=true`; watch the `hold` / spread lines to confirm quoting is sane.
5. `go run ./cmd/validate` — local AMM math vs on-chain quoter, per size rung; V2 columns should read `exact`.
6. Set `V3_DRY_RUN=false`, restart → armed. `minProfit ≥ gas` means the downside stays "gas only" from the first fire.

---

## 7. One-line mental model

> Borrow from a V2 pair for free, sell into a V3 price gap, repay the pair, keep the change — all in one transaction that reverts (gas-only loss) unless it clears a hard on-chain profit floor. The only chain-specific parts are addresses in `.env` and the contract's callback/settlement details.
