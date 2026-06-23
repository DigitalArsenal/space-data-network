package bundle

import (
	"os"
	"path/filepath"
	"runtime"
)

type Layout struct {
	Root         string
	BinDir       string
	KuboBinary   string
	SDNUIPath    string
	WebUIPath    string
	UpdaterWASM  string
	HDWalletWASM string
	ManifestPath string
}

func ResolveCurrent() Layout {
	exe, err := os.Executable()
	if err != nil {
		return Layout{}
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		resolved = exe
	}
	return ResolveFromExecutable(resolved)
}

func ResolveFromExecutable(executablePath string) Layout {
	if executablePath == "" {
		return Layout{}
	}
	binDir := filepath.Dir(executablePath)
	root := filepath.Dir(binDir)
	if filepath.Base(binDir) != "bin" {
		// The Linux VM bundle ships a launcher script at bin/<exe> that
		// execs the real binary from runtime/sdn/<exe> (so the bundled
		// WasmEdge libraries resolve); accept that layout as well.
		if filepath.Base(binDir) == "sdn" && filepath.Base(root) == "runtime" {
			root = filepath.Dir(root)
			binDir = filepath.Join(root, "bin")
		} else {
			return Layout{}
		}
	}
	manifestPath := filepath.Join(root, "manifest.json")
	if _, err := os.Stat(manifestPath); err != nil {
		return Layout{}
	}
	kuboName := "ipfs"
	if runtime.GOOS == "windows" {
		kuboName = "ipfs.exe"
	}
	return Layout{
		Root:         root,
		BinDir:       binDir,
		KuboBinary:   filepath.Join(root, "runtime", "kubo", kuboName),
		SDNUIPath:    filepath.Join(root, "runtime", "ui", "sdn"),
		WebUIPath:    filepath.Join(root, "runtime", "ui", "webui"),
		UpdaterWASM:  filepath.Join(root, "runtime", "modules", "org.spacedatanetwork.updater.wasm"),
		HDWalletWASM: filepath.Join(root, "runtime", "modules", "hd-wallet-wasi.wasm"),
		ManifestPath: manifestPath,
	}
}
