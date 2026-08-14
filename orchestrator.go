package stc

import (
	"errors"
	"log"
	"sort"
	"sync"
	"sync/atomic"
)

// orchestrator 是 fiber 生命周期转移的唯一决策者（D3）：
// 单 goroutine 事件循环串行处理全部转移；Apply 与逆操作在锁外
// 的独立 goroutine 中运行，其完成以事件回流，循环本身从不阻塞。
// 这正是论文 §4 中 orchestrator 与 fiber 迭代器交错的运行时形态，
// 也使嵌套 Load（apply 内再 Load 子组件并等待）不会死锁。
type orchestrator struct {
	sh    *shared
	inbox chan cmd
	done  chan struct{}

	cmds     atomic.Int64
	stopping atomic.Bool
	stopOnce sync.Once
}

type cmd interface{}

type (
	cmdLoad    struct{ f *Fiber }
	cmdDispose struct{ f *Fiber }
	cmdApplied struct {
		f   *Fiber
		inv Inverse
		err error
	}
	cmdUnwound struct{ f *Fiber }
	cmdService struct{}
)

func newOrchestrator(sh *shared) *orchestrator {
	return &orchestrator{
		sh:    sh,
		inbox: make(chan cmd, 4096),
		done:  make(chan struct{}),
	}
}

func (o *orchestrator) start() { go o.run() }

// send 入队一条命令。循环已退出时静默丢弃。
func (o *orchestrator) send(c cmd) {
	select {
	case o.inbox <- c:
	case <-o.done:
	}
}

func (o *orchestrator) notifyService() { o.send(cmdService{}) }

func (o *orchestrator) run() {
	defer close(o.done)
	for {
		c := <-o.inbox
		o.cmds.Add(1)
		o.handle(c)
		o.settle()
		if o.stopping.Load() && o.quiescent() {
			return
		}
	}
}

// quiescent：注册表清空且无在飞的 apply/unwind。
func (o *orchestrator) quiescent() bool {
	o.sh.mu.RLock()
	n := len(o.sh.registry)
	o.sh.mu.RUnlock()
	return n == 0 && o.sh.pending.Load() == 0
}

// shutdown 显式撤退全部 fiber 并等待循环退出。幂等。
func (o *orchestrator) shutdown() {
	o.stopOnce.Do(func() {
		o.stopping.Store(true)
		o.sh.mu.RLock()
		fs := make([]*Fiber, 0, len(o.sh.registry))
		for _, f := range o.sh.registry {
			fs = append(fs, f)
		}
		o.sh.mu.RUnlock()
		for _, f := range fs {
			o.send(cmdDispose{f: f})
		}
		o.send(cmdService{})
		<-o.done
	})
}

func (o *orchestrator) handle(c cmd) {
	switch c := c.(type) {
	case cmdLoad:
		o.sh.mu.Lock()
		o.sh.registry[c.f.id] = c.f
		o.sh.mu.Unlock()

	case cmdDispose:
		switch c.f.State() {
		case StatePending:
			o.removeFiber(c.f, StateGone)
		case StateLoading, StateUnloading:
			c.f.disposeRequested = true // 完成当前转移后撤退
		case StateActive:
			// Dispose 是终局语义：撤退后不得因依赖仍满足而复活。
			c.f.disposeRequested = true
			o.beginUnload(c.f)
		case StateGone, StateFailed:
			// 幂等：已终结。
		}

	case cmdApplied:
		f := c.f
		if c.inv != nil {
			inv := c.inv
			o.sh.mu.Lock()
			if !f.ctx.unwinding {
				f.ctx.inverses = append(f.ctx.inverses, inv)
				inv = nil
			}
			o.sh.mu.Unlock()
			if inv != nil {
				// 防御路径（fresh-context 设计下不可达）：立即自撤销。
				if err := inv(); err != nil {
					log.Printf("stc: stranded inverse error: %v", err)
				}
			}
		}
		switch {
		case c.err != nil:
			f.err = c.err
			f.failed = true
			o.beginUnload(f)
		case f.disposeRequested:
			o.beginUnload(f)
		case f.depsSatisfied():
			f.setState(StateActive)
		default:
			// 惯性：装载期间依赖消失，装载完成后直接卸载。
			o.beginUnload(f)
		}

	case cmdUnwound:
		f := c.f
		switch {
		case f.failed:
			o.removeFiber(f, StateFailed)
		case f.disposeRequested:
			o.removeFiber(f, StateGone)
		case f.depsSatisfied():
			// 惯性：卸载期间依赖恢复，立即重新装载。
			o.beginLoad(f)
		default:
			f.setState(StatePending)
		}

	case cmdService:
		// 无独立处理；settle 重新评估全部 fiber。
	}
}

// settle 推进到不动点：满足条件的 Pending→Loading、Active→Unloading。
func (o *orchestrator) settle() {
	for {
		o.sh.mu.RLock()
		fs := make([]*Fiber, 0, len(o.sh.registry))
		for _, f := range o.sh.registry {
			fs = append(fs, f)
		}
		o.sh.mu.RUnlock()
		sort.Slice(fs, func(i, j int) bool { return fs[i].id < fs[j].id })

		changed := false
		for _, f := range fs {
			switch f.State() {
			case StatePending:
				if f.depsSatisfied() {
					o.beginLoad(f)
					changed = true
				}
			case StateActive:
				if !f.depsSatisfied() {
					o.beginUnload(f)
					changed = true
				}
			}
		}
		if !changed {
			return
		}
	}
}

func (o *orchestrator) beginLoad(f *Fiber) {
	// 每个装载周期使用全新 context：上一周期的 context 已被卸载永久关闭，
	// 复用会让惯性重载的全部注册失效（Cordis 每次 mount 亦派生新 ctx）。
	f.ctx = f.home.Child()
	f.ctx.detach() // fiber 的 context 由其生命周期独占管理（D7）
	o.sh.mu.Lock()
	f.ctx.fiber = f
	o.sh.mu.Unlock()

	f.captureDeps()
	f.setState(StateLoading)
	o.sh.pending.Add(1)
	apply := f.comp.Apply
	if apply == nil {
		apply = func(*Context) (Inverse, error) {
			return nil, errors.New("stc: component has no Apply")
		}
	}
	go func() {
		o.sh.traceUser(TraceApplyStart, f.id)
		inv, err := apply(f.ctx)
		o.sh.pending.Add(-1)
		o.send(cmdApplied{f: f, inv: inv, err: err})
	}()
}

func (o *orchestrator) beginUnload(f *Fiber) {
	f.setState(StateUnloading)
	o.sh.pending.Add(1)
	go func() {
		_ = f.ctx.unwind() // 逆错误已逐个记录（吞没语义）
		o.sh.pending.Add(-1)
		o.send(cmdUnwound{f: f})
	}()
}

func (o *orchestrator) removeFiber(f *Fiber, terminal FiberState) {
	o.sh.mu.Lock()
	delete(o.sh.registry, f.id)
	o.sh.mu.Unlock()
	f.setState(terminal)
}

// depsSatisfied 检查 fiber 的全部 inject 键可解析（不计自身提供的条目）。
// 首次装载前 ctx 尚未派生，用 home 解析（realm 链与子 context 等价）。
func (f *Fiber) depsSatisfied() bool {
	for _, k := range f.comp.Inject {
		if _, _, ok := f.resolveBase().resolveExternal(k, f); !ok {
			return false
		}
	}
	return true
}

func (f *Fiber) resolveBase() *Context {
	if f.ctx != nil {
		return f.ctx
	}
	return f.home
}

// captureDeps 记录装载时刻的依赖代际；depStale 判定其后是否被替换
// （无缝换血：依赖始终可解析，但提供者已换人）。
func (f *Fiber) captureDeps() {
	f.depSnap = make(map[Key]provideIdent, len(f.comp.Inject))
	for _, k := range f.comp.Inject {
		if _, id, ok := f.resolveBase().resolveExternal(k, f); ok {
			f.depSnap[k] = id
		}
	}
}

func (f *Fiber) depStale() bool {
	for _, k := range f.comp.Inject {
		_, id, ok := f.resolveBase().resolveExternal(k, f)
		if !ok || id != f.depSnap[k] {
			return true
		}
	}
	return false
}
