package cardplatform

import "strings"

// Claude 档位：卡台注册表里是裸键（pro / max_5x / max_20x），ACC 定价表里带 claude_ 前缀
// （claude_pro / claude_max_5x / claude_max_20x）。本站界面与码上统一用裸键，发码一律带
// claude_ 前缀（ClaudeIssuePlan），让卡台直接定位到 Claude，不靠「遍历各产品档位表」反查。
//
// Claude 直冲为恒价 USD（claudeProduct.UsesFlatFundingPrice），兑换一律按 US/USD 收单，
// 与 Grok 同构。三档裸键唯一，不与 GPT（plus/pro_20x…）、X、Grok 的键冲突。
var claudeBarePlans = map[string]bool{
	"pro":     true,
	"max_5x":  true,
	"max_20x": true,
}

// IsClaudePlan 识别 Claude 档位：带 claude_ 前缀，或裸键命中三档之一。
func IsClaudePlan(plan string) bool {
	p := strings.ToLower(strings.TrimSpace(plan))
	if strings.HasPrefix(p, "claude_") {
		return claudeBarePlans[strings.TrimPrefix(p, "claude_")]
	}
	return claudeBarePlans[p]
}

// ClaudeBarePlan 去掉 claude_ 前缀的裸档位（本站界面与码上都用裸键）。
func ClaudeBarePlan(plan string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(plan)), "claude_")
}

// ClaudeIssuePlan 发给卡台的发码档位：显式带 claude_ 前缀。卡台落库时会去掉前缀。
func ClaudeIssuePlan(plan string) string {
	return "claude_" + ClaudeBarePlan(plan)
}
