package main

import (
	_ "embed"
	"net/http"
	"strings"
)

// ============================================================
// 页面托管
//
// 页面文件通过 go:embed 打进二进制：部署时只用拷一个 exe，
// 不会有"忘了带 web 目录导致首页 404"这种事。
//
// 页面统一放在 web/ 子目录里，跟 .go 源码分开。
// go:embed 不允许用 ".." 往上越级，但**可以嵌子目录**，
// 所以 web/ 必须留在本包目录内——挪到仓库外面就编译不过了。
// ============================================================

//go:embed web/index.html
var indexHTML string

//go:embed web/bootstrap.min.css
var bootstrapCSS []byte

// renderPage 替换模板中的占位符。
// 目前只剩版本号（右上角角标）——测速目标改由用户自己填，
// 不再从服务端预置默认值。
// 用简单字符串替换而不是 html/template：页面里全是 JS 的花括号，
// 套模板语法反而要到处转义，得不偿失。
func renderPage(tmpl string) []byte {
	html := strings.ReplaceAll(tmpl, "{{VERSION}}", Version)
	return []byte(html)
}

// handleIndex 测速台首页
// GET /
func handleIndex(w http.ResponseWriter, r *http.Request) {
	// 只认根路径，其它未注册路径仍返回 404，避免以后新增子路由被吃掉
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// 页面里带着目标 URL 等默认值，升级二进制后不能被缓存卡住
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(renderPage(indexHTML))
}

// faviconSVG 内联站点图标：🛰 卫星圆点组合。
// 不依赖外部文件，顺便消掉浏览器自动请求 /favicon.ico 产生的 404 噪音。
const faviconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">` +
	`<rect width="64" height="64" rx="14" fill="#58a6ff"/>` +
	`<circle cx="26" cy="38" r="5" fill="#0b0f16"/>` +
	`<path d="M18 30a16 16 0 0 1 16 16" stroke="#fff" stroke-width="4" fill="none" stroke-linecap="round"/>` +
	`<path d="M18 18a28 28 0 0 1 28 28" stroke="#fff" stroke-width="4" fill="none" stroke-linecap="round" opacity=".85"/></svg>`

// handleFavicon 站点图标
// GET /favicon.ico
func handleFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write([]byte(faviconSVG))
}

// handleVendorCSS 本地托管的 Bootstrap 4 样式表（v4.6.2）。
//
// 刻意走本地托管而不是 CDN：这套控制台经常部署在内网 / 无外网的机器上，
// 依赖 CDN 会让"断网 → 页面裸奔"成为必然，而且公网请求也拖慢首屏。
//
// GET /vendor/bootstrap.min.css
func handleVendorCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(bootstrapCSS)
}
