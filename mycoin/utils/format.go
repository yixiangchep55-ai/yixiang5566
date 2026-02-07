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
