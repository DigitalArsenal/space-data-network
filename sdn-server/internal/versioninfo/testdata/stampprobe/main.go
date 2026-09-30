// Command stampprobe links internal/versioninfo and nothing of the store-format
// guard, the smallest program that must still carry the stamp. The versioninfo
// tests build it with release link flags and scan the result.
package main

import (
	"fmt"

	"github.com/spacedatanetwork/sdn-server/internal/versioninfo"
)

func main() { fmt.Println(versioninfo.Version()) }
