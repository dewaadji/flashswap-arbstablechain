package contract

// StableArbV2V3 ABI — hardcoded from forge compilation.
const StableArbV2V3ABI = `[
  {"type":"constructor","inputs":[{"name":"_v2Router","type":"address"},{"name":"_v3Router","type":"address"},{"name":"_usdt0","type":"address"}],"stateMutability":"nonpayable"},
  {"type":"function","name":"executeArb","inputs":[{"name":"token","type":"address"},{"name":"v3Fee","type":"uint24"},{"name":"dir","type":"uint8"},{"name":"minProfit","type":"uint256"},{"name":"deadline","type":"uint256"}],"outputs":[],"stateMutability":"payable"},
  {"type":"function","name":"flashArb","inputs":[{"name":"pair","type":"address"},{"name":"token","type":"address"},{"name":"v3Fee","type":"uint24"},{"name":"dir","type":"uint8"},{"name":"borrowAmt","type":"uint256"},{"name":"minProfit","type":"uint256"}],"outputs":[],"stateMutability":"nonpayable"},
  {"type":"function","name":"owner","inputs":[],"outputs":[{"name":"","type":"address"}],"stateMutability":"view"},
  {"type":"function","name":"usdt0","inputs":[],"outputs":[{"name":"","type":"address"}],"stateMutability":"view"},
  {"type":"function","name":"v2Router","inputs":[],"outputs":[{"name":"","type":"address"}],"stateMutability":"view"},
  {"type":"function","name":"v3Router","inputs":[],"outputs":[{"name":"","type":"address"}],"stateMutability":"view"}
]`

// V2 Pair ABI — minimal subset.
const PairABI = `[
  {"constant":true,"inputs":[],"name":"getReserves","outputs":[{"internalType":"uint112","name":"_reserve0","type":"uint112"},{"internalType":"uint112","name":"_reserve1","type":"uint112"},{"internalType":"uint32","name":"_blockTimestampLast","type":"uint32"}],"type":"function"},
  {"constant":true,"inputs":[],"name":"token0","outputs":[{"internalType":"address","name":"","type":"address"}],"type":"function"},
  {"constant":true,"inputs":[],"name":"token1","outputs":[{"internalType":"address","name":"","type":"address"}],"type":"function"}
]`

// V2 Factory ABI — minimal subset.
const V2FactoryABI = `[
  {"constant":true,"inputs":[],"name":"allPairsLength","outputs":[{"internalType":"uint256","name":"","type":"uint256"}],"type":"function"},
  {"constant":true,"inputs":[{"internalType":"uint256","name":"","type":"uint256"}],"name":"allPairs","outputs":[{"internalType":"address","name":"","type":"address"}],"type":"function"}
]`

// V3 Factory ABI — minimal subset.
const V3FactoryABI = `[
  {"constant":true,"inputs":[{"internalType":"address","name":"","type":"address"},{"internalType":"address","name":"","type":"address"},{"internalType":"uint24","name":"","type":"uint24"}],"name":"getPool","outputs":[{"internalType":"address","name":"","type":"address"}],"type":"function"}
]`

// V3 Quoter V2 ABI — minimal subset.
const QuoterABI = `[
  {"constant":true,"inputs":[{"internalType":"address","name":"tokenIn","type":"address"},{"internalType":"address","name":"tokenOut","type":"address"},{"internalType":"uint24","name":"fee","type":"uint24"},{"internalType":"uint256","name":"amountIn","type":"uint256"},{"internalType":"uint160","name":"sqrtPriceLimitX96","type":"uint160"}],"name":"quoteExactInputSingle","outputs":[{"internalType":"uint256","name":"amountOut","type":"uint256"}],"type":"function"}
]`

// ERC20 ABI — balanceOf only.
const ERC20ABI = `[
  {"constant":true,"inputs":[{"internalType":"address","name":"account","type":"address"}],"name":"balanceOf","outputs":[{"internalType":"uint256","name":"","type":"uint256"}],"type":"function"}
]`
