# stc-go

[English](README.md) | **简体中文**

[![CI](https://github.com/0xdenny218/stc-go/actions/workflows/ci.yml/badge.svg)](https://github.com/0xdenny218/stc-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/0xdenny218/stc-go.svg)](https://pkg.go.dev/github.com/0xdenny218/stc-go)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

**时空可组合性范式（spatiotemporal composability）的 Go 实现** —— 即
[Cordis](https://github.com/cordiverse/cordis)（TypeScript）与
[DeepSeek Harness（dsh）](https://github.com/deepseek-ai/deepseek-harness)
（DeepSeek「一切皆插件」的 agent harness）背后的编程模型。

stc-go 以论文
[*A Programming Paradigm for Spatiotemporal Composability*](https://github.com/cordiverse/paper)
（钉定 `948a07b`，2026-08-14 草稿）为唯一规格——**不是** Cordis 的移植。
Cordis（钉定 `8cc9e33`）仅作语义参考与测试场景语料库。验收标准是论文
§4.4 的五条元理论定理，逐条落成 property-based 测试。

## 范式一瞥

- **时间可组合性**：组件装载时注册的每个副作用都携带逆操作，卸载时按
  LIFO 逆序精确回卷（revertible effects）。
- **空间可组合性**：组件声明依赖（inject），运行时响应式地管理依赖的
  满足与失效，fiber 据此在 Pending/Loading/Active/Unloading 之间转移
  （reactive coeffects）。
- 两者统一在单一 **context** 类型上：context 既是服务容器，也是副作用
  累加器。

这让插件宿主能够热重载组件（Go 或 WASM）并获得可证明的清理保证：
无泄漏订阅、无残留服务、无悬挂状态，且依赖方组件自动级联重载。

## 安装

```sh
go get github.com/0xdenny218/stc-go
```

WASM 组件装载（可选）在子包中：

```go
import "github.com/0xdenny218/stc-go/wasm"
```

## 快速上手

```go
package main

import (
	"context"
	"fmt"

	stc "github.com/0xdenny218/stc-go"
)

var greeting = stc.NewKey[string]("greeting")

func main() {
	root := stc.New()
	defer root.Close()

	// 先装载的消费者停在 Pending：它的依赖尚未满足。
	consumer := root.Load(stc.Component{
		Name:   "consumer",
		Inject: []stc.Key{greeting},
		Apply: func(c *stc.Context) (stc.Inverse, error) {
			msg, err := stc.Service[string](c, greeting)
			if err != nil {
				return nil, err
			}
			fmt.Println("consumer saw:", msg)
			return nil, nil
		},
	})

	// Provide 自动注册撤销效应，卸载时自动回卷。
	root.Load(stc.Component{
		Name:    "provider",
		Provide: []stc.Key{greeting},
		Apply: func(c *stc.Context) (stc.Inverse, error) {
			_, err := c.Provide(greeting, "hello, spatiotemporal world")
			return nil, err
		},
	})

	// 仅在 greeting 被提供后转为 Active（定理：Ordering）。
	if err := consumer.Ready(context.Background()); err != nil {
		panic(err)
	}
}
```

## 理论对应表（论文 §5.1 Table 2）

| 论文构造 | 符号 | stc-go |
|---|---|---|
| context（第一类上下文） | Γ∞ | `Context`（树状作用域，`New`/`Child`） |
| 可逆效应 | e ∈ 𝔈Γ | `Context.Effect(install)` |
| 上下文读写 | get(k) / set(k,v) | `Context.Get` / `Context.Set` |
| 服务提供 | provide | `Context.Provide`（自动注册撤销效应） |
| 协效应读取 | d（inject） | `Component.Inject` + `stc.Service[T]` |
| 隔离 | isolate(k, r) | `Context.Isolate(key, realm)` |
| 拦截 | intercept(k, ν) | `Context.Intercept(key, meta)` |
| 组件实例 | ⟨d,p,e,π,σ,τ,θ⟩ | `Fiber`（`Load` 创建，`Dispose` 撤退） |
| 注册表 | dom(Fγ) | root `Context` 上的 fiber 注册表 |
| fiber 状态 | τ | `Pending → Loading → Active → Unloading → (Pending \| Failed)`；显式 `Dispose` → `Gone` |

## 生命周期契约要点

- **`Close` 仅限根 context**：关停 orchestrator 并回卷整棵树；非根作用域的
  子树清理用 `Release`（不动系统）。
- **同键服务换血必须等旧提供者完全撤退**（`Dispose` 后 `Gone` 返回）再装载
  新提供者；重叠窗口内的重复提供被 `ErrDuplicateProvide` 拒绝
  （论文 Def.58 良构性的 fail-fast 强制）。
- **`Fiber.Context()` 返回当前装载周期的 context**；惯性重载会更换它，
  读到上一周期（已回卷）的 context 是合法的竞态结果。
- **`Gone()` 在 fiber 出册（Gone 或 Failed）时返回**；**`Ready()`** 在
  Active / Failed / Gone 时返回（分别对应 nil / 装载错误 / `ErrDisposed`）。

## 验收 = 五条元理论定理（论文 §4.4）

`property_test.go` 逐条落实为 property-based 测试：

| 定理 | 性质 |
|---|---|
| T59 Preservation | 任意操作后注册表良构不变量保持 |
| T61 Recovery exactness | 撤销 fiber 后的状态 ≡ 其从未装载的状态 |
| T63 Ordering | fiber 仅在依赖就绪后进入 Loading |
| T66 Progress | 有界 orchestrator 步数内达到静默 |
| T73 Confluence | 静默终态与调度顺序无关（`-race` + 随机调度） |

```sh
go test -race ./...
go test -run Property -fuzz FuzzInterleaving -fuzztime 10s ./...
```

## WASM 组件装载（`stc-go/wasm`）

模块实例化 = 引入，模块关闭 = 撤销（论文 §6.4 的运行时代码路线）：
fiber 的依赖门控、惯性锁、精确回卷对 WASM 组件与 Go 组件一视同仁。

- `wasm.Runtime` 封装 [wazero](https://github.com/tetratelabs/wazero)
  （解释器配置，平台无关）与 `stc` 宿主模块；guest 经导出函数
  `start()/stop()` 参与生命周期，宿主函数 `provide/get/get_size/log`
  在 fiber 自己的 context 上登记服务——卸载回卷由核心机制保证，
  guest 无需自登记清理。
- `wasm.Load` 先探针（编译+试实例化）再装载；`Handle.Update` 实现
  原子换血：探针失败旧版本原样保留，start trap 自动用旧字节回滚。
- 验收（`wasm/wasm_test.go`）：HMR 三契约（重载、跨边界依赖链、
  失败回滚）+ 规格 Test/WasmRollback + T61 跨边界卸载精确性。
- 测试 guest 为手写 WASM 二进制（`guest_test.go` 的微型编码器），
  零工具链依赖。

## 与 Cordis / DeepSeek Harness 的关系

时空可组合性范式只有一份规格（论文），有多个实现：

| 项目 | 语言 | 角色 |
|---|---|---|
| [cordiverse/paper](https://github.com/cordiverse/paper) | — | 规格（preprint，活跃修订中） |
| [cordiverse/cordis](https://github.com/cordiverse/cordis) | TypeScript | 参考实现；驱动 [Koishi](https://koishi.chat) 插件生态 |
| [deepseek-ai/deepseek-harness](https://github.com/deepseek-ai/deepseek-harness) | TypeScript | DeepSeek 的 agent harness（dsh），「一切皆插件」，由 Cordis 驱动 |
| **stc-go**（本仓库） | Go | 独立实现；论文为规格、五定理为验收 |

如果你在 Go 里构建插件系统、agent harness 或热重载宿主，想要 Cordis
在 TypeScript 世界提供的同等组合保证（依赖门控装载、精确效应回卷、
依赖方响应式重载），stc-go 就是这个库。

刻意不从 Cordis 移植的内容：四种事件派发模式
（`emit/parallel/serial/bail/waterfall`）、带配置 schema 的
`ctx.plugin()`、`hmr`/`loader` 卫星包——它们是 Cordis 生态关切，
不是范式核心。Go 侧的替代均按惯用法设计：显式类型化访问器替代
`Proxy` + declaration merging；静态组件注册替代 Go plugin 包
（无法卸载）；运行时装载的代码走 WASM。

## 与论文/Cordis 的已记录偏差

- 无 `Proxy`：协效应访问走显式泛型 `stc.Service[T]`（论文 §6.4 认可的
  编译期路线）。
- 并发模型：单一 RWMutex + 中心 orchestrator goroutine 串行化 fiber
  转移；`Apply`/逆操作在锁外 goroutine 中运行（论文不规定并发模型）。
- 嵌套子 fiber 不随父 fiber 级联卸载（收窄，见项目规格）。
- 同 key 重复 provide 排除在汇合保证之外（对应定理的条件式表述）。
- 效应累加发生在注册点（`Effect`/`Apply` 返回值），未实现论文迭代器式
  的持续 yield——验收场景未依赖，列为后续扩展。

## License

[MIT](LICENSE)
