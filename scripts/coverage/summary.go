package main

import (
	"fmt"
	"math/big"
)

// counts represents executable lines, not statements or an average of percentages.
type counts struct {
	Covered int64 `json:"covered"`
	Total   int64 `json:"total"`
}

func (c counts) validate() error {
	if c.Total <= 0 || c.Covered < 0 || c.Covered > c.Total {
		return fmt.Errorf("invalid coverage counts: %d/%d", c.Covered, c.Total)
	}
	return nil
}

// passes uses exact arithmetic: a result of exactly 80 percent must fail.
func (c counts) passes() bool {
	if c.validate() != nil {
		return false
	}
	covered := new(big.Int).Mul(big.NewInt(c.Covered), big.NewInt(5))
	total := new(big.Int).Mul(big.NewInt(c.Total), big.NewInt(4))
	return covered.Cmp(total) > 0
}

func aggregate(modules map[string]counts) (counts, error) {
	var result counts
	for name, c := range modules {
		if err := c.validate(); err != nil {
			return counts{}, fmt.Errorf("%s: %w", name, err)
		}
		if result.Total > int64(^uint64(0)>>1)-c.Total || result.Covered > int64(^uint64(0)>>1)-c.Covered {
			return counts{}, fmt.Errorf("coverage counts overflow")
		}
		result.Covered += c.Covered
		result.Total += c.Total
	}
	if err := result.validate(); err != nil {
		return counts{}, err
	}
	return result, nil
}
