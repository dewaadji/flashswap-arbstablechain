.PHONY: build deploy bytecode

bytecode:
	cat ../arb/out/StableArbV2V3.sol/StableArbV2V3.json | python3 -c "import sys,json; print(json.load(sys.stdin)['bytecode']['object'])" > contract.bin

build:
	go build -o stablearb ./cmd/stablearb

deploy:
	go run ./cmd/stablearb -deploy
