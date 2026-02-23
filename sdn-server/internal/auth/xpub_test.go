package auth

import "testing"

func TestIsValidXPub(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "standard BIP-32 xpub",
			input: "xpub661MyMwAqRbcFtXgS5sYJABqqG9YLmC4Q1Rdap9gSE8NqtwybGhePY2gZ29ESFjqJoCu1Rupje8YtGqsefD265TMg7usUDFdp6W1EGMcet8",
			want:  true,
		},
		{
			name:  "standard BIP-32 xpub account level",
			input: "xpub661MyMwAqRbcFgApR6DVLSUQgdgydW5hLekFQQcLkRHtmiGJqR8xVPcBuevmizd7EFC3rUDyqSoQGSUzT3DaEBeGN1UMB2qCbLhosCj4tSf",
			want:  true,
		},
		{
			name:  "with leading/trailing whitespace",
			input: "  xpub661MyMwAqRbcFtXgS5sYJABqqG9YLmC4Q1Rdap9gSE8NqtwybGhePY2gZ29ESFjqJoCu1Rupje8YtGqsefD265TMg7usUDFdp6W1EGMcet8  ",
			want:  true,
		},
		{
			name:  "empty string",
			input: "",
			want:  false,
		},
		{
			name:  "too short",
			input: "xpub123",
			want:  false,
		},
		{
			name:  "wrong prefix",
			input: "ypub661MyMwAqRbcFtXgS5sYJABqqG9YLmC4Q1Rdap9gSE8NqtwybGhePY2gZ29ESFjqJoCu1Rupje8YtGqsefD265TMg7usUDFdp6W1EGMcet8",
			want:  false,
		},
		{
			name:  "garbage",
			input: "notavalidxpub!!!",
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsValidXPub(tt.input); got != tt.want {
				t.Errorf("IsValidXPub(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
