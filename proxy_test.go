package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ============================================================
// 输入解析
//
// 这一层最容易出问题：代理池导出的格式五花八门，用户也常常直接粘
// 带协议的 URL 或带路径的地址。解析错了不会报错，只会让代理"测不通"，
// 排查成本极高，所以这里把常见写法全钉死。
// ============================================================

func TestParseProxyLine(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantAddr   string
		wantScheme string
		wantUser   string
		wantPass   string
		wantErr    string // 非空表示期望报错，且错误信息包含该子串
	}{
		{name: "裸 ip:port", in: "1.2.3.4:8080", wantAddr: "1.2.3.4:8080", wantScheme: "http"},
		{name: "代理池导出 ip:port:user:pass", in: "1.2.3.4:8080:admin:123456",
			wantAddr: "1.2.3.4:8080", wantScheme: "http", wantUser: "admin", wantPass: "123456"},
		{name: "userinfo 写法", in: "admin:123456@1.2.3.4:8080",
			wantAddr: "1.2.3.4:8080", wantScheme: "http", wantUser: "admin", wantPass: "123456"},
		{name: "http 前缀", in: "http://1.2.3.4:8080", wantAddr: "1.2.3.4:8080", wantScheme: "http"},
		{name: "https 前缀大小写", in: "HTTPS://1.2.3.4:8443", wantAddr: "1.2.3.4:8443", wantScheme: "https"},
		{name: "socks5 带认证", in: "socks5://admin:123456@1.2.3.4:1080",
			wantAddr: "1.2.3.4:1080", wantScheme: "socks5", wantUser: "admin", wantPass: "123456"},
		{name: "socks5h", in: "socks5h://1.2.3.4:1080", wantAddr: "1.2.3.4:1080", wantScheme: "socks5h"},
		{name: "带路径和查询串", in: "http://1.2.3.4:8080/path?x=1", wantAddr: "1.2.3.4:8080", wantScheme: "http"},
		{name: "IPv6", in: "[::1]:8080", wantAddr: "[::1]:8080", wantScheme: "http"},
		{name: "IPv6 带认证", in: "socks5://u:p@[::1]:1080",
			wantAddr: "[::1]:1080", wantScheme: "socks5", wantUser: "u", wantPass: "p"},

		{name: "缺端口", in: "1.2.3.4", wantErr: "缺少端口"},
		{name: "缺主机", in: ":8080", wantErr: "缺少主机地址"},
		{name: "端口越界", in: "1.2.3.4:99999", wantErr: "端口非法"},
		{name: "端口为 0", in: "1.2.3.4:0", wantErr: "端口非法"},
		{name: "端口不是数字", in: "1.2.3.4:abc", wantErr: "端口非法"},
		{name: "协议不支持", in: "ftp://1.2.3.4:21", wantErr: "不支持的协议"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, err := parseProxyLine(1, c.in)

			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("%q 应该报错，却解析成功: addr=%s", c.in, e.Addr)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("错误信息不含 %q，实际: %v", c.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("%q 解析失败: %v", c.in, err)
			}
			if e.Addr != c.wantAddr {
				t.Errorf("addr = %q, 期望 %q", e.Addr, c.wantAddr)
			}
			if e.Scheme != c.wantScheme {
				t.Errorf("scheme = %q, 期望 %q", e.Scheme, c.wantScheme)
			}

			wantAuth := c.wantUser != ""
			if e.Auth != wantAuth {
				t.Errorf("auth = %v, 期望 %v", e.Auth, wantAuth)
			}
			if !wantAuth {
				return
			}
			if got := e.URL.User.Username(); got != c.wantUser {
				t.Errorf("user = %q, 期望 %q", got, c.wantUser)
			}
			if got, _ := e.URL.User.Password(); got != c.wantPass {
				t.Errorf("password = %q, 期望 %q", got, c.wantPass)
			}
		})
	}
}

func TestParseProxyText(t *testing.T) {
	text := strings.Join([]string{
		"# 这是注释，应被忽略",
		"1.2.3.4:8080",
		"",
		"  http://1.2.3.4:8080  ", // 与第一条规范化后相同 → 视为重复
		"5.6.7.8:3128:admin:123456",
		"// 另一种注释写法",
		"bad-line",
	}, "\n")

	entries, bad, dup, truncated := parseProxyText(text)

	if len(entries) != 2 {
		t.Fatalf("有效条目 = %d, 期望 2（%+v）", len(entries), entries)
	}
	if dup != 1 {
		t.Errorf("重复计数 = %d, 期望 1", dup)
	}
	if truncated {
		t.Error("不该触发截断")
	}
	if len(bad) != 1 {
		t.Fatalf("错误行 = %d, 期望 1（%+v）", len(bad), bad)
	}
	// 行号必须跟编辑器里看到的一致，否则用户根本找不到是哪一行
	if bad[0].Line != 7 {
		t.Errorf("错误行号 = %d, 期望 7", bad[0].Line)
	}
	if bad[0].Raw != "bad-line" {
		t.Errorf("错误行原文 = %q", bad[0].Raw)
	}
}

func TestParseProxyTextTruncate(t *testing.T) {
	var b strings.Builder
	for i := 0; i < maxProxyCount+10; i++ {
		// 用不同端口避免被去重
		fmt.Fprintf(&b, "10.0.%d.%d:8080\n", i/250, i%250)
	}

	entries, _, _, truncated := parseProxyText(b.String())

	if len(entries) != maxProxyCount {
		t.Errorf("条目数 = %d, 期望封顶 %d", len(entries), maxProxyCount)
	}
	if !truncated {
		t.Error("超过上限时应标记 truncated，前端要据此提示用户")
	}
}

// ============================================================
// 错误归一化
//
// 顺序敏感：Windows 上"代理端口没开"的原文里既没有 connection refused，
// 又带着 proxyconnect，一旦先匹配 proxyconnect 就会被误标成 407。
// ============================================================

func TestNormalizeErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "Windows 拒绝连接",
			err: errors.New(`Get "https://example.com": proxyconnect tcp: dial tcp 127.0.0.1:1: ` +
				`connectex: No connection could be made because the target machine actively refused it.`),
			want: "拒绝连接",
		},
		{
			name: "Linux 拒绝连接",
			err:  errors.New("proxyconnect tcp: dial tcp 127.0.0.1:1: connect: connection refused"),
			want: "拒绝连接",
		},
		{
			name: "客户端超时",
			err:  errors.New("context deadline exceeded (Client.Timeout exceeded while awaiting headers)"),
			want: "超时",
		},
		{name: "ctx 超时", err: context.DeadlineExceeded, want: "超时"},
		{name: "手动取消", err: context.Canceled, want: "已取消"},
		{
			name: "407 认证",
			err:  errors.New("proxyconnect tcp: 407 Proxy Authentication Required"),
			want: "代理认证失败",
		},
		{
			name: "SOCKS 握手失败",
			err:  errors.New("socks connect tcp 1.2.3.4:1080->example.com:443: unexpected EOF"),
			want: "SOCKS",
		},
		{
			name: "域名解析失败",
			err:  errors.New("dial tcp: lookup proxy.example.com on 8.8.8.8:53: no such host"),
			want: "域名解析失败",
		},
		{
			name: "连接被重置",
			err:  errors.New("read tcp 10.0.0.1:5000->1.2.3.4:8080: connection reset by peer"),
			want: "连接被重置",
		},
		{
			name: "证书错误",
			err:  errors.New("tls: failed to verify certificate: x509: certificate signed by unknown authority"),
			want: "TLS",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := normalizeErr(c.err, 8*time.Second)
			if !strings.Contains(got, c.want) {
				t.Errorf("normalizeErr = %q, 期望包含 %q", got, c.want)
			}
		})
	}

	if got := normalizeErr(nil, time.Second); got != "" {
		t.Errorf("nil 错误应返回空串，实际 %q", got)
	}
}

// ============================================================
// 响应判定 / 参数夹紧
// ============================================================

func TestDetectKind(t *testing.T) {
	cases := []struct {
		ctype string
		head  string
		want  string
	}{
		{"application/json; charset=utf-8", `{"code":410}`, "json"},
		{"application/json", `[1,2,3]`, "json"},
		{"text/html", `<!DOCTYPE html><html>`, "html"},
		{"", `<html><body>x`, "html"},
		{"text/plain", "hello", "text"},
		{"application/json", "", "empty"},
		{"application/json", "   \n  ", "empty"},
	}
	for _, c := range cases {
		if got := detectKind(c.ctype, []byte(c.head)); got != c.want {
			t.Errorf("detectKind(%q, %q) = %q, 期望 %q", c.ctype, c.head, got, c.want)
		}
	}
}

// 目标 URL 是必填项，没有内置默认值
func TestNormalizeTarget(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr string
	}{
		{name: "完整地址原样保留", in: "https://a.example/api/x?y=1", want: "https://a.example/api/x?y=1"},
		{name: "去掉首尾空白", in: "  https://a.example/x  ", want: "https://a.example/x"},
		{name: "缺协议自动补 https", in: "a.example/api", want: "https://a.example/api"},
		{name: "http 也允许", in: "http://a.example:8080/ping", want: "http://a.example:8080/ping"},
		{name: "空串报错", in: "", wantErr: "请先填写"},
		{name: "纯空白报错", in: "   \n ", wantErr: "请先填写"},
		{name: "不是地址", in: "http://", wantErr: "不是合法地址"},
		{name: "协议不支持", in: "ftp://a.example/x", wantErr: "只支持 http/https"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := normalizeTarget(c.in)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("%q 应该报错，却通过并返回 %q", c.in, got)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("错误信息不含 %q，实际: %v", c.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%q 不该报错: %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("normalizeTarget = %q, 期望 %q", got, c.want)
			}
		})
	}
}

func TestNormalizeOptions(t *testing.T) {
	const target = "https://a.example/x"

	cases := []struct {
		name                            string
		target                          string
		timeoutMs, rounds, concv, total int
		wantTarget                      string
		wantTimeout                     time.Duration
		wantRounds, wantConc            int
		wantErr                         bool
	}{
		{
			name:   "目标是必填",
			target: "  ", timeoutMs: 0, rounds: 0, concv: 0, total: 100,
			wantErr: true,
		},
		{
			name:   "其余非法值回落默认",
			target: target, timeoutMs: 0, rounds: 0, concv: 0, total: 100,
			wantTarget: target, wantTimeout: 8 * time.Second, wantRounds: 1, wantConc: 8,
		},
		{
			name:   "合法值原样保留",
			target: target, timeoutMs: 5000, rounds: 3, concv: 16, total: 100,
			wantTarget: target, wantTimeout: 5 * time.Second, wantRounds: 3, wantConc: 16,
		},
		{
			name:   "缺协议时补 https",
			target: "a.example", timeoutMs: 8000, rounds: 1, concv: 4, total: 10,
			wantTarget: "https://a.example", wantTimeout: 8 * time.Second, wantRounds: 1, wantConc: 4,
		},
		{
			name:   "上限夹紧",
			target: target, timeoutMs: 999999, rounds: 9, concv: 999, total: 100,
			wantTarget: target, wantTimeout: 8 * time.Second, wantRounds: 5, wantConc: 64,
		},
		{
			name:   "并发不超过待测条目数",
			target: target, timeoutMs: 8000, rounds: 1, concv: 32, total: 3,
			wantTarget: target, wantTimeout: 8 * time.Second, wantRounds: 1, wantConc: 3,
		},
		{
			name:   "超时下限夹紧",
			target: target, timeoutMs: 10, rounds: 1, concv: 4, total: 10,
			wantTarget: target, wantTimeout: 8 * time.Second, wantRounds: 1, wantConc: 4,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := normalizeOptions(c.target, c.timeoutMs, c.rounds, c.concv, c.total)
			if c.wantErr {
				if err == nil {
					t.Fatal("应该返回错误，却通过了")
				}
				return
			}
			if err != nil {
				t.Fatalf("不该报错: %v", err)
			}
			if got.Target != c.wantTarget {
				t.Errorf("Target = %q, 期望 %q", got.Target, c.wantTarget)
			}
			if got.Timeout != c.wantTimeout {
				t.Errorf("Timeout = %v, 期望 %v", got.Timeout, c.wantTimeout)
			}
			if got.Rounds != c.wantRounds {
				t.Errorf("Rounds = %d, 期望 %d", got.Rounds, c.wantRounds)
			}
			if got.Concurrency != c.wantConc {
				t.Errorf("Concurrency = %d, 期望 %d", got.Concurrency, c.wantConc)
			}
		})
	}
}

func TestReadHead(t *testing.T) {
	// 超过上限时只读到 max，但 head 只留开头 300 字节
	body := strings.Repeat("x", 1000)
	head, total, err := readHead(strings.NewReader(body), 500)
	if err != nil {
		t.Fatalf("readHead 出错: %v", err)
	}
	if total != 500 {
		t.Errorf("读取字节 = %d, 期望 500（应被 LimitReader 截断）", total)
	}
	if len(head) != 300 {
		t.Errorf("head 长度 = %d, 期望 300", len(head))
	}

	head, total, err = readHead(strings.NewReader("hi"), 500)
	if err != nil || total != 2 || string(head) != "hi" {
		t.Errorf("小响应读取异常: head=%q total=%d err=%v", head, total, err)
	}
}

func TestClipText(t *testing.T) {
	// 换行/连续空白要压成一行，否则表格里会被撑开
	if got := clipText([]byte("a\n\n  b\t c"), 50); got != "a b c" {
		t.Errorf("clipText = %q, 期望 %q", got, "a b c")
	}
	// 按 rune 截断：中文不能被截成半个字
	got := clipText([]byte(strings.Repeat("中", 10)), 4)
	if got != "中中中中…" {
		t.Errorf("clipText = %q", got)
	}
}

// round2 用的是"乘 100 四舍五入再除"，注意不要被浮点误差坑到
func TestRound2(t *testing.T) {
	cases := map[float64]float64{1.234: 1.23, 1.235: 1.24, 0: 0, 199.999: 200}
	for in, want := range cases {
		if got := round2(in); got != want {
			t.Errorf("round2(%v) = %v, 期望 %v", in, got, want)
		}
	}
}
