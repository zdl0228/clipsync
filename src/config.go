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
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// 配置目录：macOS ~/Library/Application Support/clipsync，Windows %AppData%/clipsync
var configDir string

// token 是设备间共享的配对密钥，两端必须一致才能通信；可手动编辑 config.json 修改
var token string

func init() {
	initConfig()
}

type configFile struct {
	Token       string `json:"token"`
	AutoStart   string `json:"autoStart"`   // "on"/"off"，记录开机自启偏好
	ContextMenu string `json:"contextMenu"` // "on"/"off"，记录 Windows 右键发送偏好
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

// defaultToken 内置默认配对密钥：两端一致即可加密连接，开箱即用
// 如需自定义，可在 config.json 中配置 token，程序会优先采用自定义值
const defaultToken = "clipsync-2026"

func loadToken() {
	cfgMu.Lock()
	c := readConfigLocked()
	cfgMu.Unlock()
	if c.Token != "" {
		token = c.Token
		return
	}
	token = defaultToken
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
