package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ============================================================
// 测速内核
//
// 一次测速 = 用指定代理发一个完整的 HTTPS GET，然后拆解时间轴：
//
//   建连(ConnMs) → TLS 握手(TLSMs) → 等首字节(TTFBMs) → 读完响应体
//   └────────────────────── TotalMs ──────────────────────┘
//
// 判定一个代理"能用"的门槛刻意放得很低：只要在超时内拿回了 HTTP 响应
// 就算通，状态码是不是 200 单独展示。
// 原因是这类公开代理经常被目标站按地区/风控拒绝，403 一样能说明
// "这条链路是通的、只是被挡了"，比笼统报"失败"有用得多。
// ============================================================

const (
	// defaultTargetURL 默认测速目标（空 game_code 也能返回 JSON，足够做连通性/延迟判定）
	defaultTargetURL = "https://pay-api.ds.163.com/api/god-income/info?game_code="

	// userAgent 伪装成浏览器：部分站点对 Go-http-client 直接 403
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

	// maxProxyCount 单次最多测多少条，防止粘 10 万行把机器和对方站点打爆
	maxProxyCount = 500

	// maxBodyRead 单个响应最多读多少字节：只测连通性，没必要把整页拉下来
	maxBodyRead = 512 << 10

	// snippetRunes 响应片段保留的字符数（按 rune 计），用于判断返回的是不是 JSON
	snippetRunes = 120
)

// parseError 直接暴露给前端的"哪一行没解析成功"
type parseError struct {
	Line  int    `json:"line"`  // 行号（1 起，含空行，跟编辑器行号对齐）
	Raw   string `json:"raw"`   // 原始文本
	Error string `json:"error"` // 失败原因
}

// ProxyEntry 一行代理输入解析后的结果
type ProxyEntry struct {
	Line   int      `json:"line"`   // 行号
	Raw    string   `json:"raw"`    // 原始输入
	Addr   string   `json:"addr"`   // 规范化后的 host:port
	Scheme string   `json:"scheme"` // http / https / socks5 / socks5h
	Auth   bool     `json:"auth"`   // 是否带账号密码（前端只显示"带认证"，不显示密码）
	URL    *url.URL `json:"-"`      // 交给 http.Transport 的代理地址
}

// RoundResult 单轮请求的测量明细
type RoundResult struct {
	OK      bool
	Status  int
	Kind    string // json / html / text / empty
	TotalMs float64
	TTFBMs  float64
	ConnMs  float64
	TLSMs   float64
	Bytes   int64
	Snippet string
	Err     string
}

// TestResult 一个代理（可能多轮）的汇总结果，也是返回给前端的结构
type TestResult struct {
	Line      int     `json:"line"`
	Raw       string  `json:"raw"`
	Addr      string  `json:"addr"`
	Scheme    string  `json:"scheme"`
	Auth      bool    `json:"auth"`
	OK        bool    `json:"ok"`
	Status    int     `json:"status"`    // 最好那轮的状态码
	Kind      string  `json:"kind"`      // json / html / text / empty
	MinMs     float64 `json:"minMs"`     // 最快一轮的总耗时
	AvgMs     float64 `json:"avgMs"`     // 成功轮的平均总耗时
	TTFBMs    float64 `json:"ttfbMs"`    // 最快那轮的首字节时间
	ConnMs    float64 `json:"connMs"`    // 最快那轮的建连时间
	TLSMs     float64 `json:"tlsMs"`     // 最快那轮的 TLS 握手时间
	Bytes     int64   `json:"bytes"`     // 响应体字节数
	Snippet   string  `json:"snippet"`   // 响应片段
	Rounds    int     `json:"rounds"`    // 实际跑了多少轮
	OKRounds  int     `json:"okRounds"`  // 成功轮数
	Error     string  `json:"error"`     // 失败原因（全部轮都失败时）
	ElapsedMs int64   `json:"elapsedMs"` // 这个代理整体花了多久（含多轮间隔）
}

// testOptions 一次测速的运行参数（已归一化）
type testOptions struct {
	Target      string
	Timeout     time.Duration
	Rounds      int
	Concurrency int
}

// ============================================================
// 输入解析
// ============================================================

// parseProxyText 解析多行文本，返回可测的条目 + 解析失败的行。
//
// 支持的写法（代理池导出的格式五花八门，这里尽量都认）：
//
//	1.2.3.4:8080                  → http://1.2.3.4:8080
//	1.2.3.4:8080:user:pass        → 带认证的 http 代理
//	user:pass@1.2.3.4:8080        → 带认证的 http 代理
//	http://user:pass@1.2.3.4:8080
//	https://1.2.3.4:8443
//	socks5://1.2.3.4:1080
//	socks5://user:pass@1.2.3.4:1080
//
// 重复条目会被静默去掉：同一个代理测两遍既浪费对方配额，也让排名失真。
func parseProxyText(text string) (entries []ProxyEntry, bad []parseError, duplicated int, truncated bool) {
	seen := make(map[string]bool, 64)

	for i, line := range strings.Split(text, "\n") {
		lineNo := i + 1
		raw := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if raw == "" || strings.HasPrefix(raw, "#") || strings.HasPrefix(raw, "//") {
			continue
		}

		e, err := parseProxyLine(lineNo, raw)
		if err != nil {
			bad = append(bad, parseError{Line: lineNo, Raw: raw, Error: err.Error()})
			continue
		}

		key := e.Scheme + "|" + e.URL.String()
		if seen[key] {
			duplicated++
			continue
		}
		seen[key] = true

		if len(entries) >= maxProxyCount {
			truncated = true
			break
		}
		entries = append(entries, e)
	}
	return entries, bad, duplicated, truncated
}

// parseProxyLine 解析单行代理
func parseProxyLine(lineNo int, raw string) (ProxyEntry, error) {
	e := ProxyEntry{Line: lineNo, Raw: raw}

	// 先把 scheme 摘出来，剩下的统一按 host[:port][:user:pass] 处理
	scheme := "http"
	s := raw
	if i := strings.Index(s, "://"); i > 0 {
		scheme = strings.ToLower(s[:i])
		s = s[i+3:]
	}
	switch scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return e, fmt.Errorf("不支持的协议 %q（只支持 http/https/socks5）", scheme)
	}

	// 去掉路径部分：有人会直接粘 http://ip:port/ 甚至带 ?x=1
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}

	// user:pass@host:port
	var user *url.Userinfo
	if i := strings.LastIndex(s, "@"); i >= 0 {
		cred, rest := s[:i], s[i+1:]
		if u, p, ok := strings.Cut(cred, ":"); ok && u != "" {
			user = url.UserPassword(u, p)
		} else if cred != "" {
			user = url.User(cred)
		}
		s = rest
	}

	host, port, restParts := splitHostPort(s)
	if host == "" {
		return e, errors.New("缺少主机地址")
	}
	if port == "" {
		return e, errors.New("缺少端口（形如 1.2.3.4:8080）")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return e, fmt.Errorf("端口非法: %s", port)
	}

	// host:port:user:pass —— 代理池最常见的导出格式，补齐账号密码
	if user == nil && len(restParts) >= 2 {
		user = url.UserPassword(restParts[0], restParts[1])
	}

	u := &url.URL{Scheme: scheme, Host: net.JoinHostPort(host, port)}
	if user != nil {
		u.User = user
	}

	e.Addr = u.Host
	e.Scheme = scheme
	e.Auth = user != nil
	e.URL = u
	return e, nil
}

// splitHostPort 拆 host:port，兼容 IPv6 的 [::1]:8080，
// 并把多余的冒号段原样返回（给 host:port:user:pass 用）
func splitHostPort(s string) (host, port string, rest []string) {
	if strings.HasPrefix(s, "[") {
		if i := strings.Index(s, "]"); i > 0 {
			host = s[1:i]
			tail := strings.TrimPrefix(s[i+1:], ":")
			parts := strings.Split(tail, ":")
			if len(parts) > 0 {
				port = parts[0]
			}
			if len(parts) > 1 {
				rest = parts[1:]
			}
			return host, port, rest
		}
	}
	parts := strings.Split(s, ":")
	if len(parts) > 0 {
		host = parts[0]
	}
	if len(parts) > 1 {
		port = parts[1]
	}
	if len(parts) > 2 {
		rest = parts[2:]
	}
	return host, port, rest
}

// ============================================================
// 单次请求测量
// ============================================================

// measureOnce 通过 proxyURL 请求一次 target 并拆解耗时。
// proxyURL 传 nil 表示直连——本地基线就是靠这个算出来的。
//
// 关键细节：每轮都新建 Transport 并 DisableKeepAlives。
// 如果复用连接，第二轮起省掉了 TCP/TLS 握手，"延迟"会好看得离谱，
// 但用户真正关心的是冷启动一次要多久。
func measureOnce(ctx context.Context, target string, proxyURL *url.URL, timeout time.Duration) RoundResult {
	var rr RoundResult

	var (
		start                 = time.Now()
		connStart, tlsStart   time.Time
		connMs, tlsMs, ttfbMs float64
	)
	ms := func(t time.Time) float64 {
		if t.IsZero() {
			return 0
		}
		// 用纳秒换算而不是 Microseconds()：本机回环建连可能只要零点几微秒，
		// 截断到整微秒会直接变成 0，表格里就出现"连接 0ms"这种看着像 bug 的数
		return float64(time.Since(t).Nanoseconds()) / 1e6
	}

	tr := &http.Transport{
		// 显式设置 Proxy：nil 表示直连。
		// 不能留空依赖环境变量——这台机器上如果配了 HTTP_PROXY，
		// "本地基线"会悄悄走代理，对比就彻底失去意义了
		Proxy:               nil,
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: timeout,
		DialContext:         (&net.Dialer{Timeout: timeout, KeepAlive: -1}).DialContext,
	}
	if proxyURL != nil {
		tr.Proxy = http.ProxyURL(proxyURL)
	}
	defer tr.CloseIdleConnections()

	trace := &httptrace.ClientTrace{
		ConnectStart: func(network, addr string) { connStart = time.Now() },
		ConnectDone: func(network, addr string, err error) {
			if err == nil {
				connMs = ms(connStart)
			}
		},
		TLSHandshakeStart: func() { tlsStart = time.Now() },
		TLSHandshakeDone: func(state tls.ConnectionState, err error) {
			if err == nil {
				tlsMs = ms(tlsStart)
			}
		},
		GotFirstResponseByte: func() {
			ttfbMs = float64(time.Since(start).Nanoseconds()) / 1e6
		},
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target, nil)
	if err != nil {
		rr.Err = "目标 URL 非法: " + err.Error()
		return rr
	}
	req = req.WithContext(httptrace.WithClientTrace(reqCtx, trace))
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Cache-Control", "no-cache")

	client := &http.Client{
		Transport: tr,
		Timeout:   timeout,
		// 不跟随跳转：3xx 也说明链路通畅，跳过去反而把延迟算多了
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		rr.Err = normalizeErr(err, timeout)
		rr.TotalMs = ms(start)
		return rr
	}
	defer resp.Body.Close()

	head, total, err := readHead(resp.Body, maxBodyRead)
	rr.TotalMs = float64(time.Since(start).Nanoseconds()) / 1e6
	rr.Status = resp.StatusCode
	rr.Bytes = total
	rr.Snippet = clipText(head, snippetRunes)
	rr.Kind = detectKind(resp.Header.Get("Content-Type"), head)

	if err != nil && total == 0 {
		rr.Err = normalizeErr(err, timeout)
		return rr
	}

	rr.OK = true
	rr.TTFBMs = ttfbMs
	rr.ConnMs = connMs
	rr.TLSMs = tlsMs
	return rr
}

// readHead 读响应体，最多 max 字节，同时留下开头一段用于判断返回内容
func readHead(r io.Reader, max int64) (head []byte, total int64, err error) {
	buf := make([]byte, 4096)
	lr := io.LimitReader(r, max)
	for {
		n, rerr := lr.Read(buf)
		if n > 0 {
			total += int64(n)
			if len(head) < 300 {
				keep := 300 - len(head)
				if n < keep {
					keep = n
				}
				head = append(head, buf[:keep]...)
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return head, total, nil
			}
			return head, total, rerr
		}
	}
}

// detectKind 粗判响应体类型，前端据此显示 JSON / HTML / 空
func detectKind(ctype string, head []byte) string {
	t := strings.TrimSpace(string(head))
	switch {
	case t == "":
		return "empty"
	case strings.HasPrefix(t, "{"), strings.HasPrefix(t, "["):
		return "json"
	case strings.Contains(strings.ToLower(ctype), "html"),
		strings.HasPrefix(strings.ToLower(t), "<!doctype"), strings.HasPrefix(t, "<"):
		return "html"
	default:
		return "text"
	}
}

// clipText 压成一行并截断，避免把换行/超长内容塞进 JSON 和表格
func clipText(b []byte, max int) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if rs := []rune(s); len(rs) > max {
		return string(rs[:max]) + "…"
	}
	return s
}

// normalizeErr 把底层错误翻成用户能看懂的一句话。
//
// 匹配顺序有讲究：Windows 上"代理端口没开"的原文是
//
//	proxyconnect tcp: dial tcp 127.0.0.1:1: connectex: No connection could be made
//	because the target machine actively refused it.
//
// 既没有 "connection refused"，又带着 proxyconnect。
// 所以必须先认 refused/connectex，再兜底 proxyconnect，
// 否则一律会被误标成"代理认证失败（407）"。
func normalizeErr(err error, timeout time.Duration) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	low := strings.ToLower(msg)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf("超时（>%dms）", timeout.Milliseconds())
	case errors.Is(err, context.Canceled):
		return "已取消"
	case strings.Contains(low, "timeout"), strings.Contains(low, "timed out"):
		return fmt.Sprintf("超时（>%dms）", timeout.Milliseconds())
	case strings.Contains(low, "407"), strings.Contains(low, "proxy authentication required"):
		return "代理认证失败（407）"
	case strings.Contains(low, "refused"), strings.Contains(low, "connectex"):
		return "拒绝连接（端口没开）"
	case strings.Contains(low, "socks"):
		return "SOCKS 握手失败"
	case strings.Contains(low, "no such host"), strings.Contains(low, "server misbehaving"),
		strings.Contains(low, "dns"):
		return "域名解析失败"
	case strings.Contains(low, "reset"), strings.Contains(low, "eof"), strings.Contains(low, "broken pipe"):
		return "连接被重置"
	case strings.Contains(low, "certificate"), strings.Contains(low, "x509"), strings.Contains(low, "tls"):
		return "TLS 握手失败（证书/协议不兼容）"
	case strings.Contains(low, "network is unreachable"), strings.Contains(low, "no route"):
		return "网络不可达"
	case strings.Contains(low, "proxyconnect"):
		return "与代理握手失败"
	}
	if len(msg) > 140 {
		msg = msg[:140] + "…"
	}
	return msg
}

// ============================================================
// 汇总 + 并发调度
// ============================================================

// testOne 跑完一个代理的所有轮次，取最优轮做展示值
func testOne(ctx context.Context, e ProxyEntry, opt testOptions) TestResult {
	res := TestResult{
		Line:   e.Line,
		Raw:    e.Raw,
		Addr:   e.Addr,
		Scheme: e.Scheme,
		Auth:   e.Auth,
		Rounds: opt.Rounds,
	}
	begin := time.Now()
	var sum float64

	for i := 0; i < opt.Rounds; i++ {
		if ctx.Err() != nil {
			break
		}
		rr := measureOnce(ctx, opt.Target, e.URL, opt.Timeout)

		if rr.OK {
			res.OKRounds++
			sum += rr.TotalMs
			// 用"最快那一轮"的明细做展示：抖动大的代理，最好成绩
			// 最能代表它的真实上行能力，平均值容易被偶尔的抽风拉长
			if res.MinMs == 0 || rr.TotalMs < res.MinMs {
				res.MinMs = rr.TotalMs
				res.TTFBMs = rr.TTFBMs
				res.ConnMs = rr.ConnMs
				res.TLSMs = rr.TLSMs
				res.Status = rr.Status
				res.Kind = rr.Kind
				res.Bytes = rr.Bytes
				res.Snippet = rr.Snippet
			}
		} else if res.Error == "" {
			res.Error = rr.Err
			if rr.Status != 0 {
				res.Status = rr.Status
			}
		}

		// 多轮之间隔一小会儿：连着打两枪容易撞在同一个网络抖动窗口里
		if i < opt.Rounds-1 && ctx.Err() == nil {
			select {
			case <-ctx.Done():
			case <-time.After(120 * time.Millisecond):
			}
		}
	}

	if res.OKRounds > 0 {
		res.OK = true
		res.AvgMs = round2(sum / float64(res.OKRounds))
		res.MinMs = round2(res.MinMs)
		res.Error = "" // 部分轮失败不算失败，OKRounds/Rounds 已经说明了
	} else {
		res.MinMs = 0
	}
	res.ElapsedMs = time.Since(begin).Milliseconds()
	return res
}

// runTest 并发测试所有代理，每测完一个就通过 emit 交出去。
// emit 会被多个 goroutine 调用，串行化由调用方负责。
func runTest(ctx context.Context, entries []ProxyEntry, opt testOptions, emit func(TestResult)) {
	sem := make(chan struct{}, opt.Concurrency)
	var wg sync.WaitGroup

	for _, e := range entries {
		if ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case sem <- struct{}{}:
		}
		if ctx.Err() != nil {
			break
		}

		wg.Add(1)
		go func(e ProxyEntry) {
			defer wg.Done()
			defer func() { <-sem }()
			emit(testOne(ctx, e, opt))
		}(e)
	}
	wg.Wait()
}

// measureLocal 测本地直连基线（proxyURL 为 nil），返回与代理同构的结果
func measureLocal(ctx context.Context, opt testOptions) TestResult {
	res := TestResult{
		Line:   0,
		Raw:    "本地直连",
		Addr:   "local",
		Scheme: "direct",
		Rounds: opt.Rounds,
	}
	begin := time.Now()
	var sum float64

	for i := 0; i < opt.Rounds; i++ {
		if ctx.Err() != nil {
			break
		}
		rr := measureOnce(ctx, opt.Target, nil, opt.Timeout)
		if rr.OK {
			res.OKRounds++
			sum += rr.TotalMs
			if res.MinMs == 0 || rr.TotalMs < res.MinMs {
				res.MinMs = rr.TotalMs
				res.TTFBMs = rr.TTFBMs
				res.ConnMs = rr.ConnMs
				res.TLSMs = rr.TLSMs
				res.Status = rr.Status
				res.Kind = rr.Kind
				res.Bytes = rr.Bytes
				res.Snippet = rr.Snippet
			}
		} else if res.Error == "" {
			res.Error = rr.Err
		}
		if i < opt.Rounds-1 && ctx.Err() == nil {
			select {
			case <-ctx.Done():
			case <-time.After(120 * time.Millisecond):
			}
		}
	}

	if res.OKRounds > 0 {
		res.OK = true
		res.AvgMs = round2(sum / float64(res.OKRounds))
		res.MinMs = round2(res.MinMs)
		res.Error = ""
	} else {
		res.MinMs = 0
	}
	res.ElapsedMs = time.Since(begin).Milliseconds()
	return res
}

// round2 保留两位小数：毫秒精度到 0.01 已经远超实际抖动，再长纯属噪音
func round2(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}

// normalizeOptions 归一化前端传来的参数，异常值一律回落到默认值。
// 这些值会被直接用于建连和并发，不夹紧的话一次误操作就能把机器打满
func normalizeOptions(target string, timeoutMs, rounds, concurrency, total int) testOptions {
	if strings.TrimSpace(target) == "" {
		target = defaultTargetURL
	}
	target = strings.TrimSpace(target)

	timeout := 8000
	if timeoutMs >= 1000 && timeoutMs <= 30000 {
		timeout = timeoutMs
	}

	if rounds < 1 {
		rounds = 1
	}
	if rounds > 5 {
		rounds = 5
	}

	if concurrency < 1 {
		concurrency = 8
	}
	if concurrency > 64 {
		concurrency = 64
	}
	// 并发数不该超过待测条目数，否则只是白开 goroutine
	if total > 0 && concurrency > total {
		concurrency = total
	}

	return testOptions{
		Target:      target,
		Timeout:     time.Duration(timeout) * time.Millisecond,
		Rounds:      rounds,
		Concurrency: concurrency,
	}
}
