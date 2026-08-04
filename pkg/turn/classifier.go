package turn

import "strings"

var keywordRules = []struct {
	label    string
	keywords []string
}{
	{"SRE", []string{"sre", "incident", "production", "prod", "deploy", "deployment", "release", "rollback", "server", "infra", "kubernetes", "k8s", "kubectl", "helm", "docker", "terraform", "ansible", "aws", "gcp", "azure", "ec2", "ecr", "eks", "gke", "部署", "发布", "回滚", "监控", "集群", "运维", "故障", "线上"}},
	{"Coding", []string{"coding", "code", "implement", "implementation", "fix", "bug", "refactor", "test", "build", "compile", "pull request", "github", "git", "repo", "branch", "commit", "apply_patch", "代码", "修复", "实现", "重构", "构建", "测试", "仓库", "提交"}},
	{"Docs", []string{"docs", "documentation", "readme", "notion", "runbook", "spec", "release notes", "markdown", ".md", ".docx", "文档", "说明", "笔记", "整理"}},
	{"Research", []string{"research", "search", "lookup", "look up", "web", "browse", "investigate", "compare", "调研", "搜索", "查找", "研究", "资料", "对比"}},
	{"Visuals", []string{"visual", "image", "imagegen", "image_gen", "design", "screenshot", "figma", "svg", "diagram", "图片", "截图", "设计", "生成图"}},
}

func scoreText(scores map[string]int, value string, weight int) {
	text := strings.ToLower(strings.TrimSpace(value))
	if text == "" || weight <= 0 {
		return
	}
	for _, rule := range keywordRules {
		for _, keyword := range rule.keywords {
			if keywordMatch(text, keyword) {
				scores[rule.label] += weight
			}
		}
	}
}

func classify(scores map[string]int) string {
	bestLabel := ""
	bestScore := 0
	secondScore := 0
	for _, label := range []string{"SRE", "Coding", "Docs", "Research", "Visuals"} {
		score := scores[label]
		if score > bestScore {
			secondScore = bestScore
			bestLabel = label
			bestScore = score
		} else if score > secondScore {
			secondScore = score
		}
	}
	if bestScore < 5 || bestScore-secondScore < 2 {
		return "Other"
	}
	return bestLabel
}

func classifyEvidence(evidence []Evidence) string {
	scores := map[string]int{}
	for _, item := range evidence {
		scoreText(scores, item.Text, item.Weight)
	}
	return classify(scores)
}

func keywordMatch(text, keyword string) bool {
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	if keyword == "" {
		return false
	}
	if strings.ContainsAny(keyword, " -_./") || !ascii(keyword) {
		return strings.Contains(text, keyword)
	}
	start := 0
	for {
		index := strings.Index(text[start:], keyword)
		if index < 0 {
			return false
		}
		index += start
		beforeOK := index == 0 || boundary(text[index-1])
		after := index + len(keyword)
		afterOK := after == len(text) || boundary(text[after])
		if beforeOK && afterOK {
			return true
		}
		start = index + len(keyword)
	}
}

func ascii(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] > 127 {
			return false
		}
	}
	return true
}

func boundary(value byte) bool {
	return !((value >= 'a' && value <= 'z') || (value >= '0' && value <= '9'))
}
