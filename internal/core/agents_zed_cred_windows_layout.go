package core

// Windows 凭据管理器（Credential Manager）的结构体布局与条目命名，按 MSDN 定义。
//
// 刻意放在平台无关的文件里：CREDENTIALW 的内存布局是这条链路唯一「写错也不会报错、
// 只会在 Windows 上静默失效」的地方，放这里就能在任意平台（含开发机与 macOS CI）
// 用 unsafe 断言偏移量，见 agents_zed_cred_layout_test.go。
//
// Zed 在 Windows 上写入的条目形态（crates/gpui_windows/src/util.rs 与 platform.rs）：
// 类型 GENERIC、条目名 "zed:url=<api_url>"、用户名 "Bearer"、密码 = 密钥的 UTF-8 原始字节、
// 持久化范围本机。注意密码必须是原始字节：cmdkey 之类工具会把密码转成 UTF-16 存，
// Zed 读回来按 UTF-8 解析就会拿到乱码，所以只能走 CredWriteW。

// credFiletime 对应 Win32 FILETIME（两个 DWORD）
type credFiletime struct {
	LowDateTime  uint32
	HighDateTime uint32
}

// credW 对应 CREDENTIALW
type credW struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        credFiletime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         *byte
	TargetAlias        *uint16
	UserName           *uint16
}

// zedCredentialTarget Windows 侧条目名
func zedCredentialTarget(apiURL string) string {
	return "zed:url=" + apiURL
}
