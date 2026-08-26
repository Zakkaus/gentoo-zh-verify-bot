package i18n

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Review agents and debugging sessions leave probe files behind. They compile, so nothing else
// catches them, and a careless `git add -A` commits them. Name them out of the tree instead.
func TestRepositoryCarriesNoScratchFiles(t *testing.T) {
	root := filepath.Join("..", "..")
	prefixes := []string{"zz", "zzz", "ztmp", "tmp", "scratch", "probe", "repro", "debug"}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		name := strings.ToLower(entry.Name())
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		for _, prefix := range prefixes {
			if strings.HasPrefix(name, prefix) {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s looks like a leftover scratch file; delete it or give it a name that says what it verifies", rel)
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
