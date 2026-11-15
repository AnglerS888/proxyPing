package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// ============================================================
// HTTP 处理器
// ============================================================

// maxInputBytes 请求体上限。500 条代理 × 每条 80 字节 ≈ 40KB，
// 给到 4MB 是留给"粘错了"的场景：宁可拒绝也不要静默截断
const maxInputBytes = 4 << 20

// streamEvent 流式响应的一行。
// 前端按 type 分发：meta 打底、local 是本地基线、result 是单个代理结果、done 收尾
type streamEvent struct {
	Type       string       `json:"type"`
	Target     string       `json:"target,omitempty"`
	Total      int          `json:"total,omitempty"`
	Done       int          `json:"done,omitempty"`
	Duplicated int          `json:"duplicated,omitempty"`
	Truncated  bool         `json:"truncated,omitempty"`
	Invalid    []parseError `json:"invalid,omitempty"`
	Local      *TestResult  `json:"local,omitempty"`
	Result     *TestResult  `json:"result,omitempty"`
	Summary    *testSummary `json:"summary,omitempty"`
	Msg        string       `json:"msg,omitempty"`
}

// testSummary 收尾统计：前端只做展示，不再自己算一遍
type testSummary struct {
	Total     int     `json:"total"`
	OK        int     `json:"ok"`
	Fail      int     `json:"fail"`
	ElapsedMs int64   `json:"elapsedMs"`
	Fastest   string  `json:"fastest"`
	FastestMs float64 `json:"fastestMs"`
	Stopped   bool    `json:"stopped"`
}

// handleSpeedTest 批量测速（流式 NDJSON）
// POST /api/test
// body: {"proxies":"...","target":"...","timeoutMs":8000,"rounds":1,"concurrency":8}
//
// 之所以做成流式而不是"憋一个 JSON 数组"：500 个代理最坏要跑几分钟，
// 一次性返回的话页面全程空白，用户只能猜是不是卡住了。
// 现在每测完一个就吐一行，表格是实时长出来的。
func handleSpeedTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "仅支持 POST", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxInputBytes)

	var req struct {
		Proxies     string `json:"proxies"`
		Target      string `json:"target"`
		TimeoutMs   int    `json:"timeoutMs"`
		Rounds      int    `json:"rounds"`
		Concurrency int    `json:"concurrency"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]interface{}{"code": -1, "msg": "请求体解析失败: " + err.Error()})
		return
	}

	entries, bad, dup, truncated := parseProxyText(req.Proxies)
	if len(entries) == 0 {
		writeJSON(w, map[string]interface{}{
			"code":    -1,
			"msg":     "没有可测的代理，请检查输入格式（例如 1.2.3.4:8080）",
			"invalid": bad,
		})
		return
	}

	opt := normalizeOptions(req.Target, req.TimeoutMs, req.Rounds, req.Concurrency, len(entries))

	// ---------- 流式响应头 ----------
	h := w.Header()
	h.Set("Content-Type", "application/x-ndjson; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	// X-Accel-Buffering 给反代用：不禁用的话 nginx 会攒够一个缓冲块才往下发
	h.Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)

	var writeMu sync.Mutex
	emit := func(ev streamEvent) {
		b, err := json.Marshal(ev)
		if err != nil {
			return
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		w.Write(b)
		w.Write([]byte("\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}

	// 客户端断开（点了"停止"或关页面）时 r.Context() 会被取消，
	// 下面的 measureOnce 全部带着这个 ctx，会立刻收手
	ctx := r.Context()

	logMsg("[test] 开始测速 | 代理=%d 条 | 并发=%d | 超时=%dms | 轮次=%d | 目标=%s",
		len(entries), opt.Concurrency, opt.Timeout.Milliseconds(), opt.Rounds, opt.Target)

	emit(streamEvent{
		Type:       "meta",
		Target:     opt.Target,
		Total:      len(entries),
		Duplicated: dup,
		Truncated:  truncated,
		Invalid:    bad,
	})

	// ---------- 本地直连基线 ----------
	// 先测本地：这样后面的代理结果一到，前端立刻能算出"比本地慢多少"
	local := measureLocal(ctx, opt)
	emit(streamEvent{Type: "local", Local: &local})
	if local.OK {
		logMsg("[local] ✓ 本地基线 | 平均=%.2fms | TTFB=%.2fms | 状态=%d",
			local.AvgMs, local.TTFBMs, local.Status)
	} else {
		logMsg("[local] ✗ 本地基线失败: %s", local.Error)
	}

	// ---------- 代理并发测速 ----------
	start := time.Now()
	var (
		mu      sync.Mutex
		done    int
		okCount int
		fastest *TestResult
	)

	runTest(ctx, entries, opt, func(res TestResult) {
		mu.Lock()
		done++
		if res.OK {
			okCount++
			if fastest == nil || res.AvgMs < fastest.AvgMs {
				r := res
				fastest = &r
			}
		}
		n := done
		mu.Unlock()

		emit(streamEvent{Type: "result", Done: n, Total: len(entries), Result: &res})
	})

	// ---------- 收尾 ----------
	elapsed := time.Since(start).Milliseconds()
	summary := &testSummary{Total: len(entries), OK: okCount, Fail: len(entries) - okCount, ElapsedMs: elapsed}
	if fastest != nil {
		summary.Fastest = fastest.Addr
		summary.FastestMs = fastest.AvgMs
	}
	if ctx.Err() != nil {
		summary.Stopped = true
	}

	emit(streamEvent{Type: "done", Done: done, Total: len(entries), Summary: summary})

	if summary.Stopped {
		logMsg("[test] 已中止 | 已测=%d/%d | 成功=%d 失败=%d | 耗时=%dms",
			done, len(entries), summary.OK, summary.Fail, elapsed)
		return
	}
	logMsg("[test] 结束 | 成功=%d 失败=%d | 耗时=%dms", summary.OK, summary.Fail, elapsed)
}

// handleLocalPing 只测本地直连基线，供页面加载时先把"参照物"摆出来
// GET /api/local?rounds=3&timeoutMs=8000&target=...
func handleLocalPing(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rounds, _ := strconv.Atoi(q.Get("rounds"))
	timeoutMs, _ := strconv.Atoi(q.Get("timeoutMs"))
	opt := normalizeOptions(q.Get("target"), timeoutMs, rounds, 1, 1)

	local := measureLocal(r.Context(), opt)
	writeJSON(w, map[string]interface{}{
		"code":  0,
		"local": local,
	})
}

// writeJSON 写入 JSON 响应
func writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	json.NewEncoder(w).Encode(data)
}

// logMsg 记录并打印日志
func logMsg(format string, args ...interface{}) {
	fmt.Printf("[%s] %s\n", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, args...))
}
