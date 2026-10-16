package core

import (
	"testing"
	"unsafe"
)

// CREDENTIALW 的字段顺序/偏移写错时，Windows 上不会报错、只会静默写坏凭据，
// 所以在这里按 MSDN 的 x64 布局断言一遍（64 位下 macOS 与 Windows 的
// DWORD/指针/FILETIME 对齐规则一致，本机即可验证）。
func TestCredWLayout(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("非 64 位平台，偏移量不适用")
	}
	cases := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"Flags", unsafe.Offsetof(credW{}.Flags), 0},
		{"Type", unsafe.Offsetof(credW{}.Type), 4},
		{"TargetName", unsafe.Offsetof(credW{}.TargetName), 8},
		{"Comment", unsafe.Offsetof(credW{}.Comment), 16},
		{"LastWritten", unsafe.Offsetof(credW{}.LastWritten), 24},
		{"CredentialBlobSize", unsafe.Offsetof(credW{}.CredentialBlobSize), 32},
		{"CredentialBlob", unsafe.Offsetof(credW{}.CredentialBlob), 40},
		{"Persist", unsafe.Offsetof(credW{}.Persist), 48},
		{"AttributeCount", unsafe.Offsetof(credW{}.AttributeCount), 52},
		{"Attributes", unsafe.Offsetof(credW{}.Attributes), 56},
		{"TargetAlias", unsafe.Offsetof(credW{}.TargetAlias), 64},
		{"UserName", unsafe.Offsetof(credW{}.UserName), 72},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("CREDENTIALW.%s 偏移 %d，应为 %d", c.name, c.got, c.want)
		}
	}
	if got := unsafe.Sizeof(credW{}); got != 80 {
		t.Errorf("CREDENTIALW 大小 %d，应为 80", got)
	}
	if got := unsafe.Sizeof(credFiletime{}); got != 8 {
		t.Errorf("FILETIME 大小 %d，应为 8", got)
	}
}

func TestZedCredentialTarget(t *testing.T) {
	if got := zedCredentialTarget("http://127.0.0.1:7863/v1"); got != "zed:url=http://127.0.0.1:7863/v1" {
		t.Fatalf("条目名不符: %q", got)
	}
}
