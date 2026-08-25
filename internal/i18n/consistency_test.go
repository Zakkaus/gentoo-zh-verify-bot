package i18n

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type forbiddenLocaleTerm struct {
	locale        Lang
	term          string
	preferred     string
	allowedSuffix string
	wholeWord     bool
	caseFold      bool
}

var forbiddenLocaleTerms = []forbiddenLocaleTerm{
	{locale: LangZH, term: "核心", preferred: "内核"},
	{locale: LangZH, term: "终端机", preferred: "终端"},
	{locale: LangZH, term: "虚拟机器", preferred: "虚拟机"},
	{locale: LangZH, term: "作业系统", preferred: "操作系统"},
	{locale: LangZH, term: "讯息", preferred: "消息"},
	{locale: LangZH, term: "连结", preferred: "链接"},
	{locale: LangZH, term: "私讯", preferred: "私聊"},
	{locale: LangZH, term: "社群", preferred: "社区"},
	{locale: LangZH, term: "装置", preferred: "设备"},
	{locale: LangZH, term: "传送", preferred: "发送"},
	{locale: LangZH, term: "範例", preferred: "示例"},
	{locale: LangZH, term: "点选", preferred: "点击"},
	{locale: LangZH, term: "逾时", preferred: "超时"},
	{locale: LangZH, term: "允许清单", preferred: "白名单"},
	{locale: LangZH, term: "关注频道", preferred: "加入频道"},
	{locale: LangZH, term: "申请者", preferred: "申请人"},
	{locale: LangZH, term: "管理者", preferred: "管理员"},
	{locale: LangZH, term: "封锁", preferred: "封禁"},
	{locale: LangZH, term: "移出群组", preferred: "踢出"},
	{locale: LangZH, term: "静音", preferred: "禁言"},
	{locale: LangZH, term: "还有", preferred: "剩余"},
	{locale: LangZH, term: "剩下", preferred: "剩余"},
	{locale: LangZH, term: "例如：", preferred: "示例："},
	{locale: LangZH, term: "指令", preferred: "命令"},
	{locale: LangZHHant, term: "內核", preferred: "核心"},
	{locale: LangZHHant, term: "終端", preferred: "終端機", allowedSuffix: "機"},
	{locale: LangZHHant, term: "虛擬機", preferred: "虛擬機器", allowedSuffix: "器"},
	{locale: LangZHHant, term: "操作系統", preferred: "作業系統"},
	{locale: LangZHHant, term: "消息", preferred: "訊息"},
	{locale: LangZHHant, term: "链接", preferred: "連結"},
	{locale: LangZHHant, term: "私聊", preferred: "私訊"},
	{locale: LangZHHant, term: "社區", preferred: "社群"},
	{locale: LangZHHant, term: "設備", preferred: "裝置"},
	{locale: LangZHHant, term: "發送", preferred: "傳送"},
	{locale: LangZHHant, term: "示例", preferred: "範例"},
	{locale: LangZHHant, term: "點擊", preferred: "點選"},
	{locale: LangZHHant, term: "超時", preferred: "逾時"},
	{locale: LangZHHant, term: "白名單", preferred: "允許清單"},
	{locale: LangZHHant, term: "允許名單", preferred: "允許清單"},
	{locale: LangZHHant, term: "關注頻道", preferred: "加入頻道"},
	{locale: LangZHHant, term: "申請者", preferred: "申請人"},
	{locale: LangZHHant, term: "用戶", preferred: "使用者", allowedSuffix: "端"},
	{locale: LangZHHant, term: "管理者", preferred: "管理員"},
	{locale: LangZHHant, term: "封鎖", preferred: "封禁"},
	{locale: LangZHHant, term: "踢出", preferred: "移出群組"},
	{locale: LangZHHant, term: "靜音", preferred: "禁言"},
	{locale: LangZHHant, term: "還有", preferred: "剩餘"},
	{locale: LangZHHant, term: "剩下", preferred: "剩餘"},
	{locale: LangZHHant, term: "關鍵詞", preferred: "關鍵字"},
	{locale: LangZHHant, term: "再試", preferred: "重試"},
	{locale: LangZHHant, term: "例如：", preferred: "範例："},
	{locale: LangZHHant, term: "封禁時間", preferred: "封禁時長"},
	{locale: LangZHHant, term: "命令", preferred: "指令"},
	{locale: LangEN, term: "admin", preferred: "administrator", wholeWord: true, caseFold: true},
	{locale: LangEN, term: "admins", preferred: "administrators", wholeWord: true, caseFold: true},
	{locale: LangEN, term: "private message", preferred: "direct message", caseFold: true},
	{locale: LangEN, term: "private chat", preferred: "direct message", caseFold: true},
	{locale: LangEN, term: "whitelist", preferred: "allowlist", wholeWord: true, caseFold: true},
	{locale: LangEN, term: "follow the channel", preferred: "join the channel", caseFold: true},
	{locale: LangEN, term: "click", preferred: "select", wholeWord: true, caseFold: true},
	{locale: LangEN, term: "clicked", preferred: "selected", wholeWord: true, caseFold: true},
	{locale: LangEN, term: "kick", preferred: "remove", wholeWord: true, caseFold: true},
	{locale: LangEN, term: "kicked", preferred: "removed", wholeWord: true, caseFold: true},
	{locale: LangEN, term: "VM", preferred: "virtual machine", wholeWord: true},
	{locale: LangEN, term: "OS", preferred: "operating system", wholeWord: true},
	{locale: LangEN, term: "time-out", preferred: "timeout", caseFold: true},
	{locale: LangEN, term: "join-verification", preferred: "join verification", caseFold: true},
	{locale: LangEN, term: "channel-identity", preferred: "sender-channel", caseFold: true},
	{locale: LangEN, term: "Rich-text", preferred: "Rich text", caseFold: true},
	{locale: LangEN, term: "new-member", preferred: "new member", caseFold: true},
	{locale: LangEN, term: "configuration-file", preferred: "configuration file", caseFold: true},
	{locale: LangEN, term: "remaining attempts", preferred: "attempts remain", caseFold: true},
}

type localeTranslations struct {
	locale Lang
	terms  []string
}

type englishOnlyTerm struct {
	term         string
	translations []localeTranslations
}

var englishOnlyTerms = []englishOnlyTerm{
	{
		term: "USE flag",
		translations: []localeTranslations{
			{locale: LangZH, terms: []string{"USE 标志", "USE 标识", "USE 旗标", "使用标志"}},
			{locale: LangZHHant, terms: []string{"USE 標誌", "USE 旗標", "使用標誌", "使用旗標"}},
		},
	},
	{
		term: "overlay",
		translations: []localeTranslations{
			{locale: LangZH, terms: []string{"覆盖层", "叠加仓库", "叠加软件源"}},
			{locale: LangZHHant, terms: []string{"覆蓋層", "疊加套件庫", "疊加軟體來源"}},
		},
	},
	{
		term: "keyword",
		translations: []localeTranslations{
			{locale: LangZH, terms: []string{"arm64 关键词", "amd64 关键词", "stable 关键词", "testing 关键词", "~arch 关键词", "架构关键词", "架构关键字"}},
			{locale: LangZHHant, terms: []string{"arm64 關鍵字", "amd64 關鍵字", "stable 關鍵字", "testing 關鍵字", "~arch 關鍵字", "架構關鍵字", "架構關鍵詞"}},
		},
	},
	{
		term: "ebuild",
		translations: []localeTranslations{
			{locale: LangZH, terms: []string{"包构建脚本", "软件包构建脚本", "包构建文件"}},
			{locale: LangZHHant, terms: []string{"套件建置腳本", "套件建置檔", "套件建置文件"}},
		},
	},
	{
		term: "emerge",
		translations: []localeTranslations{
			{locale: LangZH, terms: []string{"合并命令", "包安装命令", "软件包安装命令"}},
			{locale: LangZHHant, terms: []string{"合併指令", "套件安裝指令", "套件管理指令"}},
		},
	},
	{
		term: "Bugzilla",
		translations: []localeTranslations{
			{locale: LangZH, terms: []string{"错误跟踪系统", "缺陷跟踪系统", "漏洞跟踪系统"}},
			{locale: LangZHHant, terms: []string{"錯誤追蹤系統", "缺陷追蹤系統", "漏洞追蹤系統"}},
		},
	},
}

const simplifiedExclusiveCharacters = "内终机虚拟业统讯链区设备传发范点选时请员组锁验证获详暂闻词优显页数据来仅标状测试树号检软构码删隐输纯单复并长执运读认计达动离线错误绝欢联这与为开关过进还没无从对实现报应网队别义种类级权储够该会处带户术态务随将连续"
const traditionalExclusiveCharacters = "內終機虛擬業統訊鏈區設備傳發範點選時請員組鎖驗證獲詳暫聞詞優顯頁數據來僅標狀測試樹號檢軟構碼刪隱輸純單複復並長執運讀認計達動離線錯誤絕歡聯這與為開關過進還沒無從對實現報應網隊別義種類級權儲夠該會處帶戶術態務隨將連續"

func TestLocaleTerminologyConsistency(t *testing.T) {
	forEachCatalogString(func(locale Lang, _, location, text string) {
		for _, rule := range forbiddenLocaleTerms {
			if locale != rule.locale {
				continue
			}
			index := forbiddenTermIndex(text, rule)
			if index < 0 {
				continue
			}
			offending := text[index : index+len(rule.term)]
			t.Errorf("%s contains forbidden term %q; use %q", location, offending, rule.preferred)
		}
	})
}

func TestGentooTermsRemainEnglish(t *testing.T) {
	forEachCatalogString(func(locale Lang, _, location, text string) {
		for _, rule := range englishOnlyTerms {
			for _, localized := range rule.translations {
				if locale != localized.locale {
					continue
				}
				for _, translation := range localized.terms {
					if strings.Contains(text, translation) {
						t.Errorf("%s translates Gentoo term %q as forbidden %q; keep %q in English", location, rule.term, translation, rule.term)
					}
				}
			}
		}
	})
}

func TestChineseLocaleScripts(t *testing.T) {
	// Characters valid in both scripts are absent from these sets; no catalogue entry needs an exception.
	forEachCatalogString(func(locale Lang, _, location, text string) {
		var forbidden string
		var script string
		switch locale {
		case LangZH:
			forbidden = traditionalExclusiveCharacters
			script = "Traditional"
		case LangZHHant:
			forbidden = simplifiedExclusiveCharacters
			script = "Simplified"
		default:
			return
		}
		for _, character := range text {
			if strings.ContainsRune(forbidden, character) {
				t.Errorf("%s contains %s-only character %q", location, script, character)
			}
		}
	})
}

func forEachCatalogString(visit func(locale Lang, path, location, text string)) {
	visitCatalog(reflect.ValueOf(Messages), "Messages", func(path string, value reflect.Value) {
		switch value.Type() {
		case textType, formatType:
			for _, locale := range Languages() {
				visit(locale, path, catalogEntry(path, locale), localizedValues(value)[locale])
			}
		case stringListType:
			for _, locale := range Languages() {
				for index, text := range localizedStringValues(value)[locale] {
					entryPath := path + "[" + strconv.Itoa(index) + "]"
					visit(locale, entryPath, catalogEntry(entryPath, locale), text)
				}
			}
		}
	})
}

func forbiddenTermIndex(text string, rule forbiddenLocaleTerm) int {
	haystack := text
	needle := rule.term
	if rule.caseFold {
		haystack = strings.ToLower(haystack)
		needle = strings.ToLower(needle)
	}
	for start := 0; start <= len(haystack)-len(needle); {
		relative := strings.Index(haystack[start:], needle)
		if relative < 0 {
			return -1
		}
		index := start + relative
		end := index + len(needle)
		if rule.allowedSuffix != "" && strings.HasPrefix(text[end:], rule.allowedSuffix) {
			start = end
			continue
		}
		if rule.wholeWord && ((index > 0 && isASCIIWordByte(haystack[index-1])) || (end < len(haystack) && isASCIIWordByte(haystack[end]))) {
			start = end
			continue
		}
		return index
	}
	return -1
}

func isASCIIWordByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '_'
}
