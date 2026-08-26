package panel

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// SettingsCallbackPrefix identifies version-one settings-panel callbacks.
const SettingsCallbackPrefix = "p1:"

const telegramCallbackDataLimit = 64

type callbackData struct {
	token  string
	screen string
	group  int64
	field  string
	value  string
}

var callbackTransitions = map[string]map[string]func(string) bool{
	"gl": {"go": screenValue("gh"), "pg": pageValue, "rf": emptyValue, "cl": emptyValue},
	"gh": {"go": screenValue("gl", "rt", "ls", "md", "vp", "ct"), "rf": emptyValue, "cl": emptyValue},
	"rt": {"en": emptyValue, "df": enumValue("g", "d", "b"), "vm": enumValue("k", "q", "m"), "ns": emptyValue, "bd": emptyValue,
		"ld": emptyValue, "lt": emptyValue, "lg": enumValue("z", "h", "e"), "go": screenValue("gh")},
	"ls": {"cw": emptyValue, "tg": emptyValue, "kc": emptyValue, "go": screenValue("gh")},
	"li": {"ca": enumValue("cw", "tg", "kc"), "cw": hexValue, "tg": hexValue, "kc": hexValue, "pg": pageValue, "go": screenValue("ls")},
	"vp": {"to": emptyValue, "mf": emptyValue, "rc": emptyValue, "vi": emptyValue, "pr": emptyValue, "go": screenValue("gh")},
	"md": {"as": emptyValue, "ms": emptyValue, "wl": emptyValue, "rx": emptyValue, "al": emptyValue,
		"ac": emptyValue, "go": screenValue("gh")},
	"ct": {"go": screenValue("gh", "qb", "fb", "ch")},
	"qb": {"qq": hexValue, "ca": emptyValue, "pg": pageValue, "go": screenValue("ct")},
	"qd": {"qq": emptyValue, "qo": emptyValue, "ok": hexValue, "dl": hexValue, "sv": emptyValue,
		"rm": emptyValue, "cn": emptyValue},
	"fb": {"fq": hexValue, "ca": emptyValue, "pg": pageValue, "rb": emptyValue, "go": screenValue("ct")},
	"fd": {"fq": emptyValue, "fa": emptyValue, "dl": hexValue, "sv": emptyValue, "rm": emptyValue, "cn": emptyValue},
	"ch": {"ci": emptyValue, "iu": emptyValue, "dl": emptyValue, "ds": emptyValue, "go": screenValue("ct")},
	"cf": {"ok": emptyValue, "cn": emptyValue},
	"in": {"cn": emptyValue},
}

func newPanelToken() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate panel token: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func encodeCallback(data callbackData) (string, error) {
	if !validToken(data.token) {
		return "", errors.New("invalid panel token")
	}
	fields, ok := callbackTransitions[data.screen]
	if !ok {
		return "", errors.New("unknown panel screen")
	}
	validate, ok := fields[data.field]
	if !ok || !validate(data.value) {
		return "", errors.New("invalid panel transition")
	}
	encoded := strings.Join([]string{"p1", data.token, data.screen, encodeSigned(data.group), data.field, data.value}, ":")
	if len(encoded) > telegramCallbackDataLimit {
		return "", fmt.Errorf("callback data is %d bytes", len(encoded))
	}
	return encoded, nil
}

func parseCallback(encoded string) (callbackData, error) {
	parts := strings.Split(encoded, ":")
	if len(parts) != 6 || parts[0] != "p1" {
		return callbackData{}, errors.New("invalid callback grammar")
	}
	if !validToken(parts[1]) {
		return callbackData{}, errors.New("invalid callback token")
	}
	group, err := decodeSigned(parts[3])
	if err != nil {
		return callbackData{}, fmt.Errorf("invalid callback group: %w", err)
	}
	data := callbackData{token: parts[1], screen: parts[2], group: group, field: parts[4], value: parts[5]}
	fields, ok := callbackTransitions[data.screen]
	if !ok {
		return callbackData{}, errors.New("unknown callback screen")
	}
	validate, ok := fields[data.field]
	if !ok || !validate(data.value) {
		return callbackData{}, errors.New("invalid callback transition")
	}
	if len(encoded) > telegramCallbackDataLimit {
		return callbackData{}, errors.New("callback data exceeds Telegram limit")
	}
	return data, nil
}

func validToken(value string) bool {
	if len(value) != 16 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func emptyValue(value string) bool { return value == "_" }

func hexValue(value string) bool {
	if len(value) < 1 || len(value) > 16 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	_, err := strconv.ParseUint(value, 16, 64)
	return err == nil
}

func enumValue(values ...string) func(string) bool {
	return func(value string) bool {
		for _, candidate := range values {
			if value == candidate {
				return true
			}
		}
		return false
	}
}

func pageValue(value string) bool {
	if !hexValue(value) {
		return false
	}
	page, _ := strconv.ParseUint(value, 16, 64)
	return page <= 1<<31-1
}

func screenValue(values ...string) func(string) bool { return enumValue(values...) }

func encodeSigned(value int64) string {
	// Zig-zag encoding reinterprets the two's-complement bits deliberately; both conversions are
	// bit-exact by definition and cannot lose information.
	zigzag := (uint64(value) << 1) ^ uint64(value>>63) // #nosec G115 -- intentional bit-exact reinterpretation
	return strconv.FormatUint(zigzag, 16)
}

func decodeSigned(value string) (int64, error) {
	if !hexValue(value) {
		return 0, errors.New("invalid hexadecimal integer")
	}
	zigzag, err := strconv.ParseUint(value, 16, 64)
	if err != nil {
		return 0, err
	}
	return int64(zigzag>>1) ^ -int64(zigzag&1), nil
}

// panelIndexMax bounds every list index and page number carried in a callback payload. No settings
// list can approach it; the bound exists so a forged payload cannot address a slice out of range or
// wrap when converted on a 32-bit platform.
const panelIndexMax = math.MaxInt32

// encodeIndex encodes a non-negative list index or page number for a callback payload.
func encodeIndex(value int) string {
	if value < 0 {
		value = 0
	}
	return encodeUnsigned(uint64(value))
}

// decodeIndex decodes a list index or page number, rejecting anything a slice could not address.
func decodeIndex(value string) (int, error) {
	raw, err := decodeUnsigned(value)
	if err != nil {
		return 0, err
	}
	if raw > panelIndexMax {
		return 0, errors.New("index out of range")
	}
	return int(raw), nil
}

func encodeUnsigned(value uint64) string { return strconv.FormatUint(value, 16) }

func decodeUnsigned(value string) (uint64, error) {
	if !hexValue(value) {
		return 0, errors.New("invalid hexadecimal integer")
	}
	return strconv.ParseUint(value, 16, 64)
}
