package main

import "github.com/vmihailenco/msgpack/v5"

func decodeMsgpack(data []byte, v any) error {
	return msgpack.Unmarshal(data, v)
}
