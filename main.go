package main

import (
	"fmt"
	"net/http"
	"os"
)

// ============================================================
// 版本号（通过 ldflags 注入：-X main.Version=xxx）
// ============================================================

var Version = "1.0.0"

// defaultPort 监听端口，可用环境变量 PROXYIP_PORT 覆盖
const defaultPort = "8899"

// ============================================================
// 入口
// ============================================================

func main() {
	// ---------- 页面（go:embed 打进二进制，无需外部文件） ----------
	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/favicon.ico", handleFavicon)
	// Bootstrap 本地托管：这个工具经常跑在内网机器上，依赖 CDN 会直接裸奔
	http.HandleFunc("/vendor/bootstrap.min.css", handleVendorCSS)

	// ---------- 测速接口 ----------
	// /api/test 是流式（NDJSON）的：每测完一个代理就立刻吐一行结果，
	// 代理多的时候不用等全部跑完才能看到东西
	http.HandleFunc("/api/test", handleSpeedTest)
	http.HandleFunc("/api/local", handleLocalPing)
	http.HandleFunc("/version", handleVersion)

	port := os.Getenv("PROXYIP_PORT")
	if port == "" {
		port = defaultPort
	}

	fmt.Println("========================================")
	fmt.Println("  代理 IP 测速台  v" + Version)
	fmt.Println("----------------------------------------")
	fmt.Println("  🌐 控制台       GET  /        ← 浏览器打开这个")
	fmt.Println("  POST /api/test  - 批量测速（流式 NDJSON）")
	fmt.Println("  GET  /api/local - 本地网络延迟基线")
	fmt.Println("  GET  /version   - 查看版本号")
	fmt.Println("----------------------------------------")
	fmt.Println("  测速目标: 页面里填（必填，不设默认值）")
	fmt.Println("  监听端口: " + port + "   控制台: http://localhost:" + port + "/")
	fmt.Println("========================================")

	if err := http.ListenAndServe(":"+port, nil); err != nil {
		fmt.Printf("服务启动失败: %v\n", err)
	}
}

// handleVersion 返回当前版本号
// GET /version
func handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"code":    0,
		"version": Version,
	})
}
