package config

import (
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	ChainID     int64
	RPCHTTP     string
	RPCWS       string
	V2Router    string
	V2Factory   string
	V3Router    string
	V3Factory   string
	V3Quoter    string
	USDT0       string
	ArbContract string
	PrivateKey  string
	MinProfit   *big.Int
	MaxGasPrice *big.Int
	DryRun      bool
	SlippageBPS int64
}

func Load(path string) (*Config, error) {
	if path == "" {
		path = ".env"
	}

	_ = godotenv.Load(path)

	cfg := &Config{}

	var ok bool

	cfg.RPCHTTP, ok = env("V3_RPC_HTTP")
	if !ok {
		return nil, fmt.Errorf("V3_RPC_HTTP required")
	}
	cfg.RPCWS = os.Getenv("V3_RPC_WS")
	cfg.V2Router, ok = env("V3_V2_ROUTER")
	if !ok {
		return nil, fmt.Errorf("V3_V2_ROUTER required")
	}
	cfg.V2Factory, ok = env("V3_V2_FACTORY")
	if !ok {
		return nil, fmt.Errorf("V3_V2_FACTORY required")
	}
	cfg.V3Router, ok = env("V3_V3_ROUTER")
	if !ok {
		return nil, fmt.Errorf("V3_V3_ROUTER required")
	}
	cfg.V3Factory, ok = env("V3_V3_FACTORY")
	if !ok {
		return nil, fmt.Errorf("V3_V3_FACTORY required")
	}
	cfg.V3Quoter, ok = env("V3_V3_QUOTER")
	if !ok {
		return nil, fmt.Errorf("V3_V3_QUOTER required")
	}
	cfg.USDT0, ok = env("V3_USDT0")
	if !ok {
		return nil, fmt.Errorf("V3_USDT0 required")
	}
	cfg.ArbContract = os.Getenv("V3_ARB_CONTRACT")

	cfg.PrivateKey = os.Getenv("V3_PRIVATE_KEY")

	chainStr := os.Getenv("V3_CHAIN_ID")
	if chainStr != "" {
		var chainID int64
		if _, err := fmt.Sscanf(chainStr, "%d", &chainID); err != nil {
			return nil, fmt.Errorf("bad V3_CHAIN_ID: %w", err)
		}
		cfg.ChainID = chainID
	} else {
		cfg.ChainID = 988
	}

	cfg.DryRun = strings.ToLower(os.Getenv("V3_DRY_RUN")) != "false"

	cfg.MinProfit = envBig("V3_MIN_PROFIT", "100000")
	cfg.MaxGasPrice = envBig("V3_MAX_GAS_PRICE", "50000000")

	slippageStr := os.Getenv("V3_SLIPPAGE_BPS")
	if slippageStr == "" {
		slippageStr = "50"
	}
	if _, err := fmt.Sscanf(slippageStr, "%d", &cfg.SlippageBPS); err != nil {
		return nil, fmt.Errorf("bad V3_SLIPPAGE_BPS: %w", err)
	}

	return cfg, nil
}

func env(key string) (string, bool) {
	v := os.Getenv(key)
	if v == "" {
		return "", false
	}
	return v, true
}

func envBig(key, def string) *big.Int {
	v := os.Getenv(key)
	if v == "" {
		v = def
	}
	n := new(big.Int)
	n.SetString(v, 10)
	return n
}
