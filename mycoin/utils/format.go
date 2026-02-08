package utils

import (
	"fmt"
	"math/big"
)

func FormatTargetHex(target *big.Int) string {
	if target == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%064x", target)
}
func BigToCompact(n *big.Int) uint32 {
	if n.Sign() <= 0 {
		return 0
	}

	bytes := n.Bytes()
	lenBytes := len(bytes)
	var mantissa uint32
	var exponent int

	if lenBytes <= 3 {
		mantissa = uint32(n.Int64())
		mantissa <<= 8 * (3 - uint(lenBytes))
		exponent = 3
	} else {
		mantissa = uint32(bytes[0])<<16 | uint32(bytes[1])<<8 | uint32(bytes[2])
		exponent = lenBytes
	}

	// 處理負數標誌位（Compact 格式保留了符號位，若最高位為1需右移）
	if mantissa&0x00800000 != 0 {
		mantissa >>= 8
		exponent++
	}

	return (uint32(exponent) << 24) | mantissa
}
func CompactToBig(bits uint32) *big.Int {
	// 拆解 exponent 和 mantissa
	exponent := uint(bits >> 24)
	mantissa := bits & 0x007fffff

	var result *big.Int

	if exponent <= 3 {
		result = big.NewInt(int64(mantissa >> (8 * (3 - exponent))))
	} else {
		result = big.NewInt(int64(mantissa))
		result.Lsh(result, 8*(exponent-3))
	}

	// 處理負數標誌
	if bits&0x00800000 != 0 {
		result.Neg(result)
	}

	return result
}
