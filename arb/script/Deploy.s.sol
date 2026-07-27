// SPDX-License-Identifier: MIT
pragma solidity ^0.8.23;

import {Script, console} from "forge-std/Script.sol";
import {StableArbV2V3} from "../src/StableArbV2V3.sol";

/// @notice Deploy StableArbV2V3 to Stable Chain (mainnet 988).
///
/// Usage:
///   forge script script/Deploy.s.sol --rpc-url https://rpc.stable.xyz \
///     --broadcast -vvvv
///
/// Env vars:
///   V2_ROUTER  – Uniswap V2 Router02
///   V3_ROUTER  – Uniswap V3 SwapRouter02
///   USDT0      – canonical ERC-20 USDT0 (= native, 6 decimals)
contract Deploy is Script {
    function run() external {
        address v2Router = vm.envAddress("V2_ROUTER");
        address v3Router = vm.envAddress("V3_ROUTER");
        address usdt0    = vm.envAddress("USDT0");
        address wgusdt   = vm.envOr("WGUSDT", address(0));

        vm.startBroadcast();
        StableArbV2V3 arb = new StableArbV2V3(v2Router, v3Router, usdt0, wgusdt);
        vm.stopBroadcast();

        console.log("StableArbV2V3 deployed at: %s", address(arb));
        console.log("  owner:   %s", arb.owner());
        console.log("  v2Router:%s", arb.v2Router());
        console.log("  v3Router:%s", arb.v3Router());
        console.log("  usdt0:   %s", arb.usdt0());
        console.log("  wgusdt:  %s", arb.wgusdt());
    }
}
