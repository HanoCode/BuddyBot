package core

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
)

// ============================================================
// 凭证导出加密（AES-256-GCM + PBKDF2-HMAC-SHA256）
//
// 对齐 WorkDaddy 的导出方案（思路复刻、实现自写）：
//   - 密钥 = PBKDF2-HMAC-SHA256(用户密码, 随机 salt, 迭代)
//   - 内容 = AES-256-GCM 加密的导出 JSON（GCM 自带完整性校验）
//   - 密码不写入导出文件；每次导出随机 salt 与 nonce
//
// 有意不引入 golang.org/x/crypto：PBKDF2-HMAC-SHA256 用标准库
// crypto/hmac + crypto/sha256 约 20 行即可正确实现，保持零新增依赖。
// ============================================================

// pbkdf2SHA256 标准 PBKDF2（RFC 2898）with HMAC-SHA256，stdlib 实现。
func pbkdf2SHA256(password, salt []byte, iterations, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	numBlocks := (keyLen + hashLen - 1) / hashLen

	var buf [4]byte
	dk := make([]byte, 0, numBlocks*hashLen)
	U := make([]byte, hashLen)
	for block := 1; block <= numBlocks; block++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(buf[:], uint32(block))
		prf.Write(buf[:4])
		dk = prf.Sum(dk)
		T := dk[len(dk)-hashLen:]
		copy(U, T)

		for n := 2; n <= iterations; n++ {
			prf.Reset()
			prf.Write(U)
			U = U[:0]
			U = prf.Sum(U)
			for i := range U {
				T[i] ^= U[i]
			}
		}
	}
	return dk[:keyLen]
}

// pbkdf2Iterations 密钥派生迭代次数。OWASP 2023 对 PBKDF2-SHA256 的建议下限
// 为 60 万，桌面本地工具取 21 万在安全与导出耗时（<0.5s）间平衡。
const pbkdf2Iterations = 210000

// EncryptedExport 加密导出信封（version=2；version=1 为历史明文导出）
type EncryptedExport struct {
	Version    int    `json:"version"` // 恒为 2
	KDF        string `json:"kdf"`     // pbkdf2-sha256
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`  // base64
	Nonce      string `json:"nonce"` // base64
	Data       string `json:"data"`  // base64(AES-256-GCM 密文)
	Count      int    `json:"count"` // 导出的凭证份数（解密前可见）
	ExportedAt string `json:"exportedAt"`
}

// EncryptExport 把任意导出载荷加密为信封 JSON。
func EncryptExport(payload any, password, exportedAt string, count int) ([]byte, error) {
	if password == "" {
		return nil, fmt.Errorf("导出密码不能为空")
	}
	plain, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	nonce := make([]byte, 12)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	key := pbkdf2SHA256([]byte(password), salt, pbkdf2Iterations, 32)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, plain, nil)
	b64 := base64.StdEncoding.EncodeToString
	env := EncryptedExport{
		Version:    2,
		KDF:        "pbkdf2-sha256",
		Iterations: pbkdf2Iterations,
		Salt:       b64(salt),
		Nonce:      b64(nonce),
		Data:       b64(sealed),
		Count:      count,
		ExportedAt: exportedAt,
	}
	return json.Marshal(env)
}

// DecryptExport 解密导出信封，返回内层原始 JSON。
// 密码错误时 GCM 校验失败，返回可读错误（不会得到乱码）。
func DecryptExport(envBytes []byte, password string) ([]byte, error) {
	if password == "" {
		return nil, fmt.Errorf("该文件为加密导出，需要提供导出密码")
	}
	var env EncryptedExport
	if err := json.Unmarshal(envBytes, &env); err != nil {
		return nil, fmt.Errorf("不是有效的加密导出文件: %w", err)
	}
	if env.Version != 2 {
		return nil, fmt.Errorf("不支持的导出版本: %d", env.Version)
	}
	b64 := base64.StdEncoding.DecodeString
	salt, err := b64(env.Salt)
	if err != nil {
		return nil, fmt.Errorf("导出文件 salt 无效")
	}
	nonce, err := b64(env.Nonce)
	if err != nil {
		return nil, fmt.Errorf("导出文件 nonce 无效")
	}
	data, err := b64(env.Data)
	if err != nil {
		return nil, fmt.Errorf("导出文件数据无效")
	}
	key := pbkdf2SHA256([]byte(password), salt, env.Iterations, 32)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, data, nil)
	if err != nil {
		return nil, fmt.Errorf("解密失败：导出密码错误或文件已损坏")
	}
	return plain, nil
}
