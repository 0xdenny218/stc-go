# stc-go

时空可组合性范式（spatiotemporal composability）的 Go 实现。

以论文 [*A Programming Paradigm for Spatiotemporal Composability*][paper]
为唯一规格：**不是** Cordis 的移植——Cordis（TypeScript）是同一范式的另一种
实现，仅作为测试场景的语料来源。项目规格与里程碑见
[jiangyu-wiki `stc-go/specs/project.spec.md`][spec]。

[-paper 钉定 `948a07b` · cordis 参考 `8cc9e33` · 2026-08-14]

[paper]: https://github.com/cordiverse/paper
[spec]: https://github.com/jiangyu/jiangyu-wiki/blob/main/stc-go/specs/project.spec.md

## 范式一瞥

- **时间可组合性**：组件装载时注册的每个副作用都携带逆操作，卸载时按
  LIFO 逆序精确回卷（revertible effects）。
- **空间可组合性**：组件声明依赖（inject），运行时响应式地管理依赖的满足与
  失效，fiber 据此在 Pending/Loading/Active/Unloading 之间转移
  （reactive coeffects）。
- 两者统一在单一 **context** 类型上：context 是服务容器，也是副作用累加器。

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

- `Close` 仅限根 context：关停 orchestrator 并回卷整棵树；非根作用域的
  子树清理用 `Release`（不动系统）。
- 同键服务换血必须等旧提供者完全撤退（`Dispose` 后 `Gone` 返回）再装载
  新提供者；重叠窗口内的重复提供被 `ErrDuplicateProvide` 拒绝
  （论文 Def.58 良构性的 fail-fast 强制）。
- `Fiber.Context()` 返回当前装载周期的 context；惯性重载会更换它，
  读到上一周期（已回卷）的 context 是合法的竞态结果。
- `Gone()` 在 fiber 出册（Gone 或 Failed）时返回；`Ready()` 在 Active/
  Failed/Gone 时返回（分别对应 nil / 装载错误 / `ErrDisposed`）。

## 验收 = 五条元理论定理（论文 §4.4）

`property_test.go` 逐条落实为 property-based 测试：

| 定理 | 性质 |
|---|---|
| T59 Preservation | 任意操作后注册表良构不变量保持 |
| T61 Recovery exactness | 撤销 fiber 后的状态 ≡ 其从未装载的状态 |
| T63 Ordering | fiber 仅在依赖就绪后进入 Loading |
| T66 Progress | 有界步数内达到静默 |
| T73 Confluence | 静默终态与调度顺序无关（`-race` + 随机调度） |

```sh
go test -race ./...
go test -run Property -fuzz FuzzInterleaving -fuzztime 10s ./...
```

## M4：WASM 组件装载（`stc/wasm`）

模块实例化 = 引入，模块关闭 = 撤销（论文 §6.4 的运行时代码路线）：
fiber 的依赖门控、惯性锁、精确回卷对 WASM 组件与 Go 组件一视同仁。

- `wasm.Runtime` 封装 wazero（解释器配置，平台无关）与 `stc` 宿主模块；
  guest 经导出函数 `start()/stop()` 参与生命周期，宿主函数
  `provide/get/get_size/log` 在 fiber 自己的 context 上登记服务——
  卸载回卷由 M1 机制保证，guest 无需自登记清理。
- `wasm.Load` 先探针（编译+试实例化）再装载；`Handle.Update` 实现
  原子换血：探针失败旧版本原样保留，start trap 自动用旧字节回滚。
- 验收（`wasm/wasm_test.go`）：HMR 三契约（重载、跨边界依赖链、
  失败回滚）+ 规格 Test/WasmRollback + T61 跨边界卸载精确性。
- 测试 guest 为手写 WASM 二进制（`guest_test.go` 的微型编码器），
  零工具链依赖。

## 与论文/Cordis 的已记录偏差

- 无 Proxy：协效应访问走显式泛型 `stc.Service[T]`（论文 §6.4 认可的编译期路线）。
- 并发模型：单一 RWMutex + 中心 orchestrator goroutine 串行化 fiber 转移；
  Apply/逆操作在锁外 goroutine 中运行（论文不规定并发模型，此为 D3 决策）。
- 嵌套子 fiber 不随父 fiber 级联卸载（D7 收窄）。
- 同 key 重复 provide 排除在汇合保证之外（D7，对应定理条件）。
- 效应累加发生在注册点（Effect/Apply 返回值），未实现论文迭代器式的
  持续 yield——验收场景未依赖，列为后续扩展。

## License

MIT
