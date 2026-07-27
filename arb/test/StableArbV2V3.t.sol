// SPDX-License-Identifier: MIT
pragma solidity ^0.8.23;

import {Test, console} from "forge-std/Test.sol";
import {StableArbV2V3} from "../src/StableArbV2V3.sol";

contract StableArbV2V3Test is Test {
    StableArbV2V3 public arb;

    address constant V2_ROUTER = address(0x100);
    address constant V3_ROUTER = address(0x200);
    address constant USDT0     = address(0x300);

    function setUp() public {
        arb = new StableArbV2V3(V2_ROUTER, V3_ROUTER, USDT0, address(0));
    }

    function test_constructor_owner() public view {
        assertEq(arb.owner(), address(this));
    }

    function test_constructor_usdt0() public view {
        assertEq(arb.usdt0(), USDT0);
    }

    function test_constructor_routers() public view {
        assertEq(arb.v2Router(), V2_ROUTER);
        assertEq(arb.v3Router(), V3_ROUTER);
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

    function test_receive() public {
        (bool ok,) = address(arb).call{value: 1 ether}("");
        assertTrue(ok);
        assertEq(address(arb).balance, 1 ether);
    }
}
