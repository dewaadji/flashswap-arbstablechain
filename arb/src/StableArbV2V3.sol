// SPDX-License-Identifier: MIT
pragma solidity ^0.8.23;

/// @notice Standard Uniswap V2 flash-swap callee interface.
/// On Stable Chain (chain 988) the canonical V2 factory + pairs use
/// the original ``uniswapV2Call`` selector.
interface IUniswapV2Callee {
    function uniswapV2Call(address sender, uint256 amount0, uint256 amount1, bytes calldata data) external;
}

interface IUniswapV2Pair {
    function swap(uint256 amount0Out, uint256 amount1Out, address to, bytes calldata data) external;
    function token0() external view returns (address);
    function token1() external view returns (address);
    function getReserves() external view returns (uint112 reserve0, uint112 reserve1, uint32 blockTimestampLast);
}

interface IUniswapV2Router02 {
    function factory() external pure returns (address);
    function swapExactETHForTokens(
        uint256 amountOutMin, address[] calldata path, address to, uint256 deadline
    ) external payable returns (uint256[] memory amounts);
    function swapExactTokensForETH(
        uint256 amountIn, uint256 amountOutMin, address[] calldata path, address to, uint256 deadline
    ) external returns (uint256[] memory amounts);
}

interface IUniswapV3SwapRouter {
    struct ExactInputSingleParams {
        address tokenIn;
        address tokenOut;
        uint24 fee;
        address recipient;
        uint256 amountIn;
        uint256 amountOutMinimum;
        uint160 sqrtPriceLimitX96;
    }

    function exactInputSingle(ExactInputSingleParams calldata params)
        external
        payable
        returns (uint256 amountOut);
}

interface IERC20 {
    function balanceOf(address account) external view returns (uint256);
    function transfer(address to, uint256 amount) external returns (bool);
    function approve(address spender, uint256 amount) external returns (bool);
}

/// @title StableArbV2V3
/// @notice Zero-capital flash-swap arbitrage between Uniswap V2 pairs and
///         Uniswap V3 pools on Stable Chain.
///
/// Stable Chain uses USDT0 as both the native gas token and the canonical
/// ERC-20.  Balances are shared — a native transfer updates the ERC-20
/// balance and vice versa — so no wrapping/unwrapping is ever needed.
///
/// Two entrypoints (both ``onlyOwner``):
///   - ``flashArb``  – borrow from a V2 pair via flash-swap, execute the arb,
///                     repay the pair, sweep native USDT0 profit to owner.
///   - ``executeArb`` – same two-leg arb but funded with ``msg.value``
///                     (own-capital fallback).
contract StableArbV2V3 is IUniswapV2Callee {
    address public immutable owner;
    address public immutable v2Router;
    address public immutable v3Router;
    address public immutable usdt0; // canonical ERC-20 USDT0 (= native)

    constructor(address _v2Router, address _v3Router, address _usdt0) {
        owner = msg.sender;
        v2Router = _v2Router;
        v3Router = _v3Router;
        usdt0 = _usdt0;
    }

    modifier onlyOwner() {
        require(msg.sender == owner, "!owner");
        _;
    }

    // ------------------------------------------------------------------------
    // Entrypoint 1 — zero-capital flash-swap arb
    // ------------------------------------------------------------------------

    /// @param pair       V2 pair that will lend the tokens.
    /// @param token      The non-stable asset being arbed (must be token0 or
    ///                   token1 of the pair).
    /// @param v3Fee      Fee tier of the V3 pool used for the swap leg(s).
    /// @param dir        1 = borrow token, repay USDT0; 2 = borrow USDT0,
    ///                   repay token.
    /// @param borrowAmt  Amount to flash-borrow from the V2 pair.
    /// @param minProfit  Revert if final native balance gain is below this.
    function flashArb(
        address pair,
        address token,
        uint24 v3Fee,
        uint8 dir,
        uint256 borrowAmt,
        uint256 minProfit
    ) external onlyOwner {
        uint256 balBefore = address(this).balance;

        (uint256 amount0Out, uint256 amount1Out) = _flashOutAmounts(pair, token, dir, borrowAmt);

        bytes memory cbData = abi.encode(pair, token, v3Fee, dir, minProfit, balBefore);
        IUniswapV2Pair(pair).swap(amount0Out, amount1Out, address(this), cbData);

        uint256 profit = address(this).balance - balBefore;
        require(profit >= minProfit, "profit < min");

        _sweepNative(owner, address(this).balance);
    }

    // ------------------------------------------------------------------------
    // Entrypoint 2 — own-capital fallback arb
    // ------------------------------------------------------------------------

    function executeArb(
        address token,
        uint24 v3Fee,
        uint8 dir,
        uint256 minProfit,
        uint256 deadline
    ) external payable onlyOwner {
        require(deadline >= block.timestamp, "EXPIRED");
        uint256 balBefore = address(this).balance;

        // On Stable Chain native == ERC-20 USDT0; msg.value is immediately
        // available as an ERC-20 balance too — no deposit needed.
        if (dir == 1) {
            _buyTokenOnV2(token, msg.value, deadline);
            _sellTokenOnV3(token, v3Fee, IERC20(token).balanceOf(address(this)));
        } else {
            uint256 spend = _toERC20(msg.value);
            _buyTokenOnV3(token, v3Fee, spend);
            _sellTokenOnV2(token, IERC20(token).balanceOf(address(this)), deadline);
        }

        uint256 profit = address(this).balance - balBefore;
        require(profit >= minProfit, "profit < min");

        _sweepNative(owner, address(this).balance);
    }

    // ------------------------------------------------------------------------
    // V2 flash-swap callback
    // ------------------------------------------------------------------------

    function uniswapV2Call(
        address, /* sender */
        uint256 amount0,
        uint256 amount1,
        bytes calldata data
    ) external override {
        (address pair, address token, uint24 v3Fee, uint8 dir, uint256 minProfit, uint256 balBefore) =
            abi.decode(data, (address, address, uint24, uint8, uint256, uint256));
        require(msg.sender == pair, "!pair");

        if (dir == 1) {
            _dir1Callback(pair, token, v3Fee, amount0, amount1, balBefore, minProfit);
        } else {
            _dir2Callback(pair, token, v3Fee, amount0, amount1, balBefore, minProfit);
        }
    }

    // ------------------------------------------------------------------------
    // Internal — Direction 1: borrow token, repay USDT0
    // ------------------------------------------------------------------------

    function _dir1Callback(
        address pair,
        address token,
        uint24 v3Fee,
        uint256 amount0,
        uint256 amount1,
        uint256 balBefore,
        uint256 minProfit
    ) internal {
        uint256 borrowAmt = token == IUniswapV2Pair(pair).token0() ? amount0 : amount1;

        uint256 usdt0FromV3 = _sellTokenOnV3(token, v3Fee, borrowAmt);

        uint256 repayAmt = _v2RepayAmountOther(pair, borrowAmt, token == IUniswapV2Pair(pair).token0());
        require(usdt0FromV3 >= repayAmt, "V3 output < repay");

        require(IERC20(usdt0).transfer(pair, repayAmt), "USDT0 transfer");

        // Native balance reflects the shared USDT0 balance — no unwrap.
        require(address(this).balance >= balBefore + minProfit, "profit < min");
    }

    // ------------------------------------------------------------------------
    // Internal — Direction 2: borrow USDT0, repay token
    // ------------------------------------------------------------------------

    function _dir2Callback(
        address pair,
        address token,
        uint24 v3Fee,
        uint256 amount0,
        uint256 amount1,
        uint256 balBefore,
        uint256 minProfit
    ) internal {
        bool tokenIs0 = token == IUniswapV2Pair(pair).token0();
        uint256 borrowAmt = tokenIs0 ? amount1 : amount0;

        uint256 tokenFromV3 = _buyTokenOnV3(token, v3Fee, borrowAmt);

        uint256 repayAmt = _v2RepayAmountOther(pair, borrowAmt, !tokenIs0);
        require(tokenFromV3 >= repayAmt, "V3 output < repay");

        require(IERC20(token).transfer(pair, repayAmt), "token transfer");

        uint256 leftover = IERC20(token).balanceOf(address(this));
        if (leftover > 0) {
            _sellTokenOnV3(token, v3Fee, leftover);
        }

        require(address(this).balance >= balBefore + minProfit, "profit < min");
    }

    // ------------------------------------------------------------------------
    // Helpers — V2 math
    // ------------------------------------------------------------------------

    /// @notice ``getAmountIn`` for repaying a V2 pair with the *other* token.
    function _v2RepayAmountOther(
        address pair,
        uint256 amountOut,
        bool outIsToken0
    ) internal view returns (uint256) {
        (uint112 r0, uint112 r1,) = IUniswapV2Pair(pair).getReserves();
        (uint256 reserveIn, uint256 reserveOut) = outIsToken0
            ? (uint256(r1), uint256(r0))
            : (uint256(r0), uint256(r1));
        uint256 numerator = reserveIn * amountOut * 1000;
        uint256 denominator = (reserveOut - amountOut) * 997;
        return (numerator / denominator) + 1;
    }

    // ------------------------------------------------------------------------
    // Helpers — swap execution
    // ------------------------------------------------------------------------

    function _sellTokenOnV3(address token, uint24 fee, uint256 amountIn)
        internal
        returns (uint256 amountOut)
    {
        IERC20(token).approve(v3Router, amountIn);
        amountOut = IUniswapV3SwapRouter(v3Router).exactInputSingle(
            IUniswapV3SwapRouter.ExactInputSingleParams({
                tokenIn: token,
                tokenOut: usdt0,
                fee: fee,
                recipient: address(this),
                amountIn: amountIn,
                amountOutMinimum: 0,
                sqrtPriceLimitX96: 0
            })
        );
    }

    function _buyTokenOnV3(address token, uint24 fee, uint256 usdt0In)
        internal
        returns (uint256 amountOut)
    {
        IERC20(usdt0).approve(v3Router, usdt0In);
        amountOut = IUniswapV3SwapRouter(v3Router).exactInputSingle(
            IUniswapV3SwapRouter.ExactInputSingleParams({
                tokenIn: usdt0,
                tokenOut: token,
                fee: fee,
                recipient: address(this),
                amountIn: usdt0In,
                amountOutMinimum: 0,
                sqrtPriceLimitX96: 0
            })
        );
    }

    function _buyTokenOnV2(address token, uint256 usdt0In, uint256 deadline) internal {
        address[] memory path = new address[](2);
        path[0] = usdt0;
        path[1] = token;
        IUniswapV2Router02(v2Router).swapExactETHForTokens{value: usdt0In}(
            0, path, address(this), deadline
        );
    }

    function _sellTokenOnV2(address token, uint256 amountIn, uint256 deadline) internal {
        IERC20(token).approve(v2Router, amountIn);
        address[] memory path = new address[](2);
        path[0] = token;
        path[1] = usdt0;
        IUniswapV2Router02(v2Router).swapExactTokensForETH(
            amountIn, 0, path, address(this), deadline
        );
    }

    // ------------------------------------------------------------------------
    // Helpers — misc
    // ------------------------------------------------------------------------

    /// @notice Convert a native 18-decimal amount to ERC-20 6-decimal units.
    /// On Stable Chain the two representations share the same balance.
    function _toERC20(uint256 nativeAmount) internal pure returns (uint256) {
        return nativeAmount / 1e12;
    }

    function _flashOutAmounts(
        address pair,
        address token,
        uint8 dir,
        uint256 borrowAmt
    ) internal view returns (uint256 amount0Out, uint256 amount1Out) {
        address t0 = IUniswapV2Pair(pair).token0();
        if (dir == 1) {
            if (token == t0) amount0Out = borrowAmt;
            else amount1Out = borrowAmt;
        } else {
            if (usdt0 == t0) amount0Out = borrowAmt;
            else amount1Out = borrowAmt;
        }
    }

    function _sweepNative(address to, uint256 amount) internal {
        (bool ok,) = to.call{value: amount}("");
        require(ok, "sweep failed");
    }

    receive() external payable {}
}
