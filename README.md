# Go DirScanner

一个基于 Go 语言编写的高并发、轻量级网站目录与敏感文件扫描工具。项目采用 Pipeline（流水线）/ Worker Pool（工作池）并发模型，具备内存安全、实时落盘等特性，专为安全测试与资产发现设计。

## 核心特性

- **高性能并发架构**：基于 Go 协程（Goroutine）与通道（Channel）实现的生产-消费模型，默认开启 50 并发高速扫描。
- **流式内存优化**：采用“流式读取（Stream Read）”字典机制，即使加载数百万行的超大字典，内存占用也极低，彻底告别内存溢出风险。
- **高抗干扰网络配置**：
  - 自动禁用 HTTP Keep-Alive，请求完毕立刻完全断开并释放本地端口，防止高并发下端口耗尽崩溃。
  - 自动忽略 HTTPS 证书错误，支持自签名或过期证书的目标。
  - 强行丢弃 HTTP 重定向（CheckRedirect），直接获取原始响应码，防止跳转污染。
- **实时结果落盘**：结果实时写入 CSV 并强制执行 Flush 刷入硬盘。即使中途按下 Ctrl+C 强行终止，已扫描到的数据也绝对不会丢失。
- **智能终端交互**：自动过滤 404 无效状态码，并在终端根据状态码实时彩色高亮显示（2xx 绿、3xx 黄、4xx/5xx 红）。

## 函数执行架构

工具内部由三个解耦的核心模块通过通道（Channel）串联运行：

```text
[ 命令行参数解析 ] ---> 规范化 URL 并初始化高性能 HTTP Client
       │
       ▼
[ 异步流式生产 ] ---> (协程 1) 逐行读取字典，过滤空行与注释 ---> 塞入 jobs 通道
       │
       ▼
[ 并发计算消费 ] ---> (50个 Worker 协程) 竞争 jobs 通道 ---> requestOnce() 发包 ---> 塞入 results 通道
       │
       ▼
[ 实时结果持久 ] ---> (主协程) 消费 results ---> 过滤404 ---> 彩色打印 ---> 实时 Flush 写入 CSV
```

## 快速开始

### 1. 编译
确保你已安装 Go 环境，在项目根目录下执行编译命令：

```bash
# Windows 环境
go build -o dirscanner.exe main.go

# Linux / macOS 环境
go build -o dirscanner main.go
```

### 2. 运行参数说明
程序需要传入 4 个必要的命令行参数：
```bash
./dirscanner <请求方法[get/head]> <目标URL> <字典路径> <结果保存目录>
```

- **请求方法**：支持 get 或 head（head 方法速度极快且省流量）。
- **目标URL**：支持补全（如输入 example.com 会自动规范化为 http://example.com）。
- **字典路径**：你的 Web 路径字典文件（支持 # 注释行和空行）。
- **结果保存目录**：指定一个文件夹，程序会自动在内生成以 “年月日_时分秒.csv” 命名的结果文件。

### 3. 使用示例

```bash
# 使用 HEAD 请求快速探测，结果保存在当前目录的 outputs 文件夹下
./dirscanner head https://example.com dict.txt ./outputs

# 使用 GET 请求深入探测
./dirscanner get example.com /path/to/wordlist.txt /var/log/scan_results
```

## 输出结果

### 终端输出
终端会实时滚动非 404 的存活资产：
```text
请求模式: get
目标地址: http://example.com
字典: dict.txt
[*] 结果将保存至: outputs/20260916_162000.csv
[200] Size:[1240]B -> http://example.com
[301] Size:[0]B -> http://example.com
[403] Size:[275]B -> http://example.com

[*] 扫描任务已全部完成！共扫描了 1500 个目录。
```

### CSV 文件
保存的 CSV 文件保持纯文本格式（不带任何 ANSI 颜色代码），方便直接导入 Excel 或联动其他自动化脚本：

| StatusCode | Size(Bytes) | TargetURL |
| :--- | :--- | :--- |
| 200 | 1240 | http://example.com |
| 301 | 0 | http://example.com |
| 403 | 275 | http://example.com |

## 免责声明
本工具仅用于合法授权的安全审计、渗透测试与合规性检测。请勿将其用于未经授权的网络攻击行为。因使用本工具导致的任何直接或间接后果，均由使用者本人承担。
