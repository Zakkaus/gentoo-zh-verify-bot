package verify

import "testing"

// The same command's output must be judged the same way regardless of incidental detail such as
// how many version numbers the compiler banner happens to contain.
func TestKernelAnswerAcceptsRealCommandOutput(t *testing.T) {
	accept := []string{
		// uname -r, the answer the prompt asks for.
		"7.2.0-gentoo-cjk-zakk",
		"6.12.3-gentoo",
		// uname -a, pasted whole.
		"Linux gentoo 7.2.0-gentoo-cjk-zakk #1 SMP PREEMPT_DYNAMIC Sat Aug 22 14:56:02 AEST 2026 x86_64 AMD Ryzen 9 9950X3D 16-Core Processor AuthenticAMD GNU/Linux",
		"uname -a: Linux box 6.1.0-18-amd64 #1 SMP PREEMPT_DYNAMIC Debian 6.1.76-1 (2024-02-01) x86_64 GNU/Linux",
		// /proc/version, with a one-version and a three-version compiler banner.
		"Linux version 6.12.3-gentoo (root@box) (gcc 14) #1 SMP",
		"Linux version 7.2.0-gentoo-cjk-zakk (root@gentoo) (gcc (Gentoo 14.2.1 p7) 14.2.1, GNU ld 2.43) #1 SMP PREEMPT_DYNAMIC Sat Aug 22 14:56:02 AEST 2026",
		// The shell transcript people paste without thinking.
		"$ uname -r\n7.2.0-gentoo-cjk-zakk",
		"uname -r: 6.12.3-gentoo",
		// A release wrapped in the punctuation a chat client adds.
		"`7.2.0-gentoo-cjk-zakk`",
		"7.2.0-gentoo-cjk-zakk。",
	}
	for _, s := range accept {
		if !kernelAnswerOK(s) {
			t.Errorf("real kernel output must be accepted: %q", s)
		}
	}

	reject := []string{
		"",
		"Linux",
		"我用的是 Gentoo",
		// Words dressed as kernel output: no build field, so not something a command printed.
		"Linux gentoo 5.2 assistant model=gpt GNU/Linux",
		"Linux host 5.2 assistant GNU/Linux",
		"Linux host 5.2 my model is gpt",
		// A Windows build number is not a kernel release.
		"10.0.19045.5011",
	}
	for _, s := range reject {
		if kernelAnswerOK(s) {
			t.Errorf("this is not kernel output and must be refused: %q", s)
		}
	}
}

// A compiler banner carrying one version number and one carrying three describe the same kernel.
func TestKernelJudgementDoesNotDependOnBannerShape(t *testing.T) {
	one := "Linux version 6.12.3-gentoo (root@box) (gcc 14) #1 SMP"
	three := "Linux version 6.12.3-gentoo (root@box) (gcc (Gentoo 14.2.1 p7) 14.2.1) #1 SMP"
	if kernelAnswerOK(one) != kernelAnswerOK(three) {
		t.Errorf("same command, different banner, different verdict: one=%v three=%v",
			kernelAnswerOK(one), kernelAnswerOK(three))
	}
}
