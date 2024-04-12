package server_info

import (
	"os"
	"path/filepath"

	config "github.com/DigitalArsenal/space-data-network/serverconfig"
)

// SaveEPMToFile saves the EPM data to a file in the RootFolder.
func SaveEPMToFile(data []byte) error {
	epmFilePath := filepath.Join(config.Conf.Folders.RootFolder, "server.epm")
	return os.WriteFile(epmFilePath, data, 0644)
}

// LoadEPMFromFile loads the EPM data from a file in the RootFolder.
func LoadEPMFromFile() ([]byte, error) {
	epmFilePath := filepath.Join(config.Conf.Folders.RootFolder, "server.epm")
	return os.ReadFile(epmFilePath)
}

// SavePNMToFile saves the PNM data to a file in the RootFolder.
func SavePNMToFile(data []byte) error {
	pnmFilePath := filepath.Join(config.Conf.Folders.RootFolder, "server.pnm")
	return os.WriteFile(pnmFilePath, data, 0644)
}

// LoadPNMFromFile loads the PNM data from a file in the RootFolder.
func LoadPNMFromFile() ([]byte, error) {
	pnmFilePath := filepath.Join(config.Conf.Folders.RootFolder, "server.pnm")
	return os.ReadFile(pnmFilePath)
}
