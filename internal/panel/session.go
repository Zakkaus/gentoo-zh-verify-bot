package panel

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/config"
	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/i18n"
	"github.com/mymmrac/telego"
)

const (
	panelSessionTTL = 30 * time.Minute
	panelSessionCap = 256
	panelPageSize   = 8
)

type inputKind string

const (
	inputBanDuration      inputKind = "bd"
	inputLookupTTL        inputKind = "lt"
	inputTimeout          inputKind = "to"
	inputMaxFails         inputKind = "mf"
	inputRetryCooldown    inputKind = "rc"
	inputPrivateRate      inputKind = "pr"
	inputQuizQuestion     inputKind = "qq"
	inputQuizOption       inputKind = "qo"
	inputFallbackQuestion inputKind = "fq"
	inputFallbackAnswer   inputKind = "fa"
	inputInviteURL        inputKind = "iu"
	inputChannelWhitelist inputKind = "cw"
	inputTrustedGroup     inputKind = "tg"
	inputKnownChat        inputKind = "kc"
	inputRequiredChannel  inputKind = "ci"
	inputMuteDuration     inputKind = "md"
	inputWarnLimit        inputKind = "wl"
	inputAlertChat        inputKind = "al"
)

type pendingInput struct {
	kind             inputKind
	parent           string
	promptMessageID  int
	requestID        int32
	requestAltID     int32
	expectedRevision uint64
}

type quizDraft struct {
	index    int
	existing bool
	question config.Question
	revision uint64
}

type fallbackDraft struct {
	index    int
	existing bool
	question config.ShortQuestion
	revision uint64
}

type channelDraft struct {
	id      int64
	display string
}

type confirmation struct {
	kind     string
	index    int
	revision uint64
}

type panelSession struct {
	mu             sync.Mutex
	token          string
	ownerID        int64
	anchorGroupID  int64
	groupID        int64
	chatID         int64
	messageID      int
	language       i18n.Lang
	screen         string
	page           int
	listKind       inputKind
	revision       uint64
	globalRevision uint64
	pending        *pendingInput
	quiz           *quizDraft
	fallback       *fallbackDraft
	channel        *channelDraft
	confirm        *confirmation
	createdAt      time.Time
	expiresAt      time.Time
}

type promptKey struct {
	userID    int64
	messageID int
}

type inputTombstone struct {
	language  i18n.Lang
	expiresAt time.Time
}

type settingsPanelState struct {
	launchMu   sync.Mutex
	mu         sync.Mutex
	byToken    map[string]*panelSession
	byUser     map[int64]*panelSession
	tombstones map[promptKey]inputTombstone
	botID      int64
	botName    string
}

func newSettingsPanelState() *settingsPanelState {
	return &settingsPanelState{
		byToken:    make(map[string]*panelSession),
		byUser:     make(map[int64]*panelSession),
		tombstones: make(map[promptKey]inputTombstone),
	}
}

func (v *Panel) newSettingsSession(ownerID, anchorGroupID int64, language i18n.Lang) (*panelSession, error) {
	token, err := newPanelToken()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	session := &panelSession{
		token: token, ownerID: ownerID, anchorGroupID: anchorGroupID, groupID: anchorGroupID,
		chatID: ownerID, language: language, screen: "gl", createdAt: now, expiresAt: now.Add(panelSessionTTL),
	}
	state := v.panelState
	state.launchMu.Lock()
	defer state.launchMu.Unlock()

	state.mu.Lock()
	state.pruneLocked(now)
	previous := state.byUser[ownerID]
	if previous != nil {
		delete(state.byToken, previous.token)
		delete(state.byUser, ownerID)
	}
	if len(state.byToken) >= panelSessionCap {
		state.mu.Unlock()
		return nil, errors.New("panel session capacity reached")
	}
	if state.byToken[token] != nil {
		state.mu.Unlock()
		return nil, errors.New("panel token collision")
	}
	state.mu.Unlock()

	if previous != nil {
		previous.mu.Lock()
		if previous.pending != nil {
			v.rememberCanceledPrompt(previous, previous.pending)
			previous.pending = nil
		}
		previous.mu.Unlock()
	}

	state.mu.Lock()
	state.byToken[token] = session
	state.byUser[ownerID] = session
	state.mu.Unlock()
	return session, nil
}

func (s *settingsPanelState) pruneLocked(now time.Time) {
	for token, session := range s.byToken {
		if !now.Before(session.expiresAt) {
			delete(s.byToken, token)
			if s.byUser[session.ownerID] == session {
				delete(s.byUser, session.ownerID)
			}
		}
	}
	for key, tombstone := range s.tombstones {
		if !now.Before(tombstone.expiresAt) {
			delete(s.tombstones, key)
		}
	}
}

func (v *Panel) sessionByToken(token string) *panelSession {
	state := v.panelState
	state.mu.Lock()
	defer state.mu.Unlock()
	state.pruneLocked(time.Now())
	return state.byToken[token]
}

func (v *Panel) sessionByUser(userID int64) *panelSession {
	state := v.panelState
	state.mu.Lock()
	defer state.mu.Unlock()
	state.pruneLocked(time.Now())
	return state.byUser[userID]
}

func (v *Panel) rotateSessionToken(session *panelSession, token string) bool {
	state := v.panelState
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.byToken[session.token] != session || state.byUser[session.ownerID] != session {
		return false
	}
	delete(state.byToken, session.token)
	session.token = token
	state.byToken[token] = session
	return true
}

func (v *Panel) removeSession(session *panelSession) {
	state := v.panelState
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.byToken[session.token] == session {
		delete(state.byToken, session.token)
	}
	if state.byUser[session.ownerID] == session {
		delete(state.byUser, session.ownerID)
	}
}

func (v *Panel) rememberCanceledPrompt(session *panelSession, pending *pendingInput) {
	if pending == nil || pending.promptMessageID == 0 {
		return
	}
	state := v.panelState
	state.mu.Lock()
	state.tombstones[promptKey{userID: session.ownerID, messageID: pending.promptMessageID}] = inputTombstone{
		language: session.language, expiresAt: session.expiresAt,
	}
	state.mu.Unlock()
}

func (v *Panel) consumeTombstone(key promptKey) (inputTombstone, bool) {
	state := v.panelState
	state.mu.Lock()
	defer state.mu.Unlock()
	state.pruneLocked(time.Now())
	value, ok := state.tombstones[key]
	if ok {
		delete(state.tombstones, key)
	}
	return value, ok
}

func (v *Panel) kernelPending(userID int64) bool {
	if v.verifier == nil {
		return false
	}
	return v.verifier.KernelAnswerDM(context.Background(), telego.Update{Message: &telego.Message{
		Chat: telego.Chat{ID: userID, Type: "private"}, From: &telego.User{ID: userID}, Text: "x",
	}})
}

// PanelInputDM matches only an exact live ForceReply prompt or a canceled-prompt tombstone.
// If a kernel challenge became active after the prompt was armed, the prompt is canceled before
// ordinary text remains eligible for kernel grading.
func (v *Panel) PanelInputDM(_ context.Context, update telego.Update) bool {
	message := update.Message
	if message == nil || message.From == nil || message.Chat.Type != "private" {
		return false
	}
	replyID := 0
	if message.ReplyToMessage != nil {
		replyID = message.ReplyToMessage.MessageID
	}
	state := v.panelState
	state.mu.Lock()
	state.pruneLocked(time.Now())
	_, tombstone := state.tombstones[promptKey{userID: message.From.ID, messageID: replyID}]
	session := state.byUser[message.From.ID]
	state.mu.Unlock()
	if replyID != 0 && tombstone {
		return true
	}
	if session == nil {
		return false
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if v.sessionByUser(message.From.ID) != session {
		return false
	}
	pending := session.pending
	if pending == nil || pending.promptMessageID == 0 {
		return false
	}
	if v.kernelPending(message.From.ID) {
		v.rememberCanceledPrompt(session, pending)
		session.pending = nil
		return pending.promptMessageID == replyID
	}
	return replyID != 0 && pending.promptMessageID == replyID
}

// PanelChatSharedDM matches only the request ID of the active Telegram chat picker.
func (v *Panel) PanelChatSharedDM(_ context.Context, update telego.Update) bool {
	message := update.Message
	if message == nil || message.From == nil || message.Chat.Type != "private" || message.ChatShared == nil {
		return false
	}
	session := v.sessionByUser(message.From.ID)
	if session == nil {
		return false
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if v.sessionByUser(message.From.ID) != session {
		return false
	}
	pending := session.pending
	if pending == nil || pending.requestID == 0 {
		return false
	}
	shared, ok := sharedRequestID(message.ChatShared.RequestID)
	if !ok {
		return false
	}
	return pending.requestID == shared || pending.requestAltID == shared
}

// sharedRequestID narrows a Telegram-supplied chat-picker identifier. The Bot API defines it as a
// signed 32-bit value, so anything outside that range did not come from a prompt this bot sent.
func sharedRequestID(value int) (int32, bool) {
	if value < math.MinInt32 || value > math.MaxInt32 {
		return 0, false
	}
	return int32(value), true
}
