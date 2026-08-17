module example.com/plugin-http-guest

go 1.25.0

require github.com/0xdenny218/stc-go v0.1.0

// 本地开发：指向仓库根。发布后可直接 require 版本号。
replace github.com/0xdenny218/stc-go => ../../..
