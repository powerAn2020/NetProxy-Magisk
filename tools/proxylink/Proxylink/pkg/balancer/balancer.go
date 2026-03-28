package balancer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"proxylink/pkg/generator"
)

// BalancerDef 负载均衡定义 (存储在 _balancers/*.json)
type BalancerDef struct {
	Name     string           `json:"name"`
	Strategy string           `json:"strategy"`
	Sources  []BalancerSource `json:"sources"`
	Created  string           `json:"created"`
	Updated  string           `json:"updated"`
}

// BalancerSource 节点来源
type BalancerSource struct {
	Type  string   `json:"type"`            // "subscription" | "manual"
	Group string   `json:"group,omitempty"` // 订阅分组目录名
	Regex string   `json:"regex,omitempty"` // 正则过滤
	Files []string `json:"files,omitempty"` // 手动选择的文件相对路径
}

// XrayBalancerConfig 生成的 Xray 负载均衡完整配置
type XrayBalancerConfig struct {
	Outbounds        []*generator.XrayOutbound `json:"outbounds"`
	Routing          *RoutingConfig            `json:"routing"`
	BurstObservatory *BurstObservatory         `json:"burstObservatory,omitempty"`
}

// RoutingConfig 路由配置
type RoutingConfig struct {
	DomainStrategy string                   `json:"domainStrategy,omitempty"`
	Rules          []map[string]interface{} `json:"rules,omitempty"`
	Balancers      []BalancerRule           `json:"balancers"`
}

// BalancerRule 均衡器规则
type BalancerRule struct {
	Tag         string           `json:"tag"`
	Selector    []string         `json:"selector"`
	Strategy    BalancerStrategy `json:"strategy"`
	FallbackTag string           `json:"fallbackTag"`
}

// BalancerStrategy 均衡策略
type BalancerStrategy struct {
	Type string `json:"type"`
}

// BurstObservatory 连通性检查配置
type BurstObservatory struct {
	SubjectSelector []string    `json:"subjectSelector"`
	PingConfig      *PingConfig `json:"pingConfig"`
}

// PingConfig 探测配置
type PingConfig struct {
	Destination  string `json:"destination"`
	Interval     string `json:"interval"`
	Connectivity string `json:"connectivity"`
	Timeout      string `json:"timeout"`
	Sampling     int    `json:"sampling"`
}

// NodeConfig 节点配置文件结构 (proxylink 输出的 xray 格式)
type NodeConfig struct {
	Outbounds []json.RawMessage `json:"outbounds"`
}

// sanitizeName 清理名称用于文件名
func sanitizeName(name string) string {
	replacer := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_",
		"?", "_", "\"", "_", "<", "_", ">", "_",
		"|", "_", " ", "_",
	)
	return replacer.Replace(name)
}

func balancersDir(outboundsDir string) string {
	return filepath.Join(outboundsDir, "_balancers")
}

func generatedDir(outboundsDir string) string {
	return filepath.Join(outboundsDir, "_generated")
}

// Create 创建负载均衡组
func Create(outboundsDir, name, strategy, sourcesJSON string) error {
	if name == "" || strategy == "" || sourcesJSON == "" {
		return fmt.Errorf("参数不完整: name=%q strategy=%q", name, strategy)
	}

	var sources []BalancerSource
	if err := json.Unmarshal([]byte(sourcesJSON), &sources); err != nil {
		return fmt.Errorf("解析 sources JSON 失败: %w", err)
	}

	bDir := balancersDir(outboundsDir)
	if err := os.MkdirAll(bDir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	now := time.Now().Format(time.RFC3339)
	def := BalancerDef{
		Name:     name,
		Strategy: strategy,
		Sources:  sources,
		Created:  now,
		Updated:  now,
	}

	safeName := sanitizeName(name)
	configPath := filepath.Join(bDir, safeName+".json")

	data, err := json.MarshalIndent(def, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化失败: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("写入文件失败: %w", err)
	}

	// 立即生成 Xray 配置
	outputPath, err := Generate(outboundsDir, name)
	if err != nil {
		return fmt.Errorf("生成配置失败: %w", err)
	}

	fmt.Fprintf(os.Stderr, "负载均衡组已创建: %s -> %s\n", name, outputPath)
	return nil
}

// Update 更新负载均衡组
func Update(outboundsDir, name, strategy, sourcesJSON string) error {
	if name == "" || strategy == "" || sourcesJSON == "" {
		return fmt.Errorf("参数不完整")
	}

	var sources []BalancerSource
	if err := json.Unmarshal([]byte(sourcesJSON), &sources); err != nil {
		return fmt.Errorf("解析 sources JSON 失败: %w", err)
	}

	safeName := sanitizeName(name)
	configPath := filepath.Join(balancersDir(outboundsDir), safeName+".json")

	// 读取旧定义以保留 created 时间
	var created string
	if oldData, err := os.ReadFile(configPath); err == nil {
		var oldDef BalancerDef
		if json.Unmarshal(oldData, &oldDef) == nil {
			created = oldDef.Created
		}
	}
	if created == "" {
		created = time.Now().Format(time.RFC3339)
	}

	def := BalancerDef{
		Name:     name,
		Strategy: strategy,
		Sources:  sources,
		Created:  created,
		Updated:  time.Now().Format(time.RFC3339),
	}

	data, err := json.MarshalIndent(def, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化失败: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("写入文件失败: %w", err)
	}

	// 重新生成 Xray 配置
	if _, err := Generate(outboundsDir, name); err != nil {
		return fmt.Errorf("重新生成配置失败: %w", err)
	}

	fmt.Fprintf(os.Stderr, "负载均衡组已更新: %s\n", name)
	return nil
}

// Delete 删除负载均衡组
func Delete(outboundsDir, name string) error {
	if name == "" {
		return fmt.Errorf("请提供名称")
	}

	safeName := sanitizeName(name)
	configPath := filepath.Join(balancersDir(outboundsDir), safeName+".json")
	generatedPath := filepath.Join(generatedDir(outboundsDir), safeName+".json")

	os.Remove(configPath)
	os.Remove(generatedPath)

	fmt.Fprintf(os.Stderr, "负载均衡组已删除: %s\n", name)
	return nil
}

// List 列出所有负载均衡组 (输出 JSON 到 stdout)
func List(outboundsDir string) error {
	bDir := balancersDir(outboundsDir)

	var defs []BalancerDef

	entries, err := os.ReadDir(bDir)
	if err != nil {
		// 目录不存在，返回空数组
		fmt.Println("[]")
		return nil
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(bDir, entry.Name()))
		if err != nil {
			continue
		}

		var def BalancerDef
		if err := json.Unmarshal(data, &def); err != nil {
			continue
		}

		defs = append(defs, def)
	}

	if defs == nil {
		defs = []BalancerDef{}
	}

	output, err := json.MarshalIndent(defs, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化失败: %w", err)
	}

	fmt.Println(string(output))
	return nil
}

// Generate 生成 Xray 负载均衡配置
func Generate(outboundsDir, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("请提供名称")
	}

	safeName := sanitizeName(name)
	configPath := filepath.Join(balancersDir(outboundsDir), safeName+".json")

	// 1. 读取定义
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("读取定义失败: %w", err)
	}

	var def BalancerDef
	if err := json.Unmarshal(data, &def); err != nil {
		return "", fmt.Errorf("解析定义失败: %w", err)
	}

	// 2. 收集匹配的节点文件
	nodeFiles, err := collectNodes(outboundsDir, def.Sources)
	if err != nil {
		return "", fmt.Errorf("收集节点失败: %w", err)
	}

	if len(nodeFiles) == 0 {
		return "", fmt.Errorf("没有匹配到任何节点")
	}

	fmt.Fprintf(os.Stderr, "匹配到 %d 个节点\n", len(nodeFiles))

	// 3. 读取每个节点文件，提取第一个 outbound 并修改 tag
	var outbounds []*generator.XrayOutbound
	idx := 0

	for _, nodeFile := range nodeFiles {
		fileData, err := os.ReadFile(nodeFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "警告: 读取 %s 失败: %v\n", nodeFile, err)
			continue
		}

		var nodeCfg NodeConfig
		if err := json.Unmarshal(fileData, &nodeCfg); err != nil {
			fmt.Fprintf(os.Stderr, "警告: 解析 %s 失败: %v\n", nodeFile, err)
			continue
		}

		if len(nodeCfg.Outbounds) == 0 {
			fmt.Fprintf(os.Stderr, "警告: %s 没有 outbound\n", nodeFile)
			continue
		}

		// 取第一个 outbound (proxy 出站)
		var outbound generator.XrayOutbound
		if err := json.Unmarshal(nodeCfg.Outbounds[0], &outbound); err != nil {
			fmt.Fprintf(os.Stderr, "警告: 解析 outbound %s 失败: %v\n", nodeFile, err)
			continue
		}

		// 修改 tag
		outbound.Tag = fmt.Sprintf("lb-%d", idx)
		outbounds = append(outbounds, &outbound)
		idx++
	}

	if len(outbounds) == 0 {
		return "", fmt.Errorf("没有有效的节点文件")
	}

	fmt.Fprintf(os.Stderr, "生成负载均衡配置: %d 个出站, 策略: %s\n", len(outbounds), def.Strategy)

	// 4. 构建完整 Xray 配置
	// 读取现有路由规则并转换 outboundTag: proxy -> balancerTag: proxy
	routingRules := loadAndTransformRoutingRules(outboundsDir)

	config := XrayBalancerConfig{
		Outbounds: outbounds,
		Routing: &RoutingConfig{
			DomainStrategy: "AsIs",
			Rules:          routingRules,
			Balancers: []BalancerRule{
				{
					Tag:         "proxy",
					Selector:    []string{"lb-"},
					Strategy:    BalancerStrategy{Type: def.Strategy},
					FallbackTag: "direct",
				},
			},
		},
	}

	// 需要 observatory 的策略
	if def.Strategy == "leastPing" || def.Strategy == "leastLoad" {
		config.BurstObservatory = &BurstObservatory{
			SubjectSelector: []string{"lb-"},
			PingConfig: &PingConfig{
				Destination:  "https://www.google.com/generate_204",
				Interval:     "5m",
				Connectivity: "https://connectivitycheck.gstatic.com/generate_204",
				Timeout:      "10s",
				Sampling:     10,
			},
		}
	}

	// 5. 写入生成的配置文件
	gDir := generatedDir(outboundsDir)
	if err := os.MkdirAll(gDir, 0755); err != nil {
		return "", fmt.Errorf("创建目录失败: %w", err)
	}

	outputPath := filepath.Join(gDir, safeName+".json")
	outputData, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", fmt.Errorf("序列化配置失败: %w", err)
	}

	if err := os.WriteFile(outputPath, outputData, 0644); err != nil {
		return "", fmt.Errorf("写入配置失败: %w", err)
	}

	fmt.Fprintf(os.Stderr, "配置已生成: %s\n", outputPath)
	return outputPath, nil
}

// RegenerateAll 重新生成所有负载均衡配置 (订阅更新后调用)
func RegenerateAll(outboundsDir string) (int, error) {
	bDir := balancersDir(outboundsDir)

	entries, err := os.ReadDir(bDir)
	if err != nil {
		// 没有负载均衡定义目录
		return 0, nil
	}

	count := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(bDir, entry.Name()))
		if err != nil {
			continue
		}

		var def BalancerDef
		if err := json.Unmarshal(data, &def); err != nil {
			continue
		}

		if def.Name == "" {
			continue
		}

		fmt.Fprintf(os.Stderr, "重新生成负载均衡: %s\n", def.Name)
		if _, err := Generate(outboundsDir, def.Name); err != nil {
			fmt.Fprintf(os.Stderr, "警告: %s 重新生成失败: %v\n", def.Name, err)
			continue
		}
		count++
	}

	fmt.Fprintf(os.Stderr, "已重新生成 %d 个负载均衡配置\n", count)
	return count, nil
}

// loadAndTransformRoutingRules 读取现有路由规则并将 outboundTag: proxy 替换为 balancerTag: proxy
func loadAndTransformRoutingRules(outboundsDir string) []map[string]interface{} {
	// outboundsDir = .../config/xray/outbounds, rule.json 在 .../config/xray/confdir/routing/rule.json
	confDir := filepath.Join(outboundsDir, "..", "confdir", "routing", "rule.json")
	data, err := os.ReadFile(confDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "警告: 读取路由规则失败: %v\n", err)
		return nil
	}

	var ruleFile struct {
		Routing struct {
			DomainStrategy string                   `json:"domainStrategy"`
			Rules          []map[string]interface{} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(data, &ruleFile); err != nil {
		fmt.Fprintf(os.Stderr, "警告: 解析路由规则失败: %v\n", err)
		return nil
	}

	// 将 outboundTag: proxy 替换为 balancerTag: proxy
	for _, rule := range ruleFile.Routing.Rules {
		if tag, ok := rule["outboundTag"]; ok {
			if tagStr, ok := tag.(string); ok && tagStr == "proxy" {
				delete(rule, "outboundTag")
				rule["balancerTag"] = "proxy"
			}
		}
	}

	return ruleFile.Routing.Rules
}

// collectNodes 根据 sources 收集匹配的节点文件路径
func collectNodes(outboundsDir string, sources []BalancerSource) ([]string, error) {
	var result []string
	seen := make(map[string]bool) // 去重

	for _, src := range sources {
		switch src.Type {
		case "subscription":
			files, err := collectSubscriptionNodes(outboundsDir, src.Group, src.Regex)
			if err != nil {
				fmt.Fprintf(os.Stderr, "警告: 收集订阅节点失败: %v\n", err)
				continue
			}
			for _, f := range files {
				if !seen[f] {
					seen[f] = true
					result = append(result, f)
				}
			}

		case "manual":
			for _, relPath := range src.Files {
				fullPath := filepath.Join(outboundsDir, relPath)
				if _, err := os.Stat(fullPath); err == nil && !seen[fullPath] {
					seen[fullPath] = true
					result = append(result, fullPath)
				}
			}
		}
	}

	return result, nil
}

// collectSubscriptionNodes 从订阅分组目录收集节点，可选正则过滤
func collectSubscriptionNodes(outboundsDir, group, regexStr string) ([]string, error) {
	if group == "" {
		return nil, fmt.Errorf("订阅分组为空")
	}

	groupDir := filepath.Join(outboundsDir, group)
	entries, err := os.ReadDir(groupDir)
	if err != nil {
		return nil, fmt.Errorf("读取目录 %s 失败: %w", groupDir, err)
	}

	var re *regexp.Regexp
	if regexStr != "" {
		re, err = regexp.Compile(regexStr)
		if err != nil {
			return nil, fmt.Errorf("无效的正则表达式 %q: %w", regexStr, err)
		}
	}

	var result []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") || name == "_meta.json" {
			continue
		}

		// 正则过滤 (基于不带扩展名的文件名)
		if re != nil {
			baseName := strings.TrimSuffix(name, ".json")
			if !re.MatchString(baseName) {
				continue
			}
		}

		result = append(result, filepath.Join(groupDir, name))
	}

	return result, nil
}
