package i18n

import (
	"strings"
	"testing"

	"github.com/Zakkaus/gentoo-zh-verify-bot/internal/edition"
)

// The verification prompt shows the shape of a `uname -r` release. Naming Gentoo in that
// example only makes sense to a group that runs Gentoo.
func TestKernelPromptExampleMatchesTheEdition(t *testing.T) {
	for _, l := range []Lang{LangEN, LangZH, LangZHHant} {
		for name, prompt := range map[string]string{
			"kernel_prompt":      Messages.Verification.Challenge.KernelPrompt.Render(l, "Q", 3),
			"kernel_prompt_held": Messages.Verification.Challenge.KernelPromptHeld.Render(l, "Q", 3),
		} {
			want := "X.Y.Z" + edition.KernelExampleSuffix
			if !strings.Contains(prompt, want) {
				t.Errorf("%v %s: format example is not %q: %q", l, name, want, prompt)
			}
			if !edition.IsGentoo && strings.Contains(prompt, "gentoo") {
				t.Errorf("%v %s: the generic build's prompt names Gentoo: %q", l, name, prompt)
			}
			if strings.Contains(prompt, "{ks}") {
				t.Errorf("%v %s: an edition token was left unsubstituted", l, name)
			}
		}
	}
}
