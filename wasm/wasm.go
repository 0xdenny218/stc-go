// Package wasm 把 WASM 模块包装成 stc 组件（M4，对应论文 §6.4 的
// 运行时引入/撤销路线）：模块实例化 = 引入，模块关闭 = 撤销。
// fiber 机制（依赖门控、惯性、精确回卷）对 WASM 组件与 Go 组件一视同仁。
//
// Guest ABI（刻意最小；值一律为字符串）：
//
//   - 模块不得使用 wasm start section（探针实例化会执行它）；
//     生命周期入口是导出的 start()/stop() 函数（均可选）。
//   - 导出的 _initialize()（reactor 模式工具链的运行时初始化，如
//     TinyGo -buildmode=c-shared）若存在，会在 start 之前调用一次。
//   - wasi_snapshot_preview1 始终实例化，guest 可以自由 import WASI
//     （TinyGo 等工具链无条件需要 fd_write/proc_exit 等）。
//   - 使用宿主函数的模块必须导出名为 "memory" 的内存。
//   - 宿主模块 "stc"：
//     provide(key_ptr,key_len,val_ptr,val_len) i32  — 提供字符串服务；0 成功
//     get_size(key_ptr,key_len) i32                 — 值长度；-1 不存在
//     get(key_ptr,key_len,buf_ptr,buf_len) i32      — 拷贝值；返回写入数，-1 失败
//     log(msg_ptr,msg_len)                          — 追加到 Runtime 日志
//
// 宿主调用经 start/stop 的调用上下文拿到 fiber 的 *stc.Context，
// 因此 start 内 provide 的服务全部登记在 fiber 自己的 context 上，
// 卸载时由 M1 的逆操作机制精确回卷——模块无需自己登记清理逻辑。
package wasm

import (
	stdctx "context"
	"errors"
	"fmt"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"github.com/0xdenny218/stc-go"
)

// fiberCtxKey 把 fiber 的 *stc.Context 挂到 start/stop 的调用上下文上，
// 宿主函数由此取出（wazero 宿主函数收到的是调用方的 context）。
type fiberCtxKey struct{}

// Runtime 封装 wazero 运行时与 stc 宿主模块。非并发装载安全：
// 所有导出方法都可从任意 goroutine 调用。
type Runtime struct {
	rt   wazero.Runtime
	conf wazero.ModuleConfig

	mu   sync.Mutex
	keys map[string]stc.Key
	logs []string
}

// NewRuntime 创建运行时并实例化 stc 宿主模块。
// 用解释器配置：guest 极小，解释器换来平台无关性与确定性。
func NewRuntime() (*Runtime, error) {
	r := &Runtime{
		rt:   wazero.NewRuntimeWithConfig(stdctx.Background(), wazero.NewRuntimeConfigInterpreter()),
		conf: wazero.NewModuleConfig(),
		keys: map[string]stc.Key{},
	}
	_, err := r.rt.NewHostModuleBuilder("stc").
		NewFunctionBuilder().WithFunc(r.hostProvide).Export("provide").
		NewFunctionBuilder().WithFunc(r.hostGetSize).Export("get_size").
		NewFunctionBuilder().WithFunc(r.hostGet).Export("get").
		NewFunctionBuilder().WithFunc(r.hostLog).Export("log").
		Instantiate(stdctx.Background())
	if err != nil {
		return nil, fmt.Errorf("wasm: host module: %w", err)
	}
	// WASI 始终可用：TinyGo 等工具链产出的模块无条件 import 若干
	// wasi_snapshot_preview1 函数（fd_write/proc_exit 等）。
	wasi_snapshot_preview1.MustInstantiate(stdctx.Background(), r.rt)
	return r, nil
}

// Close 关闭运行时及其中全部模块实例。
func (r *Runtime) Close() error { return r.rt.Close(stdctx.Background()) }

// Key 返回名为 name 的字符串类型服务键（表内单例），
// 供 Go 侧声明 Inject/Provide 或读取同一服务。
func (r *Runtime) Key(name string) stc.Key {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.keys[name]
	if !ok {
		k = stc.NewKey[string](name)
		r.keys[name] = k
	}
	return k
}

// Logs 返回宿主 log 调用累积的消息（测试观察点）。
func (r *Runtime) Logs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.logs...)
}

// Options 是 WASM 组件的静态描述。
type Options struct {
	Name    string
	Inject  []string // 依赖的服务名（字符串键）
	Provide []string // 声明性元数据；实际提供以 start 内的宿主调用为准
}

// Component 把 src（.wasm 二进制）包装成 stc.Component。
// Apply = 实例化 + 调用 start；Inverse = 调用 stop + 关闭模块实例。
// start 失败（含 trap）时模块立即关闭并作为装载错误上报（fiber → Failed）。
func (r *Runtime) Component(src []byte, opts Options) stc.Component {
	inject := make([]stc.Key, len(opts.Inject))
	for i, n := range opts.Inject {
		inject[i] = r.Key(n)
	}
	provide := make([]stc.Key, len(opts.Provide))
	for i, n := range opts.Provide {
		provide[i] = r.Key(n)
	}
	return stc.Component{
		Name:    opts.Name,
		Inject:  inject,
		Provide: provide,
		Apply: func(c *stc.Context) (stc.Inverse, error) {
			callCtx := stdctx.WithValue(stdctx.Background(), fiberCtxKey{}, c)
			mod, err := r.rt.InstantiateWithConfig(callCtx, src, r.conf)
			if err != nil {
				return nil, fmt.Errorf("wasm: instantiate %s: %w", opts.Name, err)
			}
			// _initialize 是 reactor 模式工具链（如 TinyGo -buildmode=c-shared）
			// 的运行时初始化入口，按约定必须先于其他导出函数调用。
			// 它与 start 同属启动序列：失败（含 trap）同样关闭模块并上报装载错误。
			for _, entry := range []string{"_initialize", "start"} {
				if fn := mod.ExportedFunction(entry); fn != nil {
					if _, err := fn.Call(callCtx); err != nil {
						_ = mod.Close(stdctx.Background())
						return nil, fmt.Errorf("wasm: %s %s: %w", entry, opts.Name, err)
					}
				}
			}
			return func() error {
				var err error
				if stop := mod.ExportedFunction("stop"); stop != nil {
					_, err = stop.Call(callCtx)
				}
				return errors.Join(err, mod.Close(stdctx.Background()))
			}, nil
		},
	}
}

// probe 编译并试实例化 src（不调用 start）：
// 捕获二进制错误、导入不匹配等实例化级失败。
func (r *Runtime) probe(src []byte) error {
	mod, err := r.rt.InstantiateWithConfig(stdctx.Background(), src, r.conf)
	if err != nil {
		return err
	}
	return mod.Close(stdctx.Background())
}

// Handle 是已装载 WASM 组件的管理句柄，提供带回滚的版本更新。
type Handle struct {
	rt   *Runtime
	home *stc.Context
	opts Options

	mu    sync.Mutex
	src   []byte
	fiber *stc.Fiber
}

// Load 探针校验后装载 WASM 组件，并等待其到达稳定态
// （Active 或 Failed——依赖未满足时阻塞在 Pending，用 ctx 控制超时）。
func Load(ctx stdctx.Context, c *stc.Context, rt *Runtime, src []byte, opts Options) (*Handle, error) {
	if err := rt.probe(src); err != nil {
		return nil, fmt.Errorf("wasm: probe %s: %w", opts.Name, err)
	}
	f := c.Load(rt.Component(src, opts))
	if err := f.Ready(ctx); err != nil {
		return nil, fmt.Errorf("wasm: load %s: %w", opts.Name, err)
	}
	return &Handle{rt: rt, home: c, opts: opts, src: src, fiber: f}, nil
}

// Fiber 返回当前版本对应的 fiber（每次成功 Update 后更换）。
func (h *Handle) Fiber() *stc.Fiber {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fiber
}

// Update 原子地换到新版本：
//
//  1. 探针失败（编译/实例化级错误）→ 直接返回错误，旧版本原样保留；
//  2. 探针通过 → 撤退旧 fiber（等待 Gone）后装载新版本；
//  3. 新版本 start 期失败 → 用保留的旧字节回滚装载。
//
// 回滚本身失败时返回组合错误，此时句柄处于无 fiber 状态。
func (h *Handle) Update(ctx stdctx.Context, src []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if err := h.rt.probe(src); err != nil {
		return fmt.Errorf("wasm: update probe failed, old version intact: %w", err)
	}
	old := h.fiber
	old.Dispose()
	if err := old.Gone(ctx); err != nil {
		return fmt.Errorf("wasm: update: waiting old version gone: %w", err)
	}
	nf := h.home.Load(h.rt.Component(src, h.opts))
	if err := nf.Ready(ctx); err != nil {
		rb := h.home.Load(h.rt.Component(h.src, h.opts))
		if rbErr := rb.Ready(ctx); rbErr != nil {
			return fmt.Errorf("wasm: update failed (%v); rollback also failed: %w", err, rbErr)
		}
		h.fiber = rb
		return fmt.Errorf("wasm: update failed, rolled back to previous version: %w", err)
	}
	h.src = src
	h.fiber = nf
	return nil
}

// Dispose 撤退当前版本的 fiber（幂等由 fiber 层保证）。
func (h *Handle) Dispose() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fiber.Dispose()
}

// ------------------------------------------------------------------
// 宿主函数
// ------------------------------------------------------------------

func (r *Runtime) fiberCtx(callCtx stdctx.Context) *stc.Context {
	c, _ := callCtx.Value(fiberCtxKey{}).(*stc.Context)
	return c
}

func readStr(m api.Module, ptr, ln uint32) (string, bool) {
	b, ok := m.Memory().Read(ptr, ln)
	if !ok {
		return "", false
	}
	return string(b), true
}

func (r *Runtime) hostProvide(callCtx stdctx.Context, m api.Module, keyPtr, keyLen, valPtr, valLen uint32) uint32 {
	c := r.fiberCtx(callCtx)
	if c == nil {
		return 1
	}
	key, ok1 := readStr(m, keyPtr, keyLen)
	val, ok2 := readStr(m, valPtr, valLen)
	if !ok1 || !ok2 {
		return 1
	}
	if _, err := c.Provide(r.Key(key), val); err != nil {
		return 1
	}
	return 0
}

func (r *Runtime) hostGetSize(callCtx stdctx.Context, m api.Module, keyPtr, keyLen uint32) int32 {
	c := r.fiberCtx(callCtx)
	if c == nil {
		return -1
	}
	key, ok := readStr(m, keyPtr, keyLen)
	if !ok {
		return -1
	}
	v, err := stc.Service[string](c, r.Key(key))
	if err != nil {
		return -1
	}
	return int32(len(v))
}

func (r *Runtime) hostGet(callCtx stdctx.Context, m api.Module, keyPtr, keyLen, bufPtr, bufLen uint32) int32 {
	c := r.fiberCtx(callCtx)
	if c == nil {
		return -1
	}
	key, ok := readStr(m, keyPtr, keyLen)
	if !ok {
		return -1
	}
	v, err := stc.Service[string](c, r.Key(key))
	if err != nil || len(v) > int(bufLen) {
		return -1
	}
	if !m.Memory().Write(bufPtr, []byte(v)) {
		return -1
	}
	return int32(len(v))
}

func (r *Runtime) hostLog(_ stdctx.Context, m api.Module, msgPtr, msgLen uint32) {
	if s, ok := readStr(m, msgPtr, msgLen); ok {
		r.mu.Lock()
		r.logs = append(r.logs, s)
		r.mu.Unlock()
	}
}
