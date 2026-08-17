# stc-go

[![CI](https://github.com/0xdenny218/stc-go/actions/workflows/ci.yml/badge.svg)](https://github.com/0xdenny218/stc-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/0xdenny218/stc-go.svg)](https://pkg.go.dev/github.com/0xdenny218/stc-go)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

**A Go implementation of the spatiotemporal composability paradigm** — the
programming model behind [Cordis](https://github.com/cordiverse/cordis)
(TypeScript) and [DeepSeek Harness (dsh)](https://github.com/deepseek-ai/deepseek-harness),
DeepSeek's "everything is a plugin" agent harness.

stc-go is specified directly by the paper
[*A Programming Paradigm for Spatiotemporal Composability*](https://github.com/cordiverse/paper)
(pinned at `948a07b`, 2026-08-14 draft) — it is **not a port of Cordis**. Cordis
(pinned at `8cc9e33`) serves only as a reference and a source of test scenarios.
The acceptance criteria are the paper's five metatheory theorems, implemented as
property-based tests.

> 中文：[时空可组合性范式](#中文简介)的 Go 实现——论文为规格、五定理为验收，非 Cordis 移植。

## The paradigm at a glance

- **Temporal composability** — every side effect a component registers carries
  an inverse; unloading a component rewinds its effects in exact LIFO order
  (revertible effects).
- **Spatial composability** — components declare their dependencies (inject);
  the runtime reactively tracks satisfaction and loss of those dependencies,
  moving each fiber between Pending/Loading/Active/Unloading
  (reactive coeffects).
- Both dimensions unify in a single **context** type: a context is both a
  service container and an effect accumulator.

This is what lets a plugin host hot-reload a component — Go or WASM — with
provable cleanup: no leaked subscriptions, no stale services, no dangling
state, and dependent components reload automatically.

## Install

```sh
go get github.com/0xdenny218/stc-go
```

WASM component loading (optional) lives in the subpackage:

```go
import "github.com/0xdenny218/stc-go/wasm"
```

## Quick start

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

	// Loaded first, but stays Pending: its dependency isn't satisfied yet.
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

	// Provide registers its own inverse; unloading rewinds it automatically.
	root.Load(stc.Component{
		Name:    "provider",
		Provide: []stc.Key{greeting},
		Apply: func(c *stc.Context) (stc.Inverse, error) {
			_, err := c.Provide(greeting, "hello, spatiotemporal world")
			return nil, err
		},
	})

	// Becomes Active only after greeting is provided (Theorem: Ordering).
	if err := consumer.Ready(context.Background()); err != nil {
		panic(err)
	}
}
```

## Theory mapping (paper §5.1, Table 2)

| Paper construct | Symbol | stc-go |
|---|---|---|
| context (first-class) | Γ∞ | `Context` (scoped tree, `New`/`Child`) |
| revertible effect | e ∈ 𝔈Γ | `Context.Effect(install)` |
| context read/write | get(k) / set(k,v) | `Context.Get` / `Context.Set` |
| service provision | provide | `Context.Provide` (auto-registers undo) |
| coeffect read | d (inject) | `Component.Inject` + `stc.Service[T]` |
| isolation | isolate(k, r) | `Context.Isolate(key, realm)` |
| interception | intercept(k, ν) | `Context.Intercept(key, meta)` |
| component instance | ⟨d,p,e,π,σ,τ,θ⟩ | `Fiber` (`Load` creates, `Dispose` withdraws) |
| registry | dom(Fγ) | fiber registry on the root `Context` |
| fiber state | τ | `Pending → Loading → Active → Unloading → (Pending \| Failed)`; explicit `Dispose` → `Gone` |

## Lifecycle contracts (the important bits)

- **`Close` is root-only**: it shuts the orchestrator down and rewinds the
  whole tree. For subtree-only cleanup on a non-root scope, use `Release`.
- **Same-key replacement must wait for `Gone`**: before reloading a provider of
  an already-provided key, `Dispose` the old one and wait for its `Gone` to
  return. Overlapping duplicates are rejected with `ErrDuplicateProvide`
  (fail-fast enforcement of the paper's Def. 58 well-formedness).
- **`Fiber.Context()` returns the current load cycle's context**; inertial
  reloads replace it, so observing a previous (already-rewound) cycle's
  context is a legal race outcome.
- **`Gone()` returns once the fiber leaves the registry** (Gone or Failed);
  **`Ready()`** returns on Active / Failed / Gone (nil / load error /
  `ErrDisposed` respectively).

## Verification = the five metatheory theorems (paper §4.4)

`property_test.go` turns each theorem into a property-based test:

| Theorem | Property |
|---|---|
| T59 Preservation | registry well-formedness holds after every operation |
| T61 Recovery exactness | state after unwinding a fiber ≡ state where it never loaded |
| T63 Ordering | a fiber enters Loading only after its dependencies are ready |
| T66 Progress | quiescence within a bounded number of orchestrator steps |
| T73 Confluence | quiescent end state is independent of schedule order (`-race` + randomized schedules) |

```sh
go test -race ./...
go test -run Property -fuzz FuzzInterleaving -fuzztime 10s ./...
```

## WASM components (`stc-go/wasm`)

Module instantiation = introduction, module close = withdrawal (the paper's
§6.4 runtime-code route): dependency gating, inertia locks and exact rewinding
apply to WASM guests exactly as to Go components.

- `wasm.Runtime` wraps [wazero](https://github.com/tetratelabs/wazero)
  (interpreter config, platform-independent) plus an `stc` host module. Guests
  participate via exported `start()`/`stop()`; host functions
  `provide/get/get_size/log` register services on the fiber's own context —
  rewind on unload is guaranteed by the core mechanism, guests register no
  cleanup themselves.
- `wasm.Load` probes (compile + trial instantiation) before loading;
  `Handle.Update` performs atomic hot-swap: probe failure leaves the old
  version intact, a start trap rolls back to the old bytes automatically.
- Acceptance (`wasm/wasm_test.go`): the three HMR contracts (reload,
  cross-boundary dependency chain, failure rollback) + spec Test/WasmRollback
  + T61 cross-boundary unload exactness.
- Test guests are hand-encoded WASM binaries (a tiny encoder in
  `guest_test.go`) — zero toolchain dependencies.

## Relation to Cordis and DeepSeek Harness

The spatiotemporal composability paradigm has one specification (the paper)
and several implementations:

| Project | Language | Role |
|---|---|---|
| [cordiverse/paper](https://github.com/cordiverse/paper) | — | the specification (preprint, actively revised) |
| [cordiverse/cordis](https://github.com/cordiverse/cordis) | TypeScript | reference implementation; drives the [Koishi](https://koishi.chat) plugin ecosystem |
| [deepseek-ai/deepseek-harness](https://github.com/deepseek-ai/deepseek-harness) | TypeScript | DeepSeek's agent harness (dsh), "everything is a plugin", driven by Cordis |
| **stc-go** (this repo) | Go | independent implementation; paper as spec, five theorems as acceptance |

If you are building a plugin system, an agent harness, or a hot-reload host in
Go and want the same composition guarantees that Cordis gives the TypeScript
world (dependency-gated loading, exact effect rewind, reactive reload of
dependents), stc-go is that library.

Not ported from Cordis (by design): the four event-dispatch modes
(`emit/parallel/serial/bail/waterfall`), `ctx.plugin()` with config schemas,
and the `hmr`/`loader` satellite packages are Cordis ecosystem concerns, not
paradigm core. Go replacements are idiomatic: explicit typed accessors instead
of `Proxy` + declaration merging, static component registration instead of the
Go plugin package (which cannot unload), and WASM for runtime-loaded code.

## Documented deviations from the paper / Cordis

- No `Proxy`: coeffect access goes through the explicit generic
  `stc.Service[T]` (the compile-time route the paper's §6.4 endorses).
- Concurrency model: a single RWMutex plus a central orchestrator goroutine
  serializes fiber transitions; `Apply`/inverses run outside the lock in their
  own goroutines (the paper does not prescribe a concurrency model).
- Nested child fibers do not cascade-dispose with their parent (a scoped
  narrowing, documented in the project spec).
- Duplicate provide of the same key is excluded from the confluence guarantee
  (matching the theorems' conditional statements).
- Effect accumulation happens at registration (return values of
  `Effect`/`Apply`); the paper's iterator-style continuous yield is not
  implemented — no acceptance scenario depends on it.

## 中文简介

stc-go 是时空可组合性范式的 Go 实现。该范式由论文
《A Programming Paradigm for Spatiotemporal Composability》定义，
TypeScript 侧的实现是 Cordis（Koishi 生态的底座），DeepSeek 的 agent
harness（DeepSeek Harness / dsh，"一切皆插件"）即建立在 Cordis 之上。

- **时间可组合性**：组件注册的每个副作用都携带逆操作，卸载时按 LIFO 逆序
  精确回卷；
- **空间可组合性**：组件声明依赖，运行时响应式地管理依赖的满足与失效，
  依赖变化时 fiber 自动重载；
- **验收即定理**：论文 §4.4 的五条元理论定理逐条落成 property-based 测试
  （`-race` + 随机调度 + fuzz）；
- **WASM 组件**（`stc-go/wasm`）：基于 wazero，模块实例化=引入、关闭=撤销，
  `Handle.Update` 原子换血，失败自动回滚旧版本。

本项目以论文为唯一规格，**不是** Cordis 的移植；Cordis 仅作语义参考与
测试语料库。详见上文英文文档。

## License

[MIT](LICENSE)
