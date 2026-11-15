<div align="center">

<img src="docs/logo.svg" width="96" alt="ProxyPing">

# 代理测速台 · ProxyPing

把一堆代理粘进来，量出每个的真实延迟，和本机直连摆在一起对比。
单个二进制或一个容器跑在自己的机器上，不注册、不上传。

[![Version](https://img.shields.io/badge/version-1.0.0-58a6ff)](https://github.com/AnglerS888/proxyPing)
[![Gitee](https://img.shields.io/badge/Gitee-anglers%2FproxyPing-C71D23?logo=gitee&logoColor=white)](https://gitee.com/anglers/proxyPing)
[![GitHub](https://img.shields.io/badge/GitHub-AnglerS888%2FproxyPing-181717?logo=github&logoColor=white)](https://github.com/AnglerS888/proxyPing)
[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white)](https://github.com/AnglerS888/proxyPing)
[![Docker](https://img.shields.io/badge/Docker-%E6%94%AF%E6%8C%81-2496ED?logo=docker&logoColor=white)](#安装)

[一键部署](#一键部署) · [功能](#功能) · [界面预览](#界面预览) · [安装](#安装) · [怎么用](#怎么用) · [代理格式](#支持的代理格式) · [结果怎么看](#结果怎么看) · [常见问题](#常见问题) · [接口](#接口)

<img src="docs/screenshot-home.png" width="880" alt="代理测速台首页">

<sub>粘贴代理 → 点「开始测速」→ 谁快谁慢一目了然，还带着「比本地直连慢多少」</sub>

</div>

## 一键部署

服务器上装好 Docker，复制这一行就能用：

```bash
curl -fsSL http://git.danran0.cc/danran/proxyPing/raw/branch/main/bin/deploy.sh | bash -s deploy
```

脚本会自己下载源码 → 构建镜像 → 起容器 → 健康检查，装完打开 **`http://服务器IP:8899/`**。
**再执行一次就是升级**，新版本起不来会自动回滚到上一版。

<sub>访问不到这个仓库的机器，把地址换成 Gitee 或 GitHub 源即可，见 [安装](#安装) 一节。</sub>

---

## 功能

- **批量测速** —— 一行一个代理，粘多少测多少，边测边出结果，不用等全部跑完
- **本地基线对比** —— 同时测一次本机直连，每条代理都标出「相对本地 ±xx ms」，快慢有参照
- **延迟拆开看** —— 连接、TLS 握手、首字节（TTFB）、总耗时分别列出，卡在哪一环一眼看出
- **失败也说清楚** —— 超时、拒绝连接、SOCKS 握手失败、407、证书错误、403 被挡，逐条给原因
- **目标随便填** —— 填你自己要访问的接口地址，不限制站点
- **可调参数** —— 并发、超时、每 IP 轮次都能改，跑偏了随时「停止」
- **结果可带走** —— 按延迟 / 输入顺序 / 状态码排序，一键导出 CSV（Excel 直接打开不乱码）
- **深色主题、手机能用** —— 窄屏自动收成单列
- **部署简单** —— 页面和样式都编进二进制了，部署只有一个可执行文件；也可以直接 docker run

## 界面预览

**输入区 —— 代理列表 + 目标地址 + 跑分参数**

<img src="docs/screenshot-input.png" width="880" alt="输入区">

**结果表格 —— 平均延迟带条形图，相对本地差值标红标绿**

<img src="docs/screenshot-result.png" width="880" alt="测速结果">

**原始输出 —— 每完成一条打一行，出问题时对着日志看**

<img src="docs/screenshot-log.png" width="880" alt="原始输出">

**深色主题**

<img src="docs/screenshot-dark.png" width="880" alt="深色主题">

**手机上的样子**

<img src="docs/screenshot-mobile-home.png" width="300" alt="移动端">

> 截图里的 IP 和延迟是演示数据，用来展示各状态长什么样，不代表任何真实代理。

## 安装

装好后浏览器打开 `http://服务器IP:8899/`。
源码同步在 [Gitee](https://gitee.com/anglers/proxyPing) 和 [GitHub](https://github.com/AnglerS888/proxyPing)，两边内容一样，挑下得动的用。

### 方式一：一键部署（Linux + Docker，推荐）

命令就是[页首](#一键部署)那条，默认从自建仓库拉代码。那台机器访问不到时换公共源：

```bash
# Gitee 源
curl -fsSL https://gitee.com/anglers/proxyPing/raw/main/bin/deploy.sh | bash -s deploy

# GitHub 源
curl -fsSL https://raw.githubusercontent.com/AnglerS888/proxyPing/main/bin/deploy.sh | bash -s deploy
```

想改脚本或者反复部署，克隆下来再跑更顺手：

```bash
git clone https://gitee.com/anglers/proxyPing.git   # 或 https://github.com/AnglerS888/proxyPing.git
cd proxyPing
bash bin/deploy.sh
```

服务器上只需要 `docker`、`curl`、`tar`；不需要装 git，也不需要仓库权限。
脚本默认按 **Gitee → GitHub** 的顺序取源码，哪个能下用哪个。

常用参数：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `PORT` | `8899` | 宿主机端口；容器内始终 8899 |
| `BRANCH` | `main` | 部署哪个分支 |
| `SOURCE` | 自动 | 只用某个源码源：`gitee` / `github` |
| `NO_CACHE` | `0` | 设为 `1` 则 `docker build --no-cache` |
| `HEALTH_TIMEOUT` | `20` | 健康检查最多等几秒，机器慢就调大 |

```bash
PORT=9099 bash bin/deploy.sh          # 换对外端口
NO_CACHE=1 bash bin/deploy.sh         # 改过 Dockerfile 时用
```

装好之后还有几个子命令：

```bash
bash bin/deploy.sh logs      # 跟踪容器日志
bash bin/deploy.sh status    # 容器状态 + 健康接口
bash bin/deploy.sh restart   # 重启
bash bin/deploy.sh stop      # 删掉容器（镜像保留）
```

### 方式二：Docker 手动跑

```bash
git clone https://gitee.com/anglers/proxyPing.git
cd proxyPing
docker build -t proxyip .
docker run -d --name proxyip --restart unless-stopped -p 8899:8899 proxyip
```

### 方式三：直接运行

需要 Go 1.26+，不用数据库、不用配置文件：

```bash
go run .                 # 默认 :8899
# 或者
go build -o proxyip .
./proxyip
```

换端口：`PROXYIP_PORT=9000 ./proxyip`。

## 怎么用

1. **填目标地址** —— 「目标」框里填你要测的那个接口，比如 `https://example.com/api/ping`。
   这是必填项，不填点开始会提示你。不写协议头也行，会自动补 `https://`。
   > 为什么一定要自己填？因为「延迟」只有相对同一个目标才有意义，测别人的站得到的数字对你没用。
2. **粘代理** —— 一行一个，丢进大文本框。格式很宽松，见下面那张表。
   填完卡片右上角会显示「已识别 N 条」，粘错格式的行会被单独指出来。
3. **调参数（可选）** —— 并发默认 8，超时 8 秒，每 IP 轮次 1。代理多就把并发调大，
   网络差就把超时调长；想要更稳的数字，把轮次调到 2 或 3（会慢一倍）。
4. **点「开始测速」** —— 或按 `Ctrl+Enter`。表格边测边往上长，顶部有进度条。
   中途想停就点「停止」，已经测出来的结果会留着。
5. **看结果挑代理** —— 默认按平均延迟排序，最快的排第一。想换排法用右上角下拉框。
   失败的行会沉到最底下，右侧写着原因。
6. **导出** —— 点「导出 CSV」，文件里带行号、地址、各段耗时、相对本地差值、响应片段和错误原因。

## 支持的代理格式

| 这么写 | 会当成 |
| --- | --- |
| `1.2.3.4:8080` | 普通 HTTP 代理 |
| `1.2.3.4:8080:user:pass` | 带账号密码的 HTTP 代理（代理池导出常见格式） |
| `user:pass@1.2.3.4:8080` | 带账号密码的 HTTP 代理 |
| `http://user:pass@1.2.3.4:8080` | 同上 |
| `https://1.2.3.4:8443` | HTTPS（TLS）代理 |
| `socks5://1.2.3.4:1080` | SOCKS5 代理 |
| `socks5://user:pass@1.2.3.4:1080` | 带账号密码的 SOCKS5 |
| `[::1]:8080` | IPv6 地址 |

- 以 `#` 或 `//` 开头的行当注释忽略，空行自动跳过
- 后面多写的路径和参数会被去掉：`http://1.2.3.4:8080/path?x=1` 按 `1.2.3.4:8080` 处理
- 同一个代理写两遍只测一次
- 解析不出来的行不会中断整批，会在「原始输出」里逐行告诉你「第 N 行跳过：原因」

## 结果怎么看

| 列 | 意思 |
| --- | --- |
| **状态** | 绿点是正常响应；橙点 `403 被挡` 表示链路通了但被目标站拒了；红点「失败」见最后一列 |
| **平均延迟** | 成功那几轮的平均总耗时，**排名就按这个** |
| **最小** | 最快那一轮花的时间 |
| **连接 / TLS** | 连上代理用了多久 / 和目标站握手用了多久 |
| **TTFB** | 发请求到收到第一个字节，包含上面两段 |
| **相对本地** | 比本机直连慢多少（负数表示比直连还快） |
| **响应 / 说明** | 返回的是 JSON 还是 HTML、多大；失败的写具体原因 |

延迟颜色：绿 ≤250ms、蓝 ≤500ms、橙 ≤900ms、红 >900ms。

几个常见现象：

- **「403 被挡」不是代理坏了** —— 链路是通的，只是目标站按地区或风控拒了。
- **代理比本地还快很正常** —— 本机出口被限速或绕路时，走代理反而更短。
- **公开代理大面积「失败」也很正常** —— 这类 IP 存活率本来就低，能挑出几条能用的就算赚。

## 常见问题

**目标填什么比较好？**
填你日常真正要访问的那个地址。测 `example.com` 得到的数字，跟你访问自己业务接口时的体验没有必然关系。

**为什么全都显示超时？**
先看「原始输出」里的时间线：如果本地基线也超时，说明机器本身出不去网；
如果本地正常、代理全超时，多半是这批代理失效了，或者需要认证 / 只支持 SOCKS5。

**能测多少条？**
一次最多 500 条，超出的部分不测（会提示）。并发上限 64，且不会超过你粘的条数。

**改了目标之后提示「目标已变，请重测」？**
「相对本地」是拿代理延迟减本地延迟算的。目标换了，旧基线就失效了，点本地那格的「重测」刷新一下即可。

**页面关掉再打开，之前填的还在吗？**
目标地址和并发/超时/轮次会记住（存在浏览器本地），**代理列表不记**（里面常带账号密码）。

**端口被占用？**
`PROXYIP_PORT=9000 ./proxyip`，Docker 部署则用 `PORT=9000 bash bin/deploy.sh`。

**CSV 打开是乱码？**
文件带 UTF-8 BOM，Excel 直接双击就能正常显示。

**数据会传到别处吗？**
不会。服务只监听本机端口，唯一的出站请求就是你自己填的那个目标地址。

## 接口

除了网页，也可以直接调接口，方便写脚本批量筛代理。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `POST` | `/api/test` | 批量测速，返回一行一个事件的流式结果（NDJSON） |
| `GET` | `/api/local` | 只测本地直连基线 |
| `GET` | `/version` | 版本号 |
| `GET` | `/` | 网页控制台 |

```bash
curl -s http://localhost:8899/api/test \
  -H 'Content-Type: application/json' \
  -d '{"proxies":"1.2.3.4:8080\n5.6.7.8:3128","target":"https://example.com/api/ping","timeoutMs":8000,"rounds":1,"concurrency":8}'
```

返回的事件依次是 `meta`（总数、解析失败行）→ `local`（本地基线）→ 多条 `result`（每个代理一条）→ `done`（统计）。
`target` 必填，为空会直接返回 `{"code":-1,"msg":"请先填写测速目标 URL"}`。

## 说明

- 本工具只测连通性和延迟，不提供、也不内置任何代理。请只测你有权使用的代理。
- 测速会向目标站发起真实请求，别拿它去打别人的接口做压力测试。

<div align="center">

[Gitee](https://gitee.com/anglers/proxyPing) · [GitHub](https://github.com/AnglerS888/proxyPing)

</div>
