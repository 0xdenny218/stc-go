//go:build wasm

package guest

import (
	"errors"
	"unsafe"
)

// 宿主模块 "stc" 的导入函数（ABI 见 stc-go/wasm 包文档；值一律为字符串）。

//go:wasmimport stc provide
func hostProvide(keyPtr, keyLen, valPtr, valLen uint32) int32

//go:wasmimport stc get_size
func hostGetSize(keyPtr, keyLen uint32) int32

//go:wasmimport stc get
func hostGet(keyPtr, keyLen, bufPtr, bufLen uint32) int32

//go:wasmimport stc log
func hostLog(msgPtr, msgLen uint32)

// ErrProvide 是宿主拒绝服务登记时返回的错误（如重复提供）。
var ErrProvide = errors.New("stc-guest: provide rejected by host")

func strarg(s string) (uint32, uint32) {
	if len(s) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(unsafe.StringData(s)))), uint32(len(s))
}

// Provide 以 key 提供字符串服务。fiber 卸载时该提供被自动撤销，
// 无需（也不应）在 stop 里手动抵消。
func Provide(key, value string) error {
	kp, kl := strarg(key)
	vp, vl := strarg(value)
	if hostProvide(kp, kl, vp, vl) != 0 {
		return ErrProvide
	}
	return nil
}

// Get 读取 key 的字符串值；第二个返回值报告 key 是否存在。
func Get(key string) (string, bool) {
	kp, kl := strarg(key)
	n := hostGetSize(kp, kl)
	if n < 0 {
		return "", false
	}
	buf := make([]byte, n)
	w := hostGet(kp, kl, uint32(uintptr(unsafe.Pointer(unsafe.SliceData(buf)))), uint32(len(buf)))
	if w < 0 {
		return "", false
	}
	return string(buf[:w]), true
}

// MustGet 同 Get，key 不存在时 panic（依赖缺失本该由 Inject 门控挡住）。
func MustGet(key string) string {
	v, ok := Get(key)
	if !ok {
		panic("stc-guest: missing service " + key)
	}
	return v
}

// Log 追加一条消息到宿主 Runtime 的日志（host 侧经 Runtime.Logs 观察）。
func Log(msg string) {
	p, l := strarg(msg)
	hostLog(p, l)
}
