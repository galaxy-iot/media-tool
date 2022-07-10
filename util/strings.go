package util

import "unsafe"

func Bytes2String(bytes []byte) string {
	return *(*string)(unsafe.Pointer(&bytes))
}

func String2Bytes(str string) []byte {
	x := (*[2]uintptr)(unsafe.Pointer(&str))
	b := [3]uintptr{x[0], x[1], x[1]}
	return *(*[]byte)(unsafe.Pointer(&b))
}
