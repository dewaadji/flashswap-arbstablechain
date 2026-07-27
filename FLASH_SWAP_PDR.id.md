# Flash Swap — Product Design Reference (PDR) [ID]

Cakupan: **cara kerja mekanik flash-swap**, cara **mendapatkan dan men-deploy contract executor**, dan alasan desain ini **portabel ke chain EVM mana pun yang punya deployment Uniswap V3 + fork bergaya Uniswap V2**.

Dokumen ini **sengaja tidak** mendaftar address router / factory / quoter milik project. Itu nilai infra spesifik-chain yang hidup di `.env` / `config.go`; anggap sebagai input, bukan bagian dari mekanik.

---

## 1. Apa itu flash swap (dan kenapa dipakai)

**Flash swap** adalah primitive Uniswap-V2: pair mengirim token output ke lo **sebelum** lo bayar, lalu memanggil balik (callback) ke contract lo. Di dalam callback itu lo bebas ngapain aja — asalkan, pas callback selesai, pair sudah dilunasi (entah token satunya, atau token yang sama plus fee 0.3%). Kalau belum lunas, **seluruh transaksi revert**.

Kita eksploitasi ini buat arbitrage dengan **modal kerja nol**:

```
satu transaksi atomik  (revert => lo gak rugi apa-apa selain gas)
  1. pinjam aset X dari V2 pair              <- pair yang nalangin modal
  2. swap X -> Y di Uniswap V3               <- selisih harga yang kita deteksi
  3. lunasi V2 pair (aset Y, atau X+fee)
  4. sisanya = profit                        <- disapu ke wallet owner
```

Modal yang berisiko cuma **gas**. Jumlah yang dipinjam gak pernah jadi milik kita untuk hilang — kalau step 2 gak menghasilkan cukup buat step 3, step 3 revert dan pinjaman dibatalkan. Ini seluruh cerita keamanannya: **kalah race = rugi gas, bukan principal.**

### Kenapa harus V2-pair-sebagai-lender
Leg pinjam itu cuma `swap()` biasa di pair Uniswap-V2 dengan argumen `data` non-kosong — itu memicu flash callback, bukan minta bayar di muka. Jadi "lender"-nya cuma pair DEX biasa; gak butuh pool flash-loan Aave/Balancer, dan gak ada fee flash-loan terpisah selain fee swap si pair. Di **fork** V2, callback-nya di-rename (misal `dyorCall(...)` bukan `uniswapV2Call(...)`), tapi mekaniknya identik byte-for-byte.

---

## 2. Dua arah

Bot menghitung harga kedua arah tiap block dan fire mana pun yang profit, di size terbaik. "Native" di sini = gas token chain; di sebagian chain sama dengan stable quote, di chain lain lo wrap/unwrap — detail itu spesifik-chain, bentuknya tidak.

| Dir | Fire ketika | Pinjam dari V2 pair | Swap di V3 | Lunasi pair pakai | Bentuk profit |
|-----|-------------|---------------------|------------|-------------------|---------------|
| **1** | token murah di V2 | si **token** | jual token → stable | aset quote V2 | sisa native |
| **2** | token murah di V3 | **stable/wrapper** | beli token pakai stable | si **token** | jual sisa token |

Direction 2 merangkai dua swap lewat pool V3 yang sama di dalam satu callback, yang gak bisa dimodelkan penuh oleh quoter off-chain — jadi arah ini **selalu disimulasi on-chain (`eth_call`) sebelum broadcast**, bahkan saat preflight lainnya dilewati.

---

## 3. Contract executor on-chain

Keempat step terjadi di dalam satu contract, `StableArbV2V3.sol`. Bot gak pernah mengorkestrasi leg dari off-chain — dia cuma kirim **satu** call dan biarkan contract melakukan borrow→swap→repay→sweep secara atomik. Dua entrypoint:

```solidity
// jalur zero-capital — pair nalangin semua
function flashArb(
    address pair,        // V2 pair yang minjemin
    address token,       // token yang di-arb
    uint24  v3Fee,       // pool fee tier V3 mana yang dilewati
    uint8   dir,         // 1 atau 2
    uint256 borrowAmt,   // berapa yang di-flash-borrow
    uint256 minProfit    // revert kecuali saldo native akhir lewat angka ini
) external;              // onlyOwner

// jalur fallback modal-sendiri (msg.value yang mendanai trade)
function executeArb(
    address token,
    uint24  v3Fee,
    uint8   dir,
    uint256 minProfit,
    uint256 deadline
) external payable;      // onlyOwner
```

Jaminan desain yang tertanam di contract:

- **`onlyOwner`** — cuma wallet deployer yang bisa panggil. Makanya deployer **harus** key bot itu sendiri (lihat §4).
- **Guard `minProfit`** — contract revert kecuali saldo native akhir lewat `minProfit`. Set `minProfit ≥ biaya gas` dan fill yang rugi tinggal revert; lo bayar gas, gak pernah pinjaman. Ini garis pertahanan terakhir walau quote off-chain salah.
- **Flash callback** — contract implement selector callback fork V2 (`dyorCall` / `uniswapV2Call`) supaya pair bisa nyerahin token pinjaman di tengah transaksi.

---

## 4. Cara mendapatkan smart contract

Ada dua cara resmi buat dapetin bytecode yang bisa di-deploy. **Lo gak butuh source Solidity atau toolchain Foundry buat deploy** — creation bytecode-nya sudah ditanam di binary Go.

### Opsi A — deploy bytecode tertanam (default, tanpa toolchain)
Repo membawa `internal/bot/contract.bin` = **creation bytecode** contract, sengaja di-commit. `deploy.go` menanamnya via `//go:embed` dan men-deploy langsung:

```bash
make deploy      # == go run ./cmd/stablearb -deploy
```

Alur deploy (`internal/bot/deploy.go`):
1. Baca `contract.bin`, tempel argumen constructor ter-ABI-encode
   `constructor(address v2router, address v3router, address usdt0)`
   — ini datang dari `.env` lo (`V3_V2_ROUTER`, `V3_V3_ROUTER`, plus token native/stable).
2. Tandatangani tx creation dengan **key bot lo** (`V3_PK_FILE`) → **owner = wallet bot** (wajib, karena kedua entrypoint `onlyOwner`).
3. Tunggu receipt dan cetak address hasil deploy.
4. Taruh address itu di `V3_ARB_CONTRACT` dan lo siap.

```
[deploy] predicted contract address: 0x...
[deploy] ✅ DEPLOYED at 0x... (block=... gasUsed=...)
[deploy] set V3_ARB_CONTRACT=0x... in .env, then run live.
```

### Opsi B — rebuild bytecode dari source
Source Solidity (`StableArbV2V3.sol`) ada di project Foundry sibling (`../arb`). Kalau lo ubah contract, rebuild artifact tertanam dan deploy ulang:

```bash
make bytecode    # regenerasi internal/bot/contract.bin dari ../arb/out/...
make deploy
```

> Poin dari commit `contract.bin`: operator bisa deploy di VPS baru cukup dengan binary Go — tanpa `forge`, tanpa pohon source.

---

## 5. Portability — chain EVM mana pun yang punya Uniswap

Mekaniknya **gak punya dependensi keras ke chain spesifik (988)** yang jadi setelan bawaannya. Semua yang spesifik-chain digerakkan env; gak ada yang unik satu chain soal "flash-borrow di V2 pair → swap di V3 → lunasi atomik".

Buat retarget ke chain EVM lain, chain itu cuma perlu punya:

1. **Deployment Uniswap V3** — router, quoter, factory (buat leg V3 + quoting off-chain).
2. **DEX bergaya Uniswap-V2** (V2 kanonik, atau fork mana pun seperti DYORswap/Pancake/dll) yang pair-nya mendukung flash-swap callback — ini si lender.
3. Deployment `Multicall3` (address standar `0xcA11…` di kebanyakan chain) buat baca state satu-`eth_call`-per-block. Opsional; fallback ke JSON-RPC batching.

Lalu set, per chain:

| Yang diubah | Di mana |
|-------------|---------|
| `chainId`, RPC HTTP/WS | `V3_CHAIN_ID`, `V3_RPC_HTTP`, `V3_RPC_WS` |
| V2 router + factory (fork si lender) | `V3_V2_ROUTER`, `V3_V2_FACTORY` |
| V3 router + quoter + factory | `V3_V3_ROUTER`, `V3_V3_QUOTER`, `V3_V3_FACTORY` |
| token stable / wrapper / native | `V3_USDT0`, `V3_WGUSDT` |
| wallet bot terpisah yang terisi | `V3_PK_FILE` |

Dua hal yang **bukan** sekadar config dan harus dicek saat porting:

- **Selector flash callback.** Contract implement nama callback fork yang spesifik. V2 kanonik pakai `uniswapV2Call`; fork bisa me-rename (build ini menyasar `dyorCall`). Kalau fork V2 chain target pakai selector beda, **contract**-nya yang harus implement selector itu (rebuild Opsi B) — env doang gak cukup.
- **Relasi native ↔ stable.** Build ini asumsi gas token native *adalah* stable quote-nya (jadi hasil V3 mendarat sebagai native tanpa bridge). Di chain yang native ≠ stable (misal chain gas-ETH yang nge-arb pair USDC), logic unwrap/settle di contract perlu disesuaikan. Layer pricing Go itu agnostik-arah; bagian yang perlu direview adalah **jalur settlement di contract**.

Selebihnya — discovery, math AMM lokal, kedua arah, guard `minProfit`, alur deploy — agnostik-chain.

---

## 6. Checklist go-live minimal

1. `cp .env.example .env`, isi RPC + wallet `V3_PK_FILE` **khusus** (key sendiri; dua bot live di satu key = tabrakan nonce).
2. Isi wallet itu dengan sedikit token native — **gas doang**; trade didanai flash.
3. `make deploy` → salin address ke `V3_ARB_CONTRACT`.
4. Jalankan dengan `V3_DRY_RUN=true`; pantau baris `hold` / spread buat mastiin quoting waras.
5. `go run ./cmd/validate` — math AMM lokal vs quoter on-chain, per rung size; kolom V2 harus baca `exact`.
6. Set `V3_DRY_RUN=false`, restart → armed. `minProfit ≥ gas` berarti downside tetap "gas doang" dari fire pertama.

---

## 7. Model mental satu baris

> Pinjam dari V2 pair gratis, jual ke celah harga V3, lunasi pair-nya, ambil kembaliannya — semua dalam satu transaksi yang revert (rugi gas-doang) kecuali lewat lantai profit keras on-chain. Satu-satunya bagian spesifik-chain adalah address di `.env` dan detail callback/settlement contract.
