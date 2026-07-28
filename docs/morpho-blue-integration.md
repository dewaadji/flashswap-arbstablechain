# Morpho Blue Flash Loan Integration — Brief

## Konteks

Saat ini bot menggunakan **Uniswap V2 flash swap** sebagai sumber dana arbitrase,
dari dua factory: **Uniswap V2 canonical** (`V2Factory`) dan **DYORswap** (`V2Factory2`).
Keduanya adalah V2-compatible DEX dengan mekanisme flash swap yang sama (fee **0.3%**
pada repayment), yang secara signifikan menggerus profit untuk trade yang tipis
spread-nya.

**Morpho Blue** adalah lending protocol dengan flash loan **0% fee**, sudah deploy di
Stable Chain. Dengan mengadopsi Morpho sebagai sumber dana alternatif, threshold
profit bisa lebih rendah dan lebih banyak opportunity yang tereksekusi.

## Kondisi Morpho di Stable Chain

| Item | Detail |
|---|---|
| Status | Sudah deploy di Stable Chain |
| Market | 3 market dengan loan asset USDT0 |
| Collateral | sthUSD, thBILL |
| Oracle | Gauntlet, Hyperithm |
| Likuiditas | >$500k per market |

**Loan asset = USDT0** — kita pinjam USDT0 langsung, bukan token arb. Ini simplifikasi
besar karena tidak perlu logic konversi WgUSDT, tidak perlu kalkulasi repayment fee.

## Arsitektur Saat Ini vs Setelah Morpho

```
Sebelum:
  flashArb(pair, token, fee, dir, borrowAmt, minProfit)
    → V2 pair.swap()           [pinjam token atau USDT0, fee 0.3%]
                                [pair dari Uniswap V2 canonical atau DYORswap]
    → uniswapV2Call()          [callback: beli/jual di V3, repay pair]

  executeArb(token, fee, dir, minProfit, deadline)
    → msg.value                [modal sendiri]

Sesudah:
  flashArb(...)                [tetap, tidak berubah]
  executeArb(...)              [tetap, tidak berubah]
  morphoFlashArb(marketId, token, v3Fee, dir, borrowAmt, minProfit)  [BARU]
    → Morpho.flashLoan(usdt0, borrowAmt)  [pinjam USDT0, fee 0%]
    → onMorphoFlashLoan()     [callback: beli/jual di V2/V3, repay Morpho]
```

## Flow Trade Morpho

### Dir 1 — V2 murah, V3 mahal
```
1. Morpho kirim USDT0 borrowAmt
2. Beli token di V2 pakai USDT0 (_buyTokenOnV2)
3. Jual semua token di V3 (_sellTokenOnV3)
4. Transfer borrowAmt USDT0 kembali ke Morpho
5. Sweep sisa native USDT0 ke owner = profit
```

### Dir 2 — V3 murah, V2 mahal
```
1. Morpho kirim USDT0 borrowAmt
2. Beli token di V3 pakai USDT0 (_buyTokenOnV3)
3. Jual semua token di V2 (_sellTokenOnV2)
4. Transfer borrowAmt USDT0 kembali ke Morpho
5. Sweep sisa native USDT0 ke owner = profit
```

Perbandingan dengan flow V2 flash swap:
- Tidak perlu `_v2RepayAmountOther` (konversi repayment dengan fee)
- Tidak perlu logic WgUSDT wrapping/unwrapping
- Repay = borrowAmt (exact, tanpa fee)
- Callback lebih simpel

## Perubahan per Komponen

### 1. Solidity — entrypoint + callback baru

File: `arb/src/StableArbMorpho.sol` (kontrak baru, tidak sentuh `StableArbV2V3.sol`)

```
- constructor(morphoAddr, v2Router, v3Router, usdt0)
- morphoFlashArb(marketId, token, v3Fee, dir, borrowAmt, minProfit)
- onMorphoFlashLoan(uint256 assets, bytes data)
```

Kontrak baru supaya tidak merusak yang sudah production. Logic V2/V3 swap
(`_buyTokenOnV2`, `_sellTokenOnV2`, dll) bisa di-copy dari kontrak existing.

### 2. Discovery — scan Morpho market

File: `bot/internal/discover/discover.go`

```go
func MorphoMarket(ctx, client, morphoAddr, usdt0 common.Address) ([32]byte, bool)
```

Karena loan asset selalu USDT0, tidak perlu scan per-token. Cukup tahu apakah Morpho
ada di chain dan punya market dengan likuiditas.

### 3. Bot cache — extend cachedPair

File: `bot/cmd/stablearb/main.go`

```go
type cachedPair struct {
    // ... existing ...
    MorphoMarketId [32]byte  // zero = tidak tersedia
}
```

### 4. Trader — method baru untuk Morpho

File: `bot/internal/trader/trader.go`

```go
func (t *Trader) MorphoFlashArb(ctx, marketId [32]byte, token common.Address,
    v3Fee int64, dir uint8, borrowAmt, minProfit *big.Int) (string, error)
```

Mirip `FlashArb`, dengan ABI packing untuk method `morphoFlashArb`.
Simulasi pre-flight tetap jalan via `eth_call`.

### 5. Main loop — path ketiga di runOnce

File: `bot/cmd/stablearb/main.go`

```go
// MORPHO PATH
if cp.MorphoMarketId != [32]byte{} {
    borrowStable := new(big.Int).Div(resUSD, big.NewInt(100))

    // Dir M1: borrow USDT0 → buy V2 → sell V3
    tokenBought := price.V2AmountOut(borrowStable, resUSD, resTok)
    usdt0FromV3 := price.EstimateV3Output(tokenBought, sqrtPrice, fee, poolTokIsTok0)
    profitM1 := usdt0FromV3 - borrowStable - gasCost

    // Dir M2: borrow USDT0 → buy V3 → sell V2
    tokenBoughtV3 := price.EstimateV3Buy(borrowStable, sqrtPrice, fee, poolTokIsTok0)
    usdt0FromV2 := price.V2AmountOut(tokenBoughtV3, resTok, resUSD)
    profitM2 := usdt0FromV2 - borrowStable - gasCost
}
```

### 6. Price — tidak ada perubahan

Semua fungsi math existing (`V2AmountOut`, `V2AmountIn`, `EstimateV3Output`,
`EstimateV3Buy`, `AddSlippage`) dipakai ulang.

## Dampak Profit

Perbandingan dengan size trade yang sama (borrow ~$1,000):

| Sumber Dana | Fee Repayment | Contoh Profit |
|---|---|---|
| V2 flash swap | 0.3% (~$3 per $1k) | Margin untuk profit lebih tipis |
| Morpho flash loan | 0% | Tambahan ~$3-30/trade |

**Ini bukan silver bullet.** Spread tetap harus cukup lebar antara V2 dan V3 untuk
profit setelah V3 fee + slippage. Tapi threshold profit lebih rendah karena sisi
repayment tidak kena 0.3%. Opportunity yang tadinya borderline (-$1 s/d +$2)
menjadi layak eksekusi.

## Yang Tidak Berubah

- Discovery V2 pairs dan V3 pools — tetap scan Uniswap factories
- Math V2/V3 — semua fungsi price.go dipakai ulang
- Flow existing (flashArb, executeArb, testfire) — tetap jalan tanpa modifikasi
- Simulasi pre-flight via `eth_call` — tetap bisa dipakai

## Risiko

1. **Likuiditas Morpho** — supply asset $500k+ saat ini, tapi bisa berubah.
   Kalau supply ditarik, opportunity hilang.
2. **Oracle/IRM risk** — trusted by Gauntlet dan Hyperithm, jadi relatif aman.
3. **Gas overhead kontrak baru** — deploy kontrak tambahan, tapi gas untuk
   trade Morpho mirip dengan V2 flash swap (tidak lebih mahal).

## Referensi

- rh-flash-toolkit: https://github.com/FlipZ3ro/rh-flash-toolkit (FlashToolkit
  unified contract dengan Morpho + Uniswap V2)
- Morpho Blue docs: https://docs.morpho.org/
