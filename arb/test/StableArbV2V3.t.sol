// SPDX-License-Identifier: MIT
pragma solidity ^0.8.23;

import {Test, console} from "forge-std/Test.sol";
import {StableArbV2V3} from "../src/StableArbV2V3.sol";

contract StableArbV2V3Test is Test {
    StableArbV2V3 public arb;

    address constant V2_ROUTER_ADDR = address(0x100);
    address constant V3_ROUTER_ADDR = address(0x200);
    address constant USDT0_ADDR     = address(0x300);

    function setUp() public {
        arb = new StableArbV2V3(V2_ROUTER_ADDR, V3_ROUTER_ADDR, USDT0_ADDR, address(0));
    }

    function test_constructor_owner() public view { assertEq(arb.owner(), address(this)); }
    function test_constructor_usdt0() public view { assertEq(arb.usdt0(), USDT0_ADDR); }
    function test_constructor_routers() public view {
        assertEq(arb.v2Router(), V2_ROUTER_ADDR);
        assertEq(arb.v3Router(), V3_ROUTER_ADDR);
    }

    function test_constructor_wgusdt_nonZero() public {
        StableArbV2V3 a = new StableArbV2V3(V2_ROUTER_ADDR, V3_ROUTER_ADDR, USDT0_ADDR, address(0x500));
        assertEq(a.wgusdt(), address(0x500));
    }

    function test_onlyOwner_reverts() public {
        vm.prank(address(0xdead));
        vm.expectRevert("!owner");
        arb.flashArb(address(0), address(0), 0, 0, 0, 0);
    }

    function test_executeArb_expired_deadline() public {
        vm.expectRevert("EXPIRED");
        arb.executeArb(address(0), 0, 0, 0, block.timestamp - 1);
    }

    function test_executeArb_deadline_exactBlockTimestamp() public {
        // deadline == block.timestamp is valid; won't revert with EXPIRED.
    }

    function test_receive() public {
        (bool ok,) = address(arb).call{value: 1 ether}("");
        assertTrue(ok);
        assertEq(address(arb).balance, 1 ether);
    }

    function test_receive_anySender() public {
        vm.deal(address(0xcafe), 0.5 ether);
        vm.prank(address(0xcafe));
        (bool ok,) = address(arb).call{value: 0.5 ether}("");
        assertTrue(ok);
        assertEq(address(arb).balance, 0.5 ether);
    }
}

// Fork tests: run against real Stable Chain contracts at a specific block.
//
// Usage:
//   forge test --fork-url $STABLE_CHAIN_RPC --fork-block-number <BLOCK> \
//     --match-contract ForkTest -vvvv
//
// To get a trace of a specific test, add -vvvv to see the revert reason.

contract ForkTest is Test {
    StableArbV2V3 public arb;

    // Fill in real Stable Chain addresses.
    address constant V2_ROUTER = address(0); // UniswapV2Router02
    address constant V3_ROUTER = address(0); // UniswapV3SwapRouter
    address constant USDT0     = address(0); // canonical USDT0
    address constant WGUSDT    = address(0); // wrapped USDT0 (or address(0))

    function setUp() public {
        arb = new StableArbV2V3(V2_ROUTER, V3_ROUTER, USDT0, WGUSDT);
    }

    // Template — fill pair, token, fee, borrowAmt for a known opportunity.
    function test_flashArb_dir1() public {
        // address pair  = 0x...;
        // address token = 0x...;
        // uint24  fee   = 500;
        //
        // uint256 borrowAmt = 1 ether;
        //
        // vm.startPrank(ARB_OWNER);
        // arb.flashArb(pair, token, fee, 1, borrowAmt, 0);
        // vm.stopPrank();
    }
}
