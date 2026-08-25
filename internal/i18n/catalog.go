package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Lang identifies one supported catalogue locale.
type Lang uint8

const (
	// LangZH selects Simplified Chinese.
	LangZH Lang = iota
	// LangZHHant selects Traditional Chinese.
	LangZHHant
	// LangEN selects English.
	LangEN
	langCount
)

type localeDefinition struct {
	language Lang
	tag      string
}

var localeDefinitions = [langCount]localeDefinition{
	{language: LangZH, tag: "zh"},
	{language: LangZHHant, tag: "zh-Hant"},
	{language: LangEN, tag: "en"},
}

// Languages returns every supported catalogue locale.
func Languages() [langCount]Lang {
	var languages [langCount]Lang
	for i, definition := range localeDefinitions {
		languages[i] = definition.language
	}
	return languages
}

// String returns the canonical persisted language tag.
func (l Lang) String() string {
	if l >= langCount {
		l = LangZH
	}
	return localeDefinitions[l].tag
}

// FromTelegram resolves a Telegram language tag to an applicant locale.
func FromTelegram(code string) Lang {
	code = strings.ToLower(strings.TrimSpace(code))
	if !strings.HasPrefix(code, "zh") && !strings.HasPrefix(code, "yue") {
		return LangEN
	}
	for _, tag := range [...]string{"hant", "tw", "hk", "mo", "yue"} {
		if strings.Contains(code, tag) {
			return LangZHHant
		}
	}
	return LangZH
}

// FromRequester resolves a supported requester language or returns fallback.
func FromRequester(code string, fallback Lang) Lang {
	code = strings.ToLower(strings.TrimSpace(code))
	switch {
	case code == "en" || strings.HasPrefix(code, "en-") || strings.HasPrefix(code, "en_"):
		return LangEN
	case strings.HasPrefix(code, "zh") || strings.HasPrefix(code, "yue"):
		return FromTelegram(code)
	default:
		return fallback
	}
}

// FromStored resolves canonical and legacy pending language tags.
func FromStored(tag string) Lang {
	switch strings.ToLower(strings.TrimSpace(tag)) {
	case "en":
		return LangEN
	case "zh-hant":
		return LangZHHant
	default:
		return LangZH
	}
}

type localized [langCount]string

func (s localized) value(l Lang) string {
	if l >= langCount {
		l = LangZH
	}
	return s[l]
}

// Text is a localized value that is returned without formatting.
type Text struct{ localized }

// Format is a localized value with an indexed formatting contract.
type Format struct{ localized }

// For returns the text for l.
func (t Text) For(l Lang) string { return t.value(l) }

// Render formats the value for l with its indexed arguments.
func (f Format) Render(l Lang, args ...any) string {
	return fmt.Sprintf(f.value(l), args...)
}

// Catalog contains every localized subsystem.
type Catalog struct {
	// Verification contains applicant join-verification text.
	Verification VerificationCatalog
	// Moderate contains moderation text.
	Moderate ModerateCatalog
	// LookupPackages contains package-lookup text.
	LookupPackages LookupPackagesCatalog
	// LookupDistros contains distribution-lookup text.
	LookupDistros LookupDistrosCatalog
	// LookupContent contains content-lookup text.
	LookupContent LookupContentCatalog
	// Panel contains control-panel text.
	Panel PanelCatalog
	// Bot contains bot lifecycle and command text.
	Bot BotCatalog
	// Feed contains feed publication text.
	Feed FeedCatalog
}

//go:embed locales/*/*.json
var localeFiles embed.FS

var (
	catalogTextType       = reflect.TypeFor[Text]()
	catalogFormatType     = reflect.TypeFor[Format]()
	catalogStringListType = reflect.TypeFor[StringList]()
)

// Messages is the complete localized catalogue.
var Messages = mustLoadCatalog()

func mustLoadCatalog() Catalog {
	var catalog Catalog
	destination := reflect.ValueOf(&catalog).Elem()
	catalogType := destination.Type()
	for index, definition := range localeDefinitions {
		if definition.language != Lang(index) {
			panic("i18n: locale definitions are not in Lang order")
		}
		for fieldIndex := range destination.NumField() {
			subsystem := fieldKey(catalogType.Field(fieldIndex).Name)
			path := localeFilePath(definition.tag, subsystem)
			data, err := localeFiles.ReadFile(path)
			if err != nil {
				panic(fmt.Errorf("i18n: read %s: %w", path, err))
			}
			if err := loadLocaleValue(destination.Field(fieldIndex), data, definition.language, subsystem); err != nil {
				panic(fmt.Errorf("i18n: load %s: %w", path, err))
			}
		}
	}
	return catalog
}

func localeFilePath(tag, subsystem string) string {
	return "locales/" + tag + "/" + subsystem + ".json"
}

func loadLocaleValue(destination reflect.Value, raw json.RawMessage, language Lang, path string) error {
	switch destination.Type() {
	case catalogTextType:
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("%s: expected string: %w", path, err)
		}
		destination.Addr().Interface().(*Text).localized[language] = value
		return nil
	case catalogFormatType:
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("%s: expected string: %w", path, err)
		}
		destination.Addr().Interface().(*Format).localized[language] = value
		return nil
	case catalogStringListType:
		var values []string
		if err := json.Unmarshal(raw, &values); err != nil {
			return fmt.Errorf("%s: expected string list: %w", path, err)
		}
		destination.Addr().Interface().(*StringList).localizedStrings[language] = values
		return nil
	}

	switch destination.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return fmt.Errorf("%s: expected object: %w", path, err)
		}
		if object == nil {
			return fmt.Errorf("%s: expected object", path)
		}
		typ := destination.Type()
		for i := range destination.NumField() {
			key := fieldKey(typ.Field(i).Name)
			child, ok := object[key]
			if !ok {
				continue
			}
			delete(object, key)
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			if err := loadLocaleValue(destination.Field(i), child, language, childPath); err != nil {
				return err
			}
		}
		if len(object) > 0 {
			keys := make([]string, 0, len(object))
			for key := range object {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			return fmt.Errorf("%s: unknown keys: %s", path, strings.Join(keys, ", "))
		}
		return nil
	case reflect.Array:
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return fmt.Errorf("%s: expected array: %w", path, err)
		}
		if len(values) > destination.Len() {
			return fmt.Errorf("%s: got %d entries, maximum %d", path, len(values), destination.Len())
		}
		for i, value := range values {
			if err := loadLocaleValue(destination.Index(i), value, language, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("%s: unsupported catalogue type %s", path, destination.Type())
	}
}

func fieldKey(name string) string {
	var key strings.Builder
	key.Grow(len(name) + 4)
	for i := range len(name) {
		current := name[i]
		if current >= 'A' && current <= 'Z' {
			if i > 0 && (name[i-1] < 'A' || name[i-1] > 'Z' ||
				(i+1 < len(name) && name[i+1] >= 'a' && name[i+1] <= 'z')) {
				key.WriteByte('_')
			}
			current += 'a' - 'A'
		}
		key.WriteByte(current)
	}
	return key.String()
}
