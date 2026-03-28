package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"proxylink/pkg/balancer"
	"proxylink/pkg/encoder"
	"proxylink/pkg/generator"
	"proxylink/pkg/model"
	"proxylink/pkg/parser"
	"proxylink/pkg/subscription"
)

var (
	parseURI     = flag.String("parse", "", "解析单条链接")
	parseFile    = flag.String("file", "", "从文件批量解析")
	subURL       = flag.String("sub", "", "订阅 URL")
	outputFormat = flag.String("format", "json", "输出格式: json, xray, hy2, uri")
	outputFile   = flag.String("o", "", "输出到文件 (单文件模式)")
	outputDir    = flag.String("dir", "", "输出目录 (多文件模式，每个节点单独一个文件)")
	autoName     = flag.Bool("auto", false, "自动使用 remarks 作为文件名")
	socksPort    = flag.Int("port", 1234, "Hysteria2 SOCKS 端口")
	prettyPrint  = flag.Bool("pretty", true, "美化 JSON 输出")
	insecure     = flag.Bool("insecure", false, "跳过 TLS 证书验证 (用于 Android 等环境)")
	showHelp     = flag.Bool("h", false, "显示帮助")
)

func main() {
	// 子命令模式: proxylink balancer ...
	if len(os.Args) > 1 && os.Args[1] == "balancer" {
		if err := handleBalancerCommand(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "错误: %v\n", err)
			os.Exit(1)
		}
		return
	}

	flag.Usage = usage
	flag.Parse()

	if *showHelp || (flag.NFlag() == 0 && flag.NArg() == 0) {
		usage()
		return
	}

	var err error

	switch {
	case *parseURI != "":
		err = handleParseSingle(*parseURI)
	case *parseFile != "":
		err = handleParseFile(*parseFile)
	case *subURL != "":
		err = handleSubscription(*subURL)
	case flag.NArg() > 0:
		err = handleParseSingle(flag.Arg(0))
	default:
		err = handleStdin()
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`proxylink - 代理链接解析器

用法:
  proxylink [选项] [链接]
  proxylink -parse "vless://..."
  proxylink -file nodes.txt
  proxylink -sub "https://example.com/sub"
  echo "vless://..." | proxylink

选项:`)
	flag.PrintDefaults()
	fmt.Println(`
输出格式:
  json   - ProfileItem JSON (默认)
  xray   - Xray 出站配置
  hy2    - Hysteria2 原生配置
  uri    - 生成链接

示例:
  # 解析单条，输出 Xray 配置
  proxylink -parse "vless://..." -format xray

  # 解析单条，自动使用 remarks 命名文件
  proxylink -parse "vless://..." -format xray -auto

  # 订阅转换，所有节点输出到一个文件
  proxylink -sub "https://..." -format xray -o outbounds.json

  # 订阅转换，每个节点单独输出一个文件到指定目录
  proxylink -sub "https://..." -format xray -dir ./nodes

  # Android 设备跳过证书验证
  proxylink -sub "https://..." -insecure -format xray -dir ./nodes

  # 从文件批量解析，每个节点单独输出
  proxylink -file nodes.txt -format hy2 -dir ./configs`)
}

func handleParseSingle(uri string) error {
	profile, err := parser.Parse(uri)
	if err != nil {
		return err
	}

	output, err := formatSingleProfile(profile)
	if err != nil {
		return err
	}

	return writeOutput(output, profile.Remarks)
}

func handleParseFile(filename string) error {
	content, err := os.ReadFile(filename)
	if err != nil {
		return err
	}
	return handleBatch(string(content))
}

func handleStdin() error {
	content, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	input := strings.TrimSpace(string(content))
	if input == "" {
		return fmt.Errorf("无输入内容")
	}
	return handleBatch(input)
}

func handleSubscription(url string) error {
	var converter *subscription.Converter
	if *insecure {
		converter = subscription.NewConverterInsecure()
	} else {
		converter = subscription.NewConverter()
	}

	result, err := converter.Convert(url)
	if err != nil {
		return err
	}

	if result.Success == 0 {
		return fmt.Errorf("订阅解析失败")
	}

	fmt.Fprintf(os.Stderr, "订阅解析: 成功 %d, 失败 %d\n", result.Success, result.Failed)
	return outputProfiles(result.Profiles)
}

func handleBatch(content string) error {
	profiles, errs := parser.ParseBatch(content)
	if len(errs) > 0 {
		fmt.Fprintf(os.Stderr, "警告: %d 条解析失败\n", len(errs))
	}
	if len(profiles) == 0 {
		return fmt.Errorf("无有效链接")
	}
	return outputProfiles(profiles)
}

// outputProfiles 输出多个配置
func outputProfiles(profiles []*model.ProfileItem) error {
	// 多文件模式: -dir 指定目录
	if *outputDir != "" {
		return writeMultipleFiles(profiles)
	}

	// 单文件模式
	output, err := formatProfiles(profiles)
	if err != nil {
		return err
	}

	return writeOutput(output, "")
}

// writeMultipleFiles 每个节点单独输出一个文件
func writeMultipleFiles(profiles []*model.ProfileItem) error {
	// 创建目录
	if err := os.MkdirAll(*outputDir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %v", err)
	}

	ext := getFileExtension()
	nameCount := make(map[string]int) // 跟踪文件名使用次数，避免同名覆盖

	for i, profile := range profiles {
		output, err := formatSingleProfile(profile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "警告: 格式化 %s 失败: %v\n", profile.Remarks, err)
			continue
		}

		// 生成文件名
		baseName := sanitizeFilename(profile.Remarks)
		if baseName == "" {
			baseName = fmt.Sprintf("node_%d", i+1)
		}

		// 处理重复文件名
		nameCount[baseName]++
		filename := baseName
		if nameCount[baseName] > 1 {
			filename = fmt.Sprintf("%s_%d", baseName, nameCount[baseName])
		}
		filename = filename + ext

		filepath := filepath.Join(*outputDir, filename)
		if err := os.WriteFile(filepath, []byte(output), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "警告: 写入 %s 失败: %v\n", filepath, err)
			continue
		}
		fmt.Fprintf(os.Stderr, "已写入: %s\n", filepath)
	}

	return nil
}

// writeOutput 输出结果
func writeOutput(output, remarks string) error {
	// 自动命名模式
	if *autoName && remarks != "" {
		ext := getFileExtension()
		filename := sanitizeFilename(remarks) + ext
		if err := os.WriteFile(filename, []byte(output), 0644); err != nil {
			return fmt.Errorf("写入文件失败: %v", err)
		}
		fmt.Fprintf(os.Stderr, "已写入: %s\n", filename)
		return nil
	}

	// 指定输出文件
	if *outputFile != "" {
		if err := os.WriteFile(*outputFile, []byte(output), 0644); err != nil {
			return fmt.Errorf("写入文件失败: %v", err)
		}
		fmt.Fprintf(os.Stderr, "已写入: %s\n", *outputFile)
		return nil
	}

	// 输出到 stdout
	fmt.Println(output)
	return nil
}

// getFileExtension 根据输出格式获取文件扩展名
func getFileExtension() string {
	switch *outputFormat {
	case "uri":
		return ".txt"
	default:
		return ".json"
	}
}

// sanitizeFilename 清理文件名中的非法字符
func sanitizeFilename(name string) string {
	// 只替换 Windows 不允许的字符: < > : " / \ | ? *
	replacer := strings.NewReplacer(
		"<", "_",
		">", "_",
		":", "_",
		"\"", "_",
		"/", "_",
		"\\", "_",
		"|", "_",
		"?", "_",
		"*", "_",
	)
	name = replacer.Replace(name)

	// 移除控制字符 (0x00-0x1f)
	var result strings.Builder
	for _, r := range name {
		if r >= 0x20 {
			result.WriteRune(r)
		}
	}
	name = result.String()

	// 限制长度 (按 rune 计算，支持中文)
	runes := []rune(name)
	if len(runes) > 100 {
		name = string(runes[:100])
	}

	return strings.TrimSpace(name)
}

func formatSingleProfile(profile *model.ProfileItem) (string, error) {
	switch *outputFormat {
	case "xray":
		config := generator.GenerateXrayConfig(profile)
		return toJSON(config)
	case "hy2":
		config := generator.GenerateHysteria2Config(profile, *socksPort)
		return toJSON(config)
	case "uri":
		return encoder.ToURI(profile), nil
	default:
		return toJSON(profile)
	}
}

func formatProfiles(profiles []*model.ProfileItem) (string, error) {
	switch *outputFormat {
	case "xray":
		var outbounds []*generator.XrayOutbound
		for _, p := range profiles {
			outbounds = append(outbounds, generator.GenerateXrayOutbound(p))
		}
		config := &generator.XrayConfig{Outbounds: outbounds}
		return toJSON(config)
	case "hy2":
		var configs []*generator.Hysteria2Config
		for _, p := range profiles {
			configs = append(configs, generator.GenerateHysteria2Config(p, *socksPort))
		}
		return toJSON(configs)
	case "uri":
		uris := encoder.ToURIBatch(profiles)
		return strings.Join(uris, "\n"), nil
	default:
		return toJSON(profiles)
	}
}

func toJSON(data interface{}) (string, error) {
	var jsonBytes []byte
	var err error
	if *prettyPrint {
		jsonBytes, err = json.MarshalIndent(data, "", "  ")
	} else {
		jsonBytes, err = json.Marshal(data)
	}
	return string(jsonBytes), err
}

// handleBalancerCommand 处理 balancer 子命令
func handleBalancerCommand(args []string) error {
	if len(args) == 0 {
		balancerUsage()
		return nil
	}

	subcmd := args[0]
	fs := flag.NewFlagSet("balancer "+subcmd, flag.ExitOnError)
	nameFlag := fs.String("name", "", "负载均衡组名称")
	strategyFlag := fs.String("strategy", "", "策略: random, roundRobin, leastPing, leastLoad")
	sourcesFlag := fs.String("sources", "", "来源 JSON")
	dirFlag := fs.String("dir", "", "outbounds 目录路径")

	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	outboundsDir := *dirFlag
	if outboundsDir == "" {
		return fmt.Errorf("必须指定 -dir 参数")
	}

	switch subcmd {
	case "create":
		if err := balancer.Create(outboundsDir, *nameFlag, *strategyFlag, *sourcesFlag); err != nil {
			return err
		}
		fmt.Println("success")
		return nil

	case "update":
		if err := balancer.Update(outboundsDir, *nameFlag, *strategyFlag, *sourcesFlag); err != nil {
			return err
		}
		fmt.Println("success")
		return nil

	case "delete":
		if err := balancer.Delete(outboundsDir, *nameFlag); err != nil {
			return err
		}
		fmt.Println("success")
		return nil

	case "list":
		return balancer.List(outboundsDir)

	case "generate":
		outputPath, err := balancer.Generate(outboundsDir, *nameFlag)
		if err != nil {
			return err
		}
		fmt.Println(outputPath)
		return nil

	case "regenerate-all":
		count, err := balancer.RegenerateAll(outboundsDir)
		if err != nil {
			return err
		}
		fmt.Println(count)
		return nil

	case "-h", "--help", "help":
		balancerUsage()
		return nil

	default:
		return fmt.Errorf("未知子命令: %s", subcmd)
	}
}

func balancerUsage() {
	fmt.Println(`proxylink balancer - 负载均衡管理

用法:
  proxylink balancer <子命令> [选项]

子命令:
  create          创建负载均衡组
  update          更新负载均衡组
  delete          删除负载均衡组
  list            列出所有负载均衡组
  generate        生成 Xray 配置
  regenerate-all  重新生成所有配置

选项:
  -name string     负载均衡组名称
  -strategy string 策略: random, roundRobin, leastPing, leastLoad
  -sources string  来源 JSON 数组
  -dir string      outbounds 目录路径 (必需)

示例:
  proxylink balancer create -name "负载均衡" -strategy leastPing -sources '[{"type":"subscription","group":"sub_my"}]' -dir /data/adb/modules/netproxy/config/xray/outbounds
  proxylink balancer list -dir /data/adb/modules/netproxy/config/xray/outbounds
  proxylink balancer generate -name "负载均衡" -dir /data/adb/modules/netproxy/config/xray/outbounds`)
}
