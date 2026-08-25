package verify

import (
	"context"
	"html"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/store"
	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
)

// Persist the localized question so recovery renders the same challenge.
func kernelQuestion(messages *i18n.Catalog, l i18n.Lang) string {
	return messages.Verification.Challenge.KernelQuestion.For(l)
}

// Three replies tolerate typos while bounding DM guess floods.
const kernelMaxTries = 3

// ModeName returns the operator-facing label for a challenge mode.
func ModeName(l i18n.Lang, mode string) string {
	labels := &i18n.Messages.Verification.Mode
	switch mode {
	case config.ModeKernel:
		return labels.Kernel.For(l)
	case config.ModeQuiz:
		return labels.Quiz.For(l)
	case config.ModeMixed:
		return labels.Mixed.For(l)
	}
	return mode
}

// Accept a release alone or in known kernel context; arbitrary ASCII prose must not make
// product or model versions valid answers.
const kernelReleasePattern = `[vV]?(\d{1,3}(?:\.\d{1,6}){1,3})(?:[-+_][0-9A-Za-z][0-9A-Za-z._+-]*)?`

var (
	kernelReleaseRe           = regexp.MustCompile(`^` + kernelReleasePattern + `$`)
	kernelReleaseTokenRe      = regexp.MustCompile(kernelReleasePattern)
	kernelContextWordRe       = regexp.MustCompile(`[-#]?[0-9A-Za-z](?:[0-9A-Za-z_./+-]*[0-9A-Za-z])?`)
	kernelHostnameRe          = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$`)
	kernelDateNumberRe        = regexp.MustCompile(`^(?:[0-9]{1,2}|[0-9]{4})$`)
	wslKernelOutputRe         = regexp.MustCompile(`(?i)^Windows\s+WSL[0-9]*(?:\s+kernel\s+|\s*,\s*)` + kernelReleasePattern + `$`)
	kernelMultiVersionOutputs = [...]*regexp.Regexp{
		regexp.MustCompile(`^Linux version ` + kernelReleasePattern + `(?:\s+\(.+|\s+#.+)$`),
		regexp.MustCompile(`(?i)^(?:uname\s+-a\s*:?\s*)?Linux\s+\S+\s+` + kernelReleasePattern + `\s+.+\sGNU/Linux$`),
	}
)

// Keep ASCII context narrow, but include normal uname fields so honest retries are not consumed.
var benignKernelContextWords = map[string]struct{}{
	"#1": {}, "-a": {}, "-r": {}, "-sr": {},
	"linux": {}, "uname": {}, "gnu/linux": {},
	"smp": {}, "preempt": {}, "preempt_dynamic": {},
	"x86_64": {}, "amd64": {}, "aarch64": {}, "arm64": {}, "i686": {},
	"armv7l": {}, "armv8l": {}, "riscv64": {}, "ppc64le": {}, "s390x": {},
	"kernel": {}, "version": {}, "my": {}, "is": {}, "it": {}, "the": {},
	"on": {}, "running": {}, "now": {}, "currently": {}, "here": {}, "use": {}, "using": {},
	"i": {}, "am": {},
	"mon": {}, "tue": {}, "wed": {}, "thu": {}, "fri": {}, "sat": {}, "sun": {},
	"jan": {}, "feb": {}, "mar": {}, "apr": {}, "may": {}, "jun": {},
	"jul": {}, "aug": {}, "sep": {}, "oct": {}, "nov": {}, "dec": {},
	"utc": {}, "gmt": {},
}

// Historical 0.x–2.x lines are bounded; 3.x–30.x leaves decades of future headroom.
// Rejecting implausible major/minor pairs keeps arbitrary dotted numbers out.
func plausibleKernel(major, minor int) bool {
	switch {
	case major == 0: // 0.01 … 0.99: the 1991 kernels
		return minor >= 1 && minor <= 99
	case major == 1:
		return minor <= 3
	case major == 2:
		return minor <= 6
	case major >= 3 && major <= 30: // 3.x … today's 7.x and decades of future majors
		return minor <= 99
	}
	return false
}

// Unknown ASCII context rejects otherwise plausible dotted versions; Chinese prose is allowed.
func kernelAnswerOK(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	if m := kernelReleaseRe.FindStringSubmatch(text); m != nil {
		return kernelVersionOK(m[1])
	}
	matches := kernelReleaseTokenRe.FindAllStringIndex(text, -1)
	if len(matches) != 1 {
		// Anchored /proc/version and uname shapes may contain compiler or package versions.
		for _, re := range kernelMultiVersionOutputs {
			if m := re.FindStringSubmatch(text); m != nil {
				return kernelVersionOK(m[1])
			}
		}
		return false
	}
	if m := wslKernelOutputRe.FindStringSubmatch(text); m != nil {
		return kernelVersionOK(m[1])
	}
	match := matches[0]
	release := text[match[0]:match[1]]
	m := kernelReleaseRe.FindStringSubmatch(release)
	if m == nil || !kernelVersionOK(m[1]) {
		return false
	}
	return benignKernelContext(text[:match[0]], text[match[1]:], kernelReleaseDistribution(release))
}

func kernelVersionOK(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) > 4 {
		return false
	}
	for _, p := range parts[1:] {
		if len(p) > 4 { // no kernel has had a five-digit sublevel; a Windows build does
			return false
		}
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	return err1 == nil && err2 == nil && plausibleKernel(major, minor)
}

// A distribution word is trustworthy only when the release token repeats it as a suffix segment.
func kernelReleaseDistribution(release string) string {
	release = strings.ToLower(release)
	for _, suffix := range []string{"-gentoo", "_gentoo"} {
		if i := strings.Index(release, suffix); i >= 0 {
			end := i + len(suffix)
			if end == len(release) || strings.ContainsRune("._+-", rune(release[end])) {
				return "gentoo"
			}
		}
	}
	return ""
}

func benignKernelContext(before, after, distribution string) bool {
	beforeWords := kernelContextWordRe.FindAllString(before, -1)
	unameShape := len(beforeWords) > 0 && strings.EqualFold(beforeWords[0], "linux")
	for i, word := range beforeWords {
		word = strings.ToLower(word)
		if _, ok := benignKernelContextWords[word]; ok {
			continue
		}
		if word == distribution {
			continue
		}
		// In `uname` output the hostname immediately follows Linux and is inherently operator-chosen.
		if i == 1 && unameShape && kernelHostnameRe.MatchString(word) {
			continue
		}
		return false
	}
	// Everything after the release token in `uname -a` is emitted by the kernel and the machine:
	// the build id, the build date in the builder's timezone, the architecture, and on many systems
	// the CPU model ("AMD Ryzen 9 9950X3D 16-Core Processor AuthenticAMD"). Vetting those words
	// against a vocabulary rejects real machines for owning an unlisted timezone or a new CPU, which
	// costs an honest applicant an attempt. Once the reply is anchored as uname output — it starts
	// with Linux and the release is followed by the #<build> field — the tail carries no signal
	// worth policing, so stop there.
	if unameShape && kernelUnameTailRe.MatchString(after) {
		return true
	}
	for _, word := range kernelContextWordRe.FindAllString(after, -1) {
		word = strings.ToLower(word)
		if _, ok := benignKernelContextWords[word]; ok {
			continue
		}
		if word == distribution {
			continue
		}
		if unameShape && kernelDateNumberRe.MatchString(word) {
			continue
		}
		return false
	}
	return true
}

// kernelUnameTailRe matches the `#<build>` field that follows the release in `uname -a` output.
var kernelUnameTailRe = regexp.MustCompile(`^\s*#\d+`)

// SetVerifyMode updates one group's challenge mode or restores its configured baseline.
func (v *Service) SetVerifyMode(groupID int64, mode string) error {
	return v.updateGroupSettings(groupID, func(_ store.GroupView, overrides *store.GroupOverrides) {
		if mode == "" {
			overrides.VerifyMode = nil
			return
		}
		overrides.VerifyMode = &mode
	})
}

// EffectiveMode returns one group's current challenge mode.
func (v *Service) EffectiveMode(groupID int64) string {
	group, ok := v.groupSettings(groupID)
	if !ok {
		return config.ModeKernel
	}
	return group.VerifyMode().Value
}

func (v *Service) questions(groupID int64) []config.Question {
	group, ok := v.groupSettings(groupID)
	if !ok {
		return nil
	}
	return group.Questions().Value
}

// Mixed mode uses a cryptographic coin flip; an empty quiz pool falls back to kernel.
func (v *Service) pickMode(gid int64) string {
	mode := v.EffectiveMode(gid)
	if mode == (config.ModeMixed) {
		mode = (config.ModeQuiz)
		if cryptoIntn(2) == 0 {
			mode = (config.ModeKernel)
		}
	}
	if mode == (config.ModeQuiz) && len(v.questions(gid)) == 0 {
		return config.ModeKernel
	}
	return mode
}

// Kernel challenges have no options and use correctIdx -1.
func (v *Service) newChallenge(gid int64, ul i18n.Lang) (mode, text string, opts []string, correctIdx int) {
	mode = v.pickMode(gid)
	if mode == (config.ModeKernel) {
		return mode, kernelQuestion(v.messages, ul), nil, -1
	}
	text, opts, correctIdx = shuffledQuestion(randomQuestion(v.questions(gid)))
	return mode, text, opts, correctIdx
}

// Render both expandable and legacy-compatible versions of the localized DM prompt.
func kernelPromptHTML(messages *i18n.Catalog, l i18n.Lang, question string, left int, nonce string, expandable bool) string {
	if left < 1 {
		left = 1 // a live pending always has at least one reply left; never advertise zero
	}
	prompt := messages.Verification.Challenge.KernelPrompt.Render(l, html.EscapeString(question), left)
	return prompt + "\n\n" + aiTrapLine(messages, l, nonce, expandable)
}

// Derive the tripwire token per pending so it cannot be filtered or guessed in advance.
func aiTrapToken(nonce string) string {
	if nonce == "" {
		return "AGENT-STOP"
	}
	return "AGENT-" + strings.ToUpper(nonce)
}

// The tripwire asks automated agents for an exact nonce-bound token and model declaration.
// It is only a deterrent; typed answers, deadlines, cooldowns, and strikes remain the gate.
// Localized copy keeps the legacy plain rendering readable when expandable markup is unavailable.
func aiTrapLine(messages *i18n.Catalog, l i18n.Lang, nonce string, expandable bool) string {
	body := messages.Verification.Challenge.AgentTrap.Render(l, aiTrapToken(nonce))
	if expandable {
		return "<blockquote expandable>" + body + "</blockquote>"
	}
	return body
}

// Require the exact reply shape; prompt quotations also contain the token.
var aiTrapReplyRe = regexp.MustCompile(`(?i)^model=[0-9a-z][0-9a-z.:_/+-]*$`)

func aiTrapped(text, nonce string) bool {
	text = strings.TrimSpace(text)
	token := aiTrapToken(nonce)
	if len(text) <= len(token) || !strings.EqualFold(text[:len(token)], token) || text[len(token)] != ' ' {
		return false
	}
	return aiTrapReplyRe.MatchString(strings.TrimSpace(text[len(token)+1:]))
}

// The impossible placeholder cannot collide with a real release.
const samplePrompt = "X.Y.Z-gentoo"

func copiedSample(text string) bool {
	return strings.EqualFold(strings.TrimSpace(text), samplePrompt)
}

// Operator fallback questions override the localized built-in questions.
func (v *Service) fallbackQuestion(groupID int64, l i18n.Lang) (string, []string) {
	var questions []config.ShortQuestion
	if group, ok := v.groupSettings(groupID); ok && !group.FallbackBuiltin().Value {
		questions = group.FallbackQuestions().Value
	}
	if len(questions) != 0 {
		question := questions[cryptoIntn(len(questions))]
		return question.Q, question.Answers
	}
	builtin := v.messages.Verification.Challenge.FallbackQuestions
	return builtin[cryptoIntn(len(builtin))].For(l)
}

// Accept one normalized whole reply, never a matching word embedded in prose.
func fallbackAnswerOK(text string, answers []string) bool {
	text = normalizeFallbackAnswer(text)
	if text == "" {
		return false
	}
	for _, answer := range answers {
		if text == normalizeFallbackAnswer(answer) {
			return true
		}
	}
	return false
}

func normalizeFallbackAnswer(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	text = strings.TrimSpace(strings.TrimFunc(text, unicode.IsPunct))
	text = strings.TrimPrefix(text, "https://")
	text = strings.TrimPrefix(text, "http://")
	text = strings.TrimPrefix(text, "www.")
	return strings.TrimSpace(strings.TrimFunc(text, unicode.IsPunct))
}

// No-Linux phrases switch to a fallback without consuming an attempt.
// Detect other operating systems before version parsing so their build numbers cannot pass.
func mentionsOtherOS(text string) bool {
	low := strings.ToLower(text)
	for _, l := range i18n.Languages() {
		for _, phrase := range i18n.Messages.Verification.Input.OtherOSPhrases.For(l) {
			if strings.Contains(low, phrase) {
				return true
			}
		}
	}
	return false
}

// One minute of clock and typing slack keeps the proof narrow.
const minuteSlack = 1

// Supported timezone minute offsets are 0, 30, and 45; extra shifts widen blind guesses.
var minuteShifts = [3]int{0, 30, 45}

var minuteNumber = regexp.MustCompile(`[0-9]+`)

// Use only the last minute claim so number lists do not become multiple guesses.
// Accept clock slack and real timezone minute offsets.
func minuteProofOK(text string, now time.Time) bool {
	claimed, ok := claimedMinute(text)
	if !ok {
		return false
	}
	cur := now.Minute()
	for _, shift := range minuteShifts {
		d := ((claimed-cur-shift)%60%60 + 60) % 60
		if d <= minuteSlack || d >= 60-minuteSlack {
			return true
		}
	}
	return false
}

// Use the last standalone 0–59 token and normalize full-width digits.
func claimedMinute(text string) (int, bool) {
	text = normalizeFullWidthDigits(text)
	claimed := -1
	for _, match := range minuteNumber.FindAllStringIndex(text, -1) {
		token := text[match[0]:match[1]]
		if len(token) > 2 {
			continue
		}
		n, err := strconv.Atoi(token)
		if err == nil && n <= 59 {
			claimed = n
		}
	}
	if claimed < 0 {
		return 0, false
	}
	return claimed, true
}

func normalizeFullWidthDigits(text string) string {
	if !strings.ContainsAny(text, "０１２３４５６７８９") {
		return text
	}
	return strings.Map(func(r rune) rune {
		if r >= '０' && r <= '９' {
			return '0' + r - '０'
		}
		return r
	}, text)
}

// No-Linux declarations receive the fallback rather than a strike.
func saysNoLinux(text string) bool {
	text = strings.ToLower(strings.Join(strings.Fields(text), ""))
	for _, l := range i18n.Languages() {
		for _, phrase := range i18n.Messages.Verification.Input.NoLinuxPhrases.For(l) {
			phrase = strings.ToLower(strings.Join(strings.Fields(phrase), ""))
			if strings.Contains(text, phrase) {
				return true
			}
		}
	}
	return false
}

// The fallback carries the same agent tripwire as the kernel prompt.
func fallbackPromptHTML(messages *i18n.Catalog, l i18n.Lang, question string, left int, nonce string, expandable bool) string {
	if left < 1 {
		left = 1
	}
	prompt := messages.Verification.Challenge.FallbackIntro.Render(l, html.EscapeString(question), left)
	return prompt + "\n\n" + aiTrapLine(messages, l, nonce, expandable)
}

// Route DMs only after the current kernel or fallback question was confirmed delivered.
func (v *Service) hasKernelPending(uid int64) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	for k, p := range v.pend {
		// Before prompting, the applicant may have seen only the channel-follow step.
		if k.uid == uid && !p.done && p.mode == config.ModeKernel && p.prompted && !p.fallbackPending {
			return true
		}
	}
	return false
}

// One DM answer settles all simultaneously pending groups with confirmed prompts.
func (v *Service) kernelPendingGroups(uid int64) []int64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	var gids []int64
	for k, p := range v.pend {
		if k.uid == uid && !p.done && p.mode == config.ModeKernel && p.prompted && !p.fallbackPending {
			gids = append(gids, k.gid)
		}
	}
	return gids
}

// KernelAnswerDM reports whether a private text message should be graded as a kernel answer.
func (v *Service) KernelAnswerDM(_ context.Context, update telego.Update) bool {
	m := update.Message
	if m == nil || m.From == nil || m.Chat.Type != "private" {
		return false
	}
	if t := strings.TrimSpace(m.Text); t == "" || strings.HasPrefix(t, "/") {
		return false
	}
	return v.hasKernelPending(m.From.ID)
}

// OnKernelAnswer grades one private kernel or fallback answer.
func (v *Service) OnKernelAnswer(ctx *th.Context, update telego.Update) error {
	msg := update.Message
	if msg == nil || msg.From == nil {
		return nil
	}
	bot := ctx.Bot()
	c := ctx.Context()
	uid := msg.From.ID
	// Classify once: a model version could otherwise trip one group and pass another as a kernel.
	// One reply also records only one tally entry.
	if gid, nonce, tripped := v.trippedPending(uid, msg.Text); tripped {
		v.declineAgent(c, bot, gid, uid, nonce, msg.Text)
		for _, other := range v.kernelPendingGroups(uid) {
			if other != gid {
				v.declineAgent(c, bot, other, uid, "", msg.Text) // same reply, same verdict, no second tally
			}
		}
		return nil
	}
	for _, gid := range v.kernelPendingGroups(uid) {
		v.gradeKernelAnswer(c, bot, gid, uid, msg.Text)
	}
	return nil
}

// A nonce-derived tripwire can match at most one pending.
func (v *Service) trippedPending(uid int64, text string) (gid int64, nonce string, ok bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for k, p := range v.pend {
		if k.uid == uid && !p.done && p.mode == config.ModeKernel && p.prompted && !p.fallbackPending &&
			aiTrapped(text, p.nonce) {
			return k.gid, p.nonce, true
		}
	}
	return 0, "", false
}

// Decline every affected group, but tally the one reply only once.
func (v *Service) declineAgent(c context.Context, bot modBot, gid, uid int64, nonce, text string) {
	ul, cur, _, ok := v.kernelPendingInfo(gid, uid)
	if !ok {
		return
	}
	if nonce != "" {
		model, total := v.recordAgent(text)
		log.Printf("verify: automated-agent tripwire triggered by %d in %d (model %q, %d total) — declining", uid, gid, model, total)
		alert := v.messages.Verification.Admin.AgentCaught.Render(v.groupLanguage(gid), uid, gid, model, total)
		v.adminAlert(c, bot, alert)
	} else {
		log.Printf("verify: declining %d in %d — the same reply tripped the tripwire in another group", uid, gid)
	}
	handled, settled, banned := v.decline(c, bot, gid, uid, cur, "wrong answer")
	if !handled {
		return
	}
	msg := v.messages.Verification.Result.DeclinePending.For(ul)
	if settled {
		msg = v.agentCaughtText(gid, ul, banned)
	}
	_, _ = bot.SendMessage(c, tu.Message(tu.ID(uid), msg))
}

// A plausible version passes after the channel gate; the final failed reply declines.
func (v *Service) gradeKernelAnswer(c context.Context, bot modBot, gid, uid int64, text string) {
	ul, nonce, fbAnswers, ok := v.kernelPendingInfo(gid, uid)
	if !ok {
		return // handled or replaced meanwhile
	}
	groupLang := v.groupLanguage(gid)
	challenge := &v.messages.Verification.Challenge
	// Tripwire compliance declines immediately and counts as a normal failed verification.
	if aiTrapped(text, nonce) {
		v.declineAgent(c, bot, gid, uid, nonce, text)
		return
	}
	// Guard every acceptance path from the prompt's impossible example; only the first copy is free.
	if copiedSample(text) && v.markSampleBounced(gid, uid, nonce) {
		v.save()
		_, _ = bot.SendMessage(c, htmlMessage(uid, challenge.SampleCopied.For(ul)))
		return
	}
	// Fallback answers are authoritative, but a real kernel remains acceptable.
	if len(fbAnswers) > 0 {
		if fallbackAnswerOK(text, fbAnswers) || (kernelAnswerOK(text) && !mentionsOtherOS(text)) {
			v.finishKernelPass(c, bot, gid, uid, nonce, ul, groupLang)
			return
		}
		left, curNonce, ok := v.recordKernelTry(gid, uid, nonce)
		if !ok {
			return
		}
		if left > 0 {
			v.save()
			_, _ = bot.SendMessage(c, htmlMessage(uid, challenge.FallbackWrong.Render(ul, left)))
			return
		}
		// Decline only the nonce charged by recordKernelTry, never a replacement pending.
		handled, settled, banned := v.decline(c, bot, gid, uid, curNonce, "wrong answer")
		if !handled {
			return
		}
		msg := v.messages.Verification.Result.DeclinePending.For(ul)
		if settled {
			msg = v.wrongAnswerText(gid, ul, banned)
		}
		_, _ = bot.SendMessage(c, tu.Message(tu.ID(uid), msg))
		return
	}
	// Give WSL or VM users one free clarification before accepting the same real kernel.
	if mentionsOtherOS(text) && kernelAnswerOK(text) && v.markOSClarified(gid, uid, nonce) {
		v.save()
		_, _ = bot.SendMessage(c, htmlMessage(uid, challenge.OSMixed.For(ul)))
		return
	}
	if !kernelAnswerOK(text) { // another system's build number is not a kernel version
		// Offer the answer-hidden short question once and without charging an attempt.
		// It remains a typed-knowledge gate, not a click path.
		if saysNoLinux(text) || mentionsOtherOS(text) {
			// The current minute proves the advertised escape is not a canned reply.
			// One malformed attempt gets a free format reminder.
			if !minuteProofOK(text, time.Now()) {
				if v.markNoLinuxReminded(gid, uid, nonce) {
					_, _ = bot.SendMessage(c, htmlMessage(uid, challenge.NoLinuxRetry.For(ul)))
					return
				}
			} else {
				qText, answers := v.fallbackQuestion(gid, ul)
				if v.beginKernelFallback(bot, gid, uid, nonce, qText, answers) {
					v.save()
					prompt, current := v.pendingDMChallenge(gid, uid)
					if !current {
						return
					}
					result, _ := v.sendDMQuestion(c, bot, uid, prompt)
					if result.stateChanged {
						v.save()
					}
					if !result.current {
						v.deleteChallenge(c, bot, uid, result.messageID)
					}
					return
				}
			}
		}
		left, curNonce, ok := v.recordKernelTry(gid, uid, nonce)
		if !ok {
			return
		}
		if left > 0 {
			v.save() // keep the used-up tries across a restart
			_, _ = bot.SendMessage(c, htmlMessage(uid, challenge.KernelWrong.Render(ul, left)))
			return
		}
		handled, settled, banned := v.decline(c, bot, gid, uid, curNonce, "wrong answer") // the nonce as of the charge, see above
		if !handled {
			return
		}
		msg := v.messages.Verification.Result.DeclinePending.For(ul)
		if settled {
			msg = v.wrongAnswerText(gid, ul, banned)
		}
		_, _ = bot.SendMessage(c, tu.Message(tu.ID(uid), msg))
		return
	}
	v.finishKernelPass(c, bot, gid, uid, nonce, ul, groupLang)
}

// Nonce-bind approval across the channel lookup so a stale answer cannot settle a replacement.
func (v *Service) finishKernelPass(c context.Context, bot modBot, gid, uid int64, nonce string, ul, groupLang i18n.Lang) {
	channel := &v.messages.Verification.Channel
	result := &v.messages.Verification.Result
	if !v.isChannelMember(c, bot, gid, uid, groupLang) {
		message := channel.First.Render(ul, v.channelLinkHTML(gid, ul))
		_, _ = bot.SendMessage(c, htmlMessage(uid, message))
		return
	}
	p, ok := v.claimPendingNonce(gid, uid, nonce)
	if ok && v.executeApprove(c, bot, gid, uid, p) {
		_, _ = bot.SendMessage(c, tu.Message(tu.ID(uid), result.Approved.For(ul)))
		return
	}
	_, _ = bot.SendMessage(c, tu.Message(tu.ID(uid), result.AlreadyHandled.For(ul)))
}

// Return only live, confirmed pending data needed for grading.
func (v *Service) kernelPendingInfo(gid, uid int64) (ul i18n.Lang, nonce string, fbAnswers []string, ok bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	p, exists := v.pend[pkey{gid, uid}]
	if !exists || p.done || p.fallbackPending {
		return i18n.LangZH, "", nil, false
	}
	return p.lang, p.nonce, p.fbAnswers, true
}

func (v *Service) kernelTriesUsed(gid, uid int64) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	if p, ok := v.pend[pkey{gid, uid}]; ok {
		return p.tries
	}
	return 0
}

// Prepare a hidden fallback and suspend grading until its prompt delivery is confirmed.
func (v *Service) beginKernelFallback(bot verifyBot, gid, uid int64, nonce, question string, answers []string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	p, ok := v.pend[pkey{gid, uid}]
	if !ok || p.done || p.nonce != nonce || p.hinted || p.fallbackPending {
		return false
	}
	p.hinted = true
	p.qText = question
	p.fbAnswers = append([]string(nil), answers...)
	p.fallbackPending = true
	if p.timer != nil {
		p.timer.Stop()
	}
	p.deadline = v.wallNow().Add(pendingDeliveryTimeout)
	v.armExpiry(bot, p, gid, uid, pendingDeliveryTimeout, challengeExpiryReason(false))
	return true
}

// A malformed no-Linux declaration receives only one free reminder.
func (v *Service) markNoLinuxReminded(gid, uid int64, nonce string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	p, ok := v.pend[pkey{gid, uid}]
	if !ok || p.done || p.nonce != nonce || p.noLinuxReminded {
		return false // gone, handled, or a newer request now holds this key
	}
	p.noLinuxReminded = true
	return true
}

// Clarify a mixed OS/kernel reply once rather than looping a valid WSL user toward a ban.
func (v *Service) markOSClarified(gid, uid int64, nonce string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	p, ok := v.pend[pkey{gid, uid}]
	if !ok || p.done || p.nonce != nonce || p.osClarified {
		return false
	}
	p.osClarified = true
	return true
}

// The copied-example nudge is free only once.
func (v *Service) markSampleBounced(gid, uid int64, nonce string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	p, ok := v.pend[pkey{gid, uid}]
	if !ok || p.done || p.nonce != nonce || p.sampleBounced {
		return false
	}
	p.sampleBounced = true
	return true
}

// Return the nonce charged with the failed reply so decline cannot claim a replacement.
func (v *Service) recordKernelTry(gid, uid int64, want string) (left int, nonce string, ok bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	p, exists := v.pend[pkey{gid, uid}]
	if !exists || p.done || p.nonce != want {
		return 0, "", false // a newer request replaced this pending: never charge IT for an old reply
	}
	p.tries++
	left = kernelMaxTries - p.tries
	if left < 0 {
		left = 0
	}
	return left, p.nonce, true
}
