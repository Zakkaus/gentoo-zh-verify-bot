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

A translator can prepare a locale without reading Go:

1. Copy an existing locale directory to a directory named with the new canonical locale tag.
2. Translate every string value in all eight files. Keep every JSON key, array shape, HTML fragment, command, URL, and indexed placeholder.
3. Ask a Go maintainer to add a documented `Lang` constant and a matching `localeDefinitions` entry in `catalog.go`. Their order must match, and the new entry must appear before `langCount`.
4. Run `go test ./internal/i18n` after registration. The locale is not loaded or selectable until the Go registration is complete.

## Tests

`TestCatalogComplete` checks that every typed key has a non-empty value in every registered locale, plain `Text` values contain no formatting directive, string lists contain no empty entry, and answer-hidden verification questions do not reveal their answers. Failures name the subsystem, locale file, and JSON key path.

`TestFormatPlaceholdersMatchLocales` checks every `Format` value against `zh`. All locales must contain the same indexed placeholder and formatting-verb set. Failures name the subsystem, locale file, and JSON key path.

## Shared glossary

Use one translation for the same concept throughout a locale, including across subsystem files. Preserve commands, package identifiers, API fields, URLs, and upstream project names. Write Traditional Chinese (`zh-Hant`) natively for a general Traditional Chinese audience; never derive it by character conversion from Simplified Chinese. Do not introduce region-specific Cantonese, Taiwan-only, or Hong Kong-only wording.
