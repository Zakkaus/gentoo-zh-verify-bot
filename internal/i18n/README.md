# Locale catalogues

Each locale has one JSON file per subsystem. The file name is the subsystem name, and every locale directory must contain all eight files:

- `verification.json`
- `moderate.json`
- `lookup_packages.json`
- `lookup_distros.json`
- `lookup_content.json`
- `panel.json`
- `bot.json`
- `feed.json`

An empty subsystem is represented by `{}`. Do not remove its file.

The directory name is the canonical locale tag: `zh`, `zh-Hant`, or `en`. JSON keys are identical in every locale; only values are translated. A key such as `challenge.kernel_prompt` in `locales/en/verification.json` is available in Go as `Messages.Verification.Challenge.KernelPrompt`.

## Add a key

1. Choose one subsystem. Add the same JSON key to that subsystem's file under `locales/zh/`, `locales/zh-Hant/`, and `locales/en/`.
2. In the matching Go file, add one exported field to the corresponding group. Use `Text` for a literal string or `Format` for a string containing indexed placeholders such as `%[1]s`; add a one-line English doc comment.
3. Run `go test ./internal/i18n`. Fix every reported locale file and key path before submitting the change.

Keep the object nesting and key spelling identical across locales. JSON does not support comments. Preserve HTML, commands, URLs, line breaks, and indexed placeholders exactly unless the translated sentence requires a different word order. Reordering indexed placeholders is allowed; changing their index or formatting verb is not.

## Add a locale

Adding files and registering the catalogue is necessary but does not make a locale selectable.
Complete every step:

1. Copy an existing locale directory to a directory named with the new canonical locale tag.
2. Translate every string value in all eight files. Keep every JSON key, array shape, HTML
   fragment, command, URL, and indexed placeholder.
3. In `internal/i18n/catalog.go`, add a documented `Lang` constant before `langCount` and the
   matching `localeDefinitions` entry in the same order. Update `FromTelegram`, `FromRequester`,
   and `FromStored` so Telegram, requester, and persisted tags resolve to the new locale instead
   of a fallback.
4. In `internal/config/config.go`, extend `ValidLanguage`. This validator gates the top-level
   `lang`, `groups[].lang`, feed `lang`, startup validation, and persisted panel values.
5. In `internal/bot/commands.go`, add the locale to `SetupCommands` and decide which Telegram
   `language_code` and per-chat command-menu scopes it needs. The current list registers only the
   Simplified-Chinese fallback, `zh`, and `en`, with a separate Traditional-Chinese per-chat path.
6. In `internal/panel/codec.go`, add a compact value to the `lg` callback grammar. In
   `internal/panel/settings_panel.go`, map that value to the canonical tag and add the localized
   language button. Keep the encoded callback within Telegram's 64-byte limit.
7. Add an end-to-end selection test outside `internal/i18n` that selects the locale through a
   public configuration or settings-panel path and asserts the rendered locale. Catalogue-only
   tests cannot prove that users can select it.
8. Run `go test ./internal/i18n`, then `go test -race -count=1 ./...`. The locale is complete only
   when registration, every selection path, and the end-to-end test pass.

## Tests

`TestCatalogComplete` checks that every typed key has a non-empty value in every registered locale, plain `Text` values contain no formatting directive, string lists contain no empty entry, and answer-hidden verification questions do not reveal their answers. Failures name the subsystem, locale file, and JSON key path.

`TestFormatPlaceholdersMatchLocales` checks every `Format` value against `zh`. All locales must contain the same indexed placeholder and formatting-verb set. Failures name the subsystem, locale file, and JSON key path.

## Shared glossary

Use one translation for the same concept throughout a locale, including across subsystem files. Preserve commands, package identifiers, API fields, URLs, and upstream project names. Write Traditional Chinese (`zh-Hant`) natively for a general Traditional Chinese audience; never derive it by character conversion from Simplified Chinese. Do not introduce region-specific Cantonese, Taiwan-only, or Hong Kong-only wording.
