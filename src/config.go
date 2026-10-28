package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// 配置目录：macOS ~/Library/Application Support/clipsync，Windows %AppData%/clipsync
var configDir string

// token 是设备间共享的配对密钥，两端填同一个值即可配对；
// 运行中可通过托盘「配对密钥…」修改，因此读写一律走 getToken/setToken，
// 避免托盘协程写入与网络协程读取之间形成数据竞争。
var (
	tokenMu sync.RWMutex
	token   string
)

// getToken 读取当前配对密钥
func getToken() string {
	tokenMu.RLock()
	defer tokenMu.RUnlock()
	return token
}

// setToken 更新内存中的配对密钥（持久化由 saveToken 负责）
func setToken(v string) {
	tokenMu.Lock()
	token = v
	tokenMu.Unlock()
}

func init() {
	initConfig()
}

type configFile struct {
	Token       string   `json:"token"`
	AutoStart   string   `json:"autoStart"`     // "on"/"off"，记录开机自启偏好
	ContextMenu string   `json:"contextMenu"`   // "on"/"off"，记录 Windows 右键发送偏好
	Paired      []string `json:"pairedDevices"` // 已配对设备名，仅用于"首次配对"提示去重
	NotifyWarn  string   `json:"notifyWarned"`  // 上次提示"通知权限被拒"的日期（YYYY-MM-DD），用于每天最多提醒一次
}

// cfgMu 串行化对 config.json 的读写：托盘开关（开机启动/右键发送）在各自
// goroutine 里操作同一文件，若无锁会发生"读-改-写"丢失更新（后写覆盖先写的其它字段）。
// 所有读取在锁内执行，所有写入在锁内完成"读整份→改单字段→原子写回"，杜绝并发冲突。
var cfgMu sync.Mutex

// configFilePath 返回 config.json 完整路径
func configFilePath() string { return filepath.Join(configDir, "config.json") }

// readConfigLocked 加锁读取整份配置；调用方必须已持有 cfgMu
func readConfigLocked() configFile {
	var c configFile
	if data, err := os.ReadFile(configFilePath()); err == nil {
		json.Unmarshal(data, &c)
	}
	return c
}

// writeConfigLocked 加锁原子写回配置：先写临时文件再重命名，避免写入一半
// 留下损坏的 config.json；调用方必须已持有 cfgMu
func writeConfigLocked(c configFile) {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return
	}
	tmp := configFilePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return
	}
	os.Rename(tmp, configFilePath())
}

// getAutoStartPref 读取开机自启偏好；未设置时返回 ""（视为默认开启）
func getAutoStartPref() string {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return readConfigLocked().AutoStart
}

// setAutoStartPref 记录开机自启偏好（不覆盖已有的 token/contextMenu）
func setAutoStartPref(v string) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	c := readConfigLocked()
	c.AutoStart = v
	writeConfigLocked(c)
}

// getContextMenuPref 读取右键发送偏好；未设置时返回 ""（视为默认开启）
func getContextMenuPref() string {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return readConfigLocked().ContextMenu
}

// setContextMenuPref 记录右键发送偏好（不覆盖已有的 token/autoStart）
func setContextMenuPref(v string) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	c := readConfigLocked()
	c.ContextMenu = v
	writeConfigLocked(c)
}

// getNotifyWarned 返回上次提示"通知权限被拒"的日期（YYYY-MM-DD）
func getNotifyWarned() string {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return readConfigLocked().NotifyWarn
}

// setNotifyWarned 记录本次提示日期，避免同一天反复弹窗
func setNotifyWarned(v string) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	c := readConfigLocked()
	c.NotifyWarn = v
	writeConfigLocked(c)
}

// normalizeToken 校验并规范化用户输入的配对密钥。
// 规则：去首尾空白后 4~64 个字符；不含空白与控制字符（IPC 用换行分隔首行，
// 密钥里混入换行会把协议搞乱），不含引号（对话框脚本转义后易出错）。
func normalizeToken(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("密钥不能为空")
	}
	n := utf8.RuneCountInString(s)
	if n < 4 {
		return "", fmt.Errorf("密钥太短：至少 4 个字符（当前 %d 个）", n)
	}
	if n > 64 {
		return "", fmt.Errorf("密钥太长：最多 64 个字符（当前 %d 个）", n)
	}
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			return "", fmt.Errorf("密钥不能包含空格或换行")
		case unicode.IsControl(r):
			return "", fmt.Errorf("密钥不能包含控制字符")
		case r == '"' || r == '\'' || r == '\\':
			return "", fmt.Errorf("密钥不能包含引号或反斜杠")
		}
	}
	return s, nil
}

// saveToken 修改配对密钥：先写 config.json（不覆盖 autoStart/contextMenu 等字段），
// 成功后再更新内存值，最后返回生效的新密钥。
func saveToken(v string) error {
	cfgMu.Lock()
	c := readConfigLocked()
	c.Token = v
	// writeConfigLocked 内部失败不返回错误；这里用回读校验确保真的落盘
	writeConfigLocked(c)
	got := readConfigLocked().Token
	cfgMu.Unlock()
	if got != v {
		return fmt.Errorf("写入 config.json 失败，密钥未生效")
	}
	setToken(v)
	return nil
}

func initConfig() {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		dir = "."
	}
	configDir = filepath.Join(dir, "clipsync")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		configDir = "."
	}
	loadToken()
}

// defaultToken 内置默认配对密钥：两端一致即可配对，开箱即用。
// 同网段只要装了 ClipSync 且密钥相同就能接入，因此建议改成自己的密钥
// （托盘「配对密钥…」可直接改，两台设备填同一个值）。
const defaultToken = "clipsync-2026"

func loadToken() {
	cfgMu.Lock()
	c := readConfigLocked()
	cfgMu.Unlock()
	if c.Token != "" {
		setToken(c.Token)
		return
	}
	setToken(defaultToken)
}

// getPairedMap 读取已配对设备集合；进程重启后仍记住，避免每次启动重复提示"新配对"
func getPairedMap() map[string]bool {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	m := map[string]bool{}
	for _, n := range readConfigLocked().Paired {
		if n != "" {
			m[n] = true
		}
	}
	return m
}

// addPaired 把设备名标记为已配对并原子写回 config.json（不覆盖 token/autoStart/contextMenu）
func addPaired(name string) {
	if name == "" {
		return
	}
	cfgMu.Lock()
	defer cfgMu.Unlock()
	c := readConfigLocked()
	for _, n := range c.Paired {
		if n == name {
			return
		}
	}
	c.Paired = append(c.Paired, name)
	writeConfigLocked(c)
}

// 证书全局单例：多个连接并发加载时只生成一次，避免并发写同一证书文件
var (
	certOnce    sync.Once
	certCache   tls.Certificate
	certLoadErr error
)

// loadCert 加载或生成自签名证书，用于 TLS 加密
func loadCert() (tls.Certificate, error) {
	certOnce.Do(func() {
		certPath := filepath.Join(configDir, "cert.pem")
		keyPath := filepath.Join(configDir, "key.pem")
		if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
			certCache, certLoadErr = cert, nil
			return
		}
		certCache, certLoadErr = generateCert(certPath, keyPath)
	})
	return certCache, certLoadErr
}

func generateCert(certPath, keyPath string) (tls.Certificate, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "ClipSync", Organization: []string{"ClipSync"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, err
	}

	certOut, err := os.Create(certPath)
	if err != nil {
		return tls.Certificate{}, err
	}
	pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	certOut.Close()

	keyOut, err := os.Create(keyPath)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	keyOut.Close()

	return tls.LoadX509KeyPair(certPath, keyPath)
}
