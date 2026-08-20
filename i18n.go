package main

import "strings"

// The verification path speaks three locales, chosen from the applicant's Telegram interface
// language (the `language_code` Telegram sends with every user): Simplified Chinese, Traditional
// Chinese, and English for everyone else. Only the APPLICANT-facing strings are translated —
// admin output, the admin log and the group's moderation replies stay Simplified Chinese, because
// they are read by this community's admins, not by the joiner.
type lang string

const (
	langZH  lang = "zh-hans"
	langZHT lang = "zh-hant"
	langEN  lang = "en"
)

// langFor maps an IETF language tag from Telegram to one of the three supported locales.
// "zh-hant", "zh-tw", "zh-hk", "zh-mo" (and a bare "yue") read as Traditional; any other zh as
// Simplified; everything else, including an empty/unknown tag, as English. Telegram sends the
// user's INTERFACE language, so a Chinese speaker running an English client gets English — that is
// the best signal available, and the answer (a version number) is language-neutral anyway.
func langFor(code string) lang {
	c := strings.ToLower(strings.TrimSpace(code))
	if !strings.HasPrefix(c, "zh") && !strings.HasPrefix(c, "yue") {
		return langEN
	}
	for _, t := range []string{"hant", "tw", "hk", "mo", "yue"} {
		if strings.Contains(c, t) {
			return langZHT
		}
	}
	return langZH
}

// catalog holds one locale's applicant-facing verification strings. Format arguments use explicit
// indexes (%[1]s) so a translation may reorder them.
type catalog struct {
	GroupBody        string // 1 mention, 2 link clause, 3 seconds, 4 channel hint
	GroupLinkText    string // 1 deep link
	GroupChannelHint string // 1 channel display
	VerifyButton     string

	KernelQuestion string // the kernel-mode question itself, stored with the pending at join time

	QuizPrompt   string // 1 question
	KernelPrompt string // 1 question, 2 replies left
	KernelWrong  string // 1 replies left
	SampleCopied string // the reply was our own printed example, verbatim
	NoLinuxRetry string // they said they have no Linux but left out (or mistyped) the minute
	OSMixed      string // they named another OS but also sent a plausible kernel version

	// FallbackIntro + FallbackQuestions are the escape for an applicant with no Linux installed: a
	// SHORT-ANSWER question whose answer appears nowhere in the message. Never advertised in the
	// prompt — only offered to someone who says they have no Linux — so a spam operator can't learn
	// the path exists, and reading it would not hand them an answer anyway.
	FallbackIntro     string // 1 question, 2 replies left
	FallbackWrong     string // 1 replies left
	FallbackQuestions []ShortQuestion

	AICaught string

	Approved       string
	WrongRetry     string // 1 cooldown seconds
	WrongNoWait    string
	WrongBanned    string
	AlreadyHandled string
	StaleQuestion  string
	NotYours       string

	ChannelFallbackName string // how to refer to a required channel that has no configured display name
	ChannelFirst        string // 1 channel (HTML link)
	FollowPrompt        string // 1 channel (HTML link)
	FollowButton        string // 1 channel display
	ContinueButton      string
	ContinueOK          string
	NotFollowedYet      string // 1 channel display
	NoPending           string
	Renotify            string // 1 outage duration
}

var catalogs = map[lang]*catalog{
	langZH: {
		GroupBody:        "👋 %[1]s 申请加入。请点下方「✅ 点此完成验证」%[2]s 打开机器人私聊完成验证,%[3]d 秒内未完成将被拒绝。%[4]s",
		GroupLinkText:    "(或 <a href=\"%[1]s\">点此</a>)",
		GroupChannelHint: "\n⚠️ 完成验证前还需先关注频道 %[1]s。",
		VerifyButton:     "✅ 点此完成验证",

		KernelQuestion: "你正在运行的 Linux 内核版本号是多少?",

		QuizPrompt:    "请回答下面的问题完成入群验证:\n\n❓ %[1]s",
		KernelPrompt:  "完成入群验证，请回答以下问题。\n\n❓ %[1]s\n\n<b>作答方式</b>\n在终端执行 <code>uname -r</code>，将输出的内核版本号发送至此。\n格式形如 <code>7.1.30</code> 或 <code>7.1.30-gentoo</code>，各发行版均可。\n请发送本机实际版本号，勿使用示例中的版本号。\n\n<b>无 Linux 设备</b>\n回复 <code>无 Linux 设备</code>，并发送当前分钟数。\n分钟数为时钟冒号后两位，例如 14:46 为 <code>46</code>。\n示例：<code>无 Linux 设备46</code>\n\n剩余 %[2]d 次机会。答错或超时将被拒绝。",
		KernelWrong:   "❌ 这不是有效的 Linux 内核版本号。请执行 <code>uname -r</code>，发送其输出。剩余 %[1]d 次机会。",
		SampleCopied:  "你发送的是示例中的版本号，而非本机版本号。请执行 <code>uname -r</code>，发送实际输出。再次发送示例将被拒绝。",
		NoLinuxRetry:  "格式如下：回复 <code>无 Linux 设备</code>，并发送当前分钟数。分钟数为时钟冒号后两位，例如 14:46 为 <code>46</code>。",
		OSMixed:       "你的回复提到了其他系统。若该版本号为本机运行的 Linux 内核（如 WSL 或虚拟机），请仅发送该版本号。",
		FallbackIntro: "没装 Linux 也可以,换一个问题:\n\n❓ %[1]s\n\n直接把答案发到这里即可。还有 %[2]d 次机会。",
		FallbackWrong: "❌ 不对。请再想想上面那个问题,直接把答案发过来。还有 %[1]d 次机会。",
		FallbackQuestions: []ShortQuestion{
			{Q: "Gentoo 中文社区官方网站的域名是什么?", Answers: []string{"gentoozh.org", "gentoozh"}},
			{Q: "Gentoo 官方网站的域名是什么?", Answers: []string{"gentoo.org"}},
		},

		AICaught: "❌ 检测到自动化程序代答,验证已拒绝。",

		Approved:       "✅ 验证通过,已批准加入,欢迎!",
		WrongRetry:     "❌ 答错了,已拒绝。请 %[1]d 秒后重新申请。",
		WrongNoWait:    "❌ 答错了,已拒绝。可重新申请。",
		WrongBanned:    "❌ 验证连续失败多次,已被封禁。",
		AlreadyHandled: "验证已处理,或申请已过期/无法批准,请重新申请。",
		StaleQuestion:  "该题目已过期,请重新打开验证链接获取新题。",
		NotYours:       "这不是你的验证申请,无法操作。",

		ChannelFallbackName: "管理员指定的频道",
		ChannelFirst:        "答案正确。请先关注频道 %[1]s,关注后把答案再发一次即可通过。",
		FollowPrompt:        "完成验证还差一步:请先关注频道 %[1]s,关注后回到本对话点「✅ 我已关注,继续」。",
		FollowButton:        "📢 关注频道 %[1]s",
		ContinueButton:      "✅ 我已关注,继续",
		ContinueOK:          "✅ 已关注,请回答下面的问题",
		NotFollowedYet:      "还没检测到你关注 %[1]s,关注后再点一次。",
		NoPending:           "你当前没有待处理的入群申请。请先申请加入群组,再点群内机器人消息中的「✅ 点此完成验证」按钮。",
		Renotify:            "🔄 抱歉,机器人刚才离线了约 %[1]s,你的入群验证已重新计时。请回到本对话继续答题;若这里没有题目,回群里点「✅ 点此完成验证」重新获取。",
	},
	langZHT: {
		GroupBody:        "👋 %[1]s 申請加入。請點下方「✅ 點此完成驗證」%[2]s 開啟機器人私訊完成驗證,%[3]d 秒內未完成將被拒絕。%[4]s",
		GroupLinkText:    "(或 <a href=\"%[1]s\">點此</a>)",
		GroupChannelHint: "\n⚠️ 完成驗證前還需先加入頻道 %[1]s。",
		VerifyButton:     "✅ 點此完成驗證",

		KernelQuestion: "你正在執行的 Linux 核心(kernel)版本號是多少?",

		QuizPrompt:    "請回答下面的問題完成入群驗證:\n\n❓ %[1]s",
		KernelPrompt:  "完成入群驗證，請回答以下問題。\n\n❓ %[1]s\n\n<b>作答方式</b>\n在終端機執行 <code>uname -r</code>，將輸出的核心版本號傳送至此。\n格式形如 <code>7.1.30</code> 或 <code>7.1.30-gentoo</code>，各發行版皆可。\n請傳送本機實際版本號，勿使用範例中的版本號。\n\n<b>無 Linux 裝置</b>\n回覆 <code>無 Linux 裝置</code>，並傳送當前分鐘數。\n分鐘數為時鐘冒號後兩位，例如 14:46 為 <code>46</code>。\n範例：<code>無 Linux 裝置46</code>\n\n剩餘 %[2]d 次機會。答錯或逾時將被拒絕。",
		KernelWrong:   "❌ 這不是有效的 Linux 核心版本號。請執行 <code>uname -r</code>，傳送其輸出。剩餘 %[1]d 次機會。",
		SampleCopied:  "你傳送的是範例中的版本號，而非本機版本號。請執行 <code>uname -r</code>，傳送實際輸出。再次傳送範例將被拒絕。",
		NoLinuxRetry:  "格式如下：回覆 <code>無 Linux 裝置</code>，並傳送當前分鐘數。分鐘數為時鐘冒號後兩位，例如 14:46 為 <code>46</code>。",
		OSMixed:       "你的回覆提到了其他系統。若該版本號為本機執行的 Linux 核心（如 WSL 或虛擬機），請僅傳送該版本號。",
		FallbackIntro: "還沒安裝 Linux 也可以,換一個問題:\n\n❓ %[1]s\n\n直接把答案傳到這裡即可。還有 %[2]d 次機會。",
		FallbackWrong: "❌ 不對。請再想想上面那個問題,直接把答案傳過來。還有 %[1]d 次機會。",
		FallbackQuestions: []ShortQuestion{
			{Q: "Gentoo 中文社群官方網站的網域是什麼?", Answers: []string{"gentoozh.org", "gentoozh"}},
			{Q: "Gentoo 官方網站的網域是什麼?", Answers: []string{"gentoo.org"}},
		},

		AICaught: "❌ 偵測到自動化程式代答,驗證已拒絕。",

		Approved:       "✅ 驗證通過,已核准加入,歡迎!",
		WrongRetry:     "❌ 答錯了,已拒絕。請 %[1]d 秒後重新申請。",
		WrongNoWait:    "❌ 答錯了,已拒絕。可重新申請。",
		WrongBanned:    "❌ 驗證連續失敗多次,已被封鎖。",
		AlreadyHandled: "驗證已處理,或申請已逾時/無法核准,請重新申請。",
		StaleQuestion:  "該題目已逾時,請重新開啟驗證連結取得新題。",
		NotYours:       "這不是你的驗證申請,無法操作。",

		ChannelFallbackName: "管理員指定的頻道",
		ChannelFirst:        "答案正確。請先加入頻道 %[1]s,加入後把答案再傳一次即可通過。",
		FollowPrompt:        "完成驗證還差一步:請先加入頻道 %[1]s,加入後回到本對話點「✅ 我已加入,繼續」。",
		FollowButton:        "📢 加入頻道 %[1]s",
		ContinueButton:      "✅ 我已加入,繼續",
		ContinueOK:          "✅ 已加入頻道,請回答下面的問題",
		NotFollowedYet:      "尚未偵測到你已加入 %[1]s,加入後再點一次。",
		NoPending:           "你目前沒有待處理的入群申請。請先申請加入群組,再點群內機器人訊息中的「✅ 點此完成驗證」按鈕。",
		Renotify:            "🔄 抱歉,機器人剛才離線了約 %[1]s,你的入群驗證已重新計時。請回到本對話繼續答題;若這裡沒有題目,回群組點「✅ 點此完成驗證」重新取得。",
	},
	langEN: {
		GroupBody:        "👋 %[1]s asked to join. Tap “✅ Verify” below %[2]s to open the bot in DM and finish verification. The request is declined after %[3]d seconds.%[4]s",
		GroupLinkText:    "(or <a href=\"%[1]s\">this link</a>)",
		GroupChannelHint: "\n⚠️ You must also follow the channel %[1]s before you can be approved.",
		VerifyButton:     "✅ Verify",

		KernelQuestion: "What is the version of the Linux kernel you are running?",

		QuizPrompt:    "Answer this question to finish joining:\n\n❓ %[1]s",
		KernelPrompt:  "Answer the following to finish joining.\n\n❓ %[1]s\n\n<b>How to answer</b>\nRun <code>uname -r</code> in a terminal and send the kernel version here.\nThe format looks like <code>7.1.30</code> or <code>7.1.30-gentoo</code>; any distribution's version is accepted.\nSend your machine's actual version, not the example.\n\n<b>No Linux device</b>\nReply <code>no Linux device</code> and send the current minute.\nThe minute is the two digits after the colon on your clock — at 14:46 that is <code>46</code>.\nExample: <code>no Linux device 46</code>\n\n%[2]d attempts left. A wrong answer or a timeout declines the request.",
		KernelWrong:   "❌ That is not a valid Linux kernel version. Run <code>uname -r</code> and send its output. %[1]d attempts left.",
		SampleCopied:  "You sent the example version, not your machine's. Run <code>uname -r</code> and send its actual output. Sending the example again will be declined.",
		NoLinuxRetry:  "Format: reply <code>no Linux device</code> and send the current minute — the two digits after the colon on your clock (14:46 → <code>46</code>).",
		OSMixed:       "Your reply mentions another operating system. If that version is the Linux kernel your machine runs (e.g. WSL or a VM), send only the version number.",
		FallbackIntro: "No Linux installed? Then answer this instead:\n\n❓ %[1]s\n\nSend the answer here. %[2]d attempts left.",
		FallbackWrong: "❌ Not right. Think about the question above and send the answer. %[1]d attempts left.",
		FallbackQuestions: []ShortQuestion{
			{Q: "What is the domain of the Gentoo Chinese community's website?", Answers: []string{"gentoozh.org", "gentoozh"}},
			{Q: "What is the domain of the official Gentoo website?", Answers: []string{"gentoo.org"}},
		},

		AICaught: "❌ An automated agent answered on your behalf. Verification declined.",

		Approved:       "✅ Verified — your join request was approved. Welcome!",
		WrongRetry:     "❌ Wrong answer, request declined. You can apply again in %[1]d seconds.",
		WrongNoWait:    "❌ Wrong answer, request declined. You may apply again.",
		WrongBanned:    "❌ Too many failed verifications — you have been banned.",
		AlreadyHandled: "This verification is already handled, or the request expired / can't be approved. Please apply again.",
		StaleQuestion:  "This question has expired. Open the verification link again for a new one.",
		NotYours:       "This isn't your join request.",

		ChannelFallbackName: "the channel set by the admins",
		ChannelFirst:        "Correct. Now follow the channel %[1]s, then send the answer once more to be approved.",
		FollowPrompt:        "One more step: follow the channel %[1]s, then come back here and tap “✅ I've followed, continue”.",
		FollowButton:        "📢 Follow %[1]s",
		ContinueButton:      "✅ I've followed, continue",
		ContinueOK:          "✅ Followed — here is your question",
		NotFollowedYet:      "I can't see you in %[1]s yet. Follow it, then tap again.",
		NoPending:           "You have no pending join request. Ask to join the group first, then tap “✅ Verify” on the bot's message there.",
		Renotify:            "🔄 Sorry — the bot was offline for about %[1]s, so your verification window was restarted. Continue here; if there is no question, tap “✅ Verify” in the group again.",
	},
}

// tr returns the catalog for l, falling back to Simplified Chinese (this community's language).
func tr(l lang) *catalog {
	if c, ok := catalogs[l]; ok {
		return c
	}
	return catalogs[langZH]
}
