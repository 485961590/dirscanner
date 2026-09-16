package main

import (
	"bufio"
	"crypto/tls"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Result struct {
	StatusCode int
	Size       int
	Target     string
}

type Scanner struct {
	client      *http.Client
	baseURL     string
	workerCount int
	Method      string
}

func NewScanner(baseURL string, workerCount int) *Scanner {
	return &Scanner{
		baseURL:     baseURL,
		workerCount: workerCount,
		client: &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				// 强制禁用 Keep-Alive，请求完立刻完全断开并释放本地端口，防止高并发下崩溃
				DisableKeepAlives: true,
				// 忽略 HTTPS 证书错误（防止因为目标网站证书过期或自签名直接报错退出）
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
			// 放弃跟随重定向直接响应原始码
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// 解析传入的
func parseCommandLineArgs() (method, baseURL, wordlist, csvOutput string) {
	if len(os.Args) != 5 {
		fmt.Printf("用法: dirscanner.exe <method[get,head]> <baseURL> <wordlist> <savePath>\n")
		os.Exit(1) // 干净利落地退出程序
	}

	// 校验并提取请求方法
	methodInput := strings.ToLower(os.Args[1])
	if methodInput == "get" {
		method = http.MethodGet
	} else if methodInput == "head" {
		method = http.MethodHead
	} else {
		fmt.Printf("[!] 错误: 不支持的方法类型 '%s'，只能选择 [get] 或 [head]\n", os.Args[1])
		os.Exit(1)
	}

	// 规范化目标 URL
	var err error
	baseURL, err = normalizeBaseURL(os.Args[2])
	if err != nil {
		fmt.Println("URL错误:", err)
		os.Exit(1)
	}

	// 提取字典路径
	wordlist = os.Args[3]

	// 计算并自动创建带时间戳的最终 CSV 路径
	csvOutput = determineOutputPath(os.Args[4])

	return method, baseURL, wordlist, csvOutput
}

// normalizeBaseURL检查url格式
func normalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)

	if raw == "" {
		return "", fmt.Errorf("URL不能为空")
	}

	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "http://" + raw
	}

	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("URL格式出错")
	}
	normalized := parsed.String()
	normalized = strings.TrimRight(normalized, "/")

	return normalized, nil
}

// requestOnce一次Get/Head请求
func requestOnce(client *http.Client, method, target string) (int, int, error) {
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("发送请求失败:%w", err)
	}

	// [可自定义请求头]可以加上一行自定义 User-Agent 避免被简单的 WAF 拦截
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Cache-Control", "max-age=0")

	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("发送请求失败:%w", err)
	}
	defer resp.Body.Close()

	if method == http.MethodHead {
		return resp.StatusCode, int(resp.ContentLength), nil
	}

	size, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		return resp.StatusCode, 0, fmt.Errorf("读取响应失败:%w", err)
	}
	return resp.StatusCode, int(size), nil
}

func worker(id int, client *http.Client, baseURL, method string, jobs <-chan string, results chan<- Result) {
	for j := range jobs {
		path := j
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}

		targetURL := baseURL + path
		code, size, err := requestOnce(client, method, targetURL)
		if err != nil {
			results <- Result{StatusCode: code, Size: size, Target: targetURL}
			continue
		}
		results <- Result{
			StatusCode: code,
			Size:       size,
			Target:     targetURL,
		}
	}
}

// ReadWordlistStream 打开字典并返回一个只读通道，流式输出每一行路径
func ReadWordlistStream(path string) (<-chan string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("无法打开字典: %w", err)
	}
	// 创建一个带缓冲的通道
	ch := make(chan string, 500)

	// 启动一个后台协程去读文件，读完自动关闭通道
	go func() {
		defer file.Close()
		defer close(ch)
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			// 过滤空行和注释
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			ch <- line // 通道满了会自动阻塞，天然限流
		}
	}()
	return ch, nil
}

func processResults(results <-chan Result, csvPath string) int {
	totalScanned := 0

	// 创建或打开 CSV 文件
	file, err := os.Create(csvPath)
	if err != nil {
		fmt.Printf("[!] 无法创建 CSV 文件: %v\n", err)
		return 0
	}
	defer file.Close()

	// 初始化 CSV 流式写入器
	writer := csv.NewWriter(file)
	defer writer.Flush() // 确保程序结束时，内存缓冲区的数据全部刷入硬盘
	// 写入 CSV 表头
	_ = writer.Write([]string{"StatusCode", "Size(Bytes)", "TargetURL"})

	// 实时消费通道数据
	for res := range results {
		totalScanned++

		// 捕捉网络彻底失败的情况
		if res.StatusCode == 0 {
			fmt.Printf("[ERROR] 请求失败 -> %s\n", res.Target)
			continue
		}

		// 【过滤逻辑】只打印和保存非 404 的高价值目标
		// 想看 404，把这个 if 条件删掉即可

		if res.StatusCode != 404 {
			// 根据不同状态码选择不同的 ANSI 颜色
			var colorCode string
			switch {
			case res.StatusCode >= 200 && res.StatusCode < 300:
				colorCode = "\033[32m" // 2xx 成功显绿色
			case res.StatusCode >= 300 && res.StatusCode < 400:
				colorCode = "\033[33m" // 3xx 跳转显黄色
			default:
				colorCode = "\033[31m" // 403/5xx 危险或错误显红色
			}

			// 带有色彩高亮的终端实施打印（\033[0m 用来重置颜色）
			fmt.Printf("%s[%d] Size:[%d]B -> %s\033[0m\n", colorCode, res.StatusCode, res.Size, res.Target)

			// 实时组装成字符串数组，写入 CSV（注意：CSV 文件里保持纯文本，绝对不能带颜色代码！）
			record := []string{
				fmt.Sprintf("%d", res.StatusCode),
				fmt.Sprintf("%d", res.Size),
				res.Target,
			}
			_ = writer.Write(record)
			writer.Flush()

			// 精准限流延时
			time.Sleep(50 * time.Millisecond)
		}

	}

	return totalScanned
}

// determineOutputPath 在用户指定的文件夹下，自动生成时间戳 CSV 路径
func determineOutputPath(inputPath string) string {
	// 生成标准的“年月日_时分秒”文件名
	timeFileName := time.Now().Format("20060102_150405") + ".csv"

	// 强行把用户指定的文件夹创建出来（如果已存在，MkdirAll 会自动忽略）
	_ = os.MkdirAll(inputPath, os.ModePerm)

	// 直接将文件夹路径和文件名安全拼接并返回
	return filepath.Join(inputPath, timeFileName)
}

func (s *Scanner) Run(wordlistPath string) (<-chan Result, error) {
	// 读字典
	jobs, err := ReadWordlistStream(wordlistPath)
	if err != nil {
		return nil, err
	}

	// 初始化结果通道，缓冲区与最大并发量对齐
	results := make(chan Result, s.workerCount)
	var wg sync.WaitGroup

	// 启动 Worker 线程池
	for w := 1; w <= s.workerCount; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			worker(workerID, s.client, s.baseURL, s.Method, jobs, results)
		}(w)
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	return results, nil
}

func main() {
	scanMethod, baseURL, wordlist, csvOutput := parseCommandLineArgs()

	// 打印环境配置信息
	fmt.Println("请求模式:", scanMethod)
	fmt.Println("目标地址:", baseURL)
	fmt.Println("字典:", wordlist)
	fmt.Println("[*] 结果将保存至:", csvOutput)

	// 初始化扫描引擎
	scanner := NewScanner(baseURL, 50)
	scanner.Method = scanMethod // 赋予本次指定的扫描方法

	// 启动并发扫描流水线
	results, err := scanner.Run(wordlist)
	if err != nil {
		fmt.Println("启动扫描失败:", err)
		return
	}

	// 消费结果并流式保存
	totalScanned := processResults(results, csvOutput)

	// 打印最终统计
	fmt.Printf("\n[*] 扫描任务已全部完成！共扫描了 %d 个目录。\n", totalScanned)
}
