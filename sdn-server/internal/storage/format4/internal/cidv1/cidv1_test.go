package cidv1

import "testing"

func TestGolden(t *testing.T) {
	const want = "bafkreibm6jg3ux5qumhcn2b3flc3tyu6dmlb4xa7u5bf44yegnrjhc4yeq" // contract §3.7
	if got := Of([]byte("hello")); got != want {
		t.Fatalf("Of(hello) = %s", got)
	}
	b, err := Parse(want)
	if err != nil || Text(b) != want || [4]byte(b[:4]) != Prefix {
		t.Fatalf("Parse: %x %v", b, err)
	}
	for _, bad := range []string{"", "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi",
		"BAFKREIBM6JG3UX5QUMHCN2B3FLC3TYU6DMLB4XA7U5BF44YEGNRJHC4YEQ", "zafkreibm6jg3ux5qumhcn2b3flc3tyu6dmlb4xa7u5bf44yegnrjhc4yeq",
		"bafkqaaa"} {
		if _, err := Parse(bad); err == nil {
			t.Fatalf("Parse(%q) accepted", bad)
		}
	}
}
