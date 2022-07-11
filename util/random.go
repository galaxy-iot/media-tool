package util

import (
	"math/rand"
	"time"
)

func Random32() uint32 {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	random16Upper := r.Int31() & 0x00FFFF00
	random16Lower := r.Int31() & 0x00FFFF00
	return uint32((random16Upper << 8) | (random16Lower >> 8))
}

func RandomIntn(n int) int {
	return int(Random32() & (uint32(n) - 1))
}
