package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// TestProductionCodeContainsNoChineseStringLiterals keeps the locale catalogues
// authoritative for production Chinese text. Test files are deliberately exempt
// because they need Chinese fixtures and expected values; comments are exempt
// because they are not user-visible program strings. The escaped Arch Wiki
// language labels are parser inputs and are the only production exemption.
func TestProductionCodeContainsNoChineseStringLiterals(t *testing.T) {
	sourceFile := invariantTestSource(t)
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
	i18nDirectory := filepath.Join(repositoryRoot, "internal", "i18n")

	err := filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path == i18nDirectory {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative := relativePath(repositoryRoot, path)

		files := token.NewFileSet()
		parsed, err := parser.ParseFile(files, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Errorf("parse %s: %v", relative, err)
			return nil
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Errorf("unquote %s: %v", files.Position(literal.Pos()), err)
				return true
			}
			if !strings.ContainsFunc(value, isHan) || isAllowedNonUserVisibleHanLiteral(relative, literal.Value) {
				return true
			}

			position := files.Position(literal.Pos())
			for offset, character := range literal.Value {
				if isHan(character) {
					position = files.Position(literal.Pos() + token.Pos(offset))
					break
				}
			}
			t.Errorf("%s:%d: Chinese string literal %q", relative, position.Line, value)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scan production Go files: %v", err)
	}
}

// TestLocaleFilesLoad sends every required locale file through the production
// loader, detecting missing subsystem files, malformed JSON, unknown keys, and
// invalid value shapes before deployment.
func TestLocaleFilesLoad(t *testing.T) {
	packageDirectory := filepath.Dir(invariantTestSource(t))
	catalogType := reflect.TypeFor[Catalog]()
	for _, definition := range localeDefinitions {
		for fieldIndex := range catalogType.NumField() {
			subsystem := fieldKey(catalogType.Field(fieldIndex).Name)
			path := localeFilePath(definition.tag, subsystem)
			t.Run(definition.tag+"/"+subsystem, func(t *testing.T) {
				data, err := os.ReadFile(filepath.Join(packageDirectory, path))
				if err != nil {
					t.Fatalf("%s: %v", path, err)
				}

				var catalog Catalog
				destination := reflect.ValueOf(&catalog).Elem().Field(fieldIndex)
				if err := loadLocaleValue(destination, data, definition.language, subsystem); err != nil {
					t.Errorf("%s: %v", path, err)
				}
			})
		}
	}
}

func invariantTestSource(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate invariant test source")
	}
	return sourceFile
}

func relativePath(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(relative)
}

func isAllowedNonUserVisibleHanLiteral(path, literal string) bool {
	if path != "internal/lookup/content.go" {
		return false
	}
	switch literal {
	case `"\u7b80\u4f53\u4e2d\u6587"`,
		`"\u7e41\u9ad4\u4e2d\u6587"`,
		`"\u6b63\u9ad4\u4e2d\u6587"`:
		return true
	default:
		return false
	}
}

func isHan(character rune) bool {
	return unicode.Is(unicode.Han, character)
}
