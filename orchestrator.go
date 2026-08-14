package stc

// orchestrator 串行化 fiber 生命周期转移（论文中的 orchestrator 角色）。
// M1 阶段为空实现；M2 提交完整的事件驱动状态机。
type orchestrator struct{}

func (o *orchestrator) notifyService() {}
