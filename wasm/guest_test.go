package wasm

// 手写 WASM 二进制编码器与测试 guest。
// 刻意不依赖 wabt/tinygo：guest 极小且全部静态，手写编码保证
// 测试字节码完全可控（包括构造畸形与 trap 模块）。

// 类型表（本 ABI 固定四个签名）：
//
//	0: (i32,i32,i32,i32) -> i32   provide / get
//	1: (i32,i32) -> i32           get_size
//	2: (i32,i32) -> ()            log
//	3: () -> ()                   start / stop
//
// 导入函数索引：0=provide 1=get_size 2=get 3=log；
// 本地函数（start/stop）从索引 4 起。
const (
	fnProvide = 0
	fnGetSize = 1
	fnGet     = 2
	fnLog     = 3
)

func uleb(v uint64) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			out = append(out, b|0x80)
		} else {
			return append(out, b)
		}
	}
}

func sleb(v int64) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		done := (v == 0 && b&0x40 == 0) || (v == -1 && b&0x40 != 0)
		if !done {
			b |= 0x80
		}
		out = append(out, b)
		if done {
			return out
		}
	}
}

func i32c(v int) []byte  { return append([]byte{0x41}, sleb(int64(v))...) }
func callf(i int) []byte { return append([]byte{0x10}, uleb(uint64(i))...) }
func lget(i int) []byte  { return append([]byte{0x20}, uleb(uint64(i))...) }
func lset(i int) []byte  { return append([]byte{0x21}, uleb(uint64(i))...) }

var (
	opDrop    = []byte{0x1a}
	opUnreach = []byte{0x00}
	opIf      = []byte{0x04, 0x40} // void block
	opEnd     = []byte{0x0b}
	opI32Ne   = []byte{0x47}
)

func cat(bs ...[]byte) []byte {
	var out []byte
	for _, b := range bs {
		out = append(out, b...)
	}
	return out
}

func name(s string) []byte { return append(uleb(uint64(len(s))), s...) }

func section(id byte, payload []byte) []byte {
	return cat([]byte{id}, uleb(uint64(len(payload))), payload)
}

func vec(items ...[]byte) []byte {
	return cat(uleb(uint64(len(items))), cat(items...))
}

type guestData struct {
	off int
	s   string
}

type guestSpec struct {
	start  []byte // 指令序列（不含结尾 0x0b）；nil 表示不导出 start
	stop   []byte
	locals int // start 的 i32 局部变量数
	data   []guestData
}

func buildGuest(g guestSpec) []byte {
	types := vec(
		[]byte{0x60, 4, 0x7f, 0x7f, 0x7f, 0x7f, 1, 0x7f},
		[]byte{0x60, 2, 0x7f, 0x7f, 1, 0x7f},
		[]byte{0x60, 2, 0x7f, 0x7f, 0},
		[]byte{0x60, 0, 0},
	)
	imp := func(field string, typ byte) []byte {
		return cat(name("stc"), name(field), []byte{0x00, typ})
	}
	imports := vec(
		imp("provide", 0), imp("get_size", 1), imp("get", 0), imp("log", 2),
	)

	// 本地函数与导出。
	var funcs, codes, exports [][]byte
	next := 4
	export := func(n string, kind, idx byte) []byte {
		return cat(name(n), []byte{kind, idx})
	}
	exports = append(exports, export("memory", 0x02, 0))
	body := func(instrs []byte, locals int) []byte {
		var loc []byte
		if locals > 0 {
			loc = []byte{1, byte(locals), 0x7f}
		} else {
			loc = []byte{0}
		}
		code := cat(loc, instrs, opEnd)
		return cat(uleb(uint64(len(code))), code)
	}
	if g.start != nil {
		funcs = append(funcs, []byte{3})
		codes = append(codes, body(g.start, g.locals))
		exports = append(exports, export("start", 0x00, byte(next)))
		next++
	}
	if g.stop != nil {
		funcs = append(funcs, []byte{3})
		codes = append(codes, body(g.stop, 0))
		exports = append(exports, export("stop", 0x00, byte(next)))
		next++
	}

	var datas [][]byte
	for _, d := range g.data {
		datas = append(datas, cat(
			[]byte{0x00}, i32c(d.off), opEnd, uleb(uint64(len(d.s))), []byte(d.s),
		))
	}

	return cat(
		[]byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00},
		section(1, types),
		section(2, imports),
		section(3, vec(funcs...)),
		section(5, []byte{1, 0x00, 0x01}), // 1 页内存
		section(7, vec(exports...)),
		section(10, vec(codes...)),
		section(11, vec(datas...)),
	)
}

// helloGuest：start 提供 greeting=msg；stop 记录日志 "hello stopped"。
func helloGuest(msg string) []byte {
	const (
		keyOff = 16 // "greeting"
		valOff = 64
		logOff = 128
	)
	logMsg := "hello stopped"
	return buildGuest(guestSpec{
		start: cat(
			i32c(keyOff), i32c(8), i32c(valOff), i32c(len(msg)), callf(fnProvide), opDrop,
		),
		stop: cat(i32c(logOff), i32c(len(logMsg)), callf(fnLog)),
		data: []guestData{
			{keyOff, "greeting"}, {valOff, msg}, {logOff, logMsg},
		},
	})
}

// readerGuest：start 读取 greeting 并原样提供为 echo（依赖链跨边界）。
// greeting 不存在时什么都不提供（get_size 返回 -1）。
func readerGuest() []byte {
	const (
		greetOff = 16 // "greeting"
		echoOff  = 64 // "echo"
		buf      = 1024
	)
	return buildGuest(guestSpec{
		locals: 1,
		start: cat(
			// size = get_size("greeting")
			i32c(greetOff), i32c(8), callf(fnGetSize), lset(0),
			// if size != -1:
			lget(0), i32c(-1), opI32Ne, opIf,
			//   get("greeting", buf, 64)
			i32c(greetOff), i32c(8), i32c(buf), i32c(64), callf(fnGet), opDrop,
			//   provide("echo", buf, size)
			i32c(echoOff), i32c(4), i32c(buf), lget(0), callf(fnProvide), opDrop,
			opEnd,
		),
		data: []guestData{{greetOff, "greeting"}, {echoOff, "echo"}},
	})
}

// trapGuest：实例化合法，但 start 立即 trap（post-实例化失败路径）。
func trapGuest() []byte {
	return buildGuest(guestSpec{start: opUnreach})
}

// badGuest：版本号非法，编译期即失败。
func badGuest() []byte {
	return []byte{0x00, 0x61, 0x73, 0x6d, 0x0d, 0x00, 0x00, 0x00}
}

// withModuleName 给模块追加 name section 的模块名子节，
// 模拟工具链产物（TinyGo 的模块名恒为 "main"）。
// 回归场景：同名模块 Update 不得因实例名冲突失败。
func withModuleName(src []byte, modName string) []byte {
	sub := cat([]byte{0x00}, uleb(uint64(len(modName)+1)), name(modName))
	return cat(src, section(0, cat(name("name"), sub)))
}
