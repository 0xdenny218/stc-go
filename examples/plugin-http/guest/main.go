//go:build wasm

// stc-go WASM guest 示例：start 里提供一个字符串服务，stop 里记一条日志。
// 构建：在 examples/plugin-http 下执行 make wasm（需要 TinyGo）。
package main

import "github.com/0xdenny218/stc-go/guest"

//export start
func start() {
	if err := guest.Provide("wasm-message", "hello from TinyGo guest v3"); err != nil {
		panic(err)
	}
}

//export stop
func stop() {
	guest.Log("guest stopped")
}

// reactor 模式（-buildmode=c-shared）不调用 main；入口是 start/stop。
func main() {}
