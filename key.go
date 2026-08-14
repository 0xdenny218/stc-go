package stc

import (
	"errors"
	"reflect"
	"sync/atomic"
)

// Key 是服务的稳定标识。用 NewKey[T] 创建以获得运行时类型校验。
type Key struct {
	name string
	typ  reflect.Type
}

// NewKey 创建带类型约束的服务键。
func NewKey[T any](name string) Key {
	return Key{name: name, typ: reflect.TypeFor[T]()}
}

// UntypedKey 创建不带类型约束的服务键（用于 any 语义的测试或动态场景）。
func UntypedKey(name string) Key { return Key{name: name} }

func (k Key) Name() string { return k.name }

func (k Key) String() string { return k.name }

// realm 是服务解析的隔离域（论文 isolate(k, r) 的 r）。
// realm 组成链：本域查不到时沿 parent 回落，最终到根域。
type realm struct {
	id     uint64
	name   string
	parent *realm
}

var realmSeq atomic.Uint64

var rootRealm = &realm{id: realmSeq.Add(1), name: "root"}

// RootRealm 返回所有 context 默认解析到的根域。
func RootRealm() *realm { return rootRealm }

// NewRealm 在 parent 之下创建子域。
func NewRealm(parent *realm, name string) *realm {
	if parent == nil {
		parent = rootRealm
	}
	return &realm{id: realmSeq.Add(1), name: name, parent: parent}
}

var (
	// ErrInactive 表示 context 已开始回卷或已关闭，拒绝新注册。
	ErrInactive = errors.New("stc: inactive context")
	// ErrNilInstall 表示 Effect 收到空的安装函数。
	ErrNilInstall = errors.New("stc: nil install")
	// ErrDuplicateProvide 表示某服务键已由另一个 fiber 提供。
	// 对应论文 Definition 58 的良构性：每 (key, realm) 至多一个 fiber 提供者。
	ErrDuplicateProvide = errors.New("stc: duplicate provide")
)
