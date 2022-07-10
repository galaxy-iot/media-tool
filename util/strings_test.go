package util

import (
	"reflect"
	"testing"
)

func TestStringBytesConver(t *testing.T) {
	testCases := map[string][]byte{
		"testcase": []byte("testcase"),
		"dsa-2.12": []byte("dsa-2.12"),
	}

	for str, bytes := range testCases {
		convertedBytes := String2Bytes(str)
		if !reflect.DeepEqual(convertedBytes, bytes) {
			t.Errorf("test failed: String2Bytes %v", str)
			return
		}

		convertedString := Bytes2String(bytes)
		if convertedString != str {
			t.Errorf("test failed: Bytes2String %v", bytes)
			return
		}
	}
}
