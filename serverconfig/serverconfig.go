package serverconfig

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"

	"github.com/ipfs/kubo/plugin/loader"
	"golang.org/x/crypto/argon2"
)

var (
	pluginsLoaded sync.Once
)

//go:embed manifest.json
var versionFile embed.FS

var once sync.Once

type folderConfig struct {
	RootFolder     string
	OutgoingFolder string
}

type datastoreConfig struct {
	Directory string
	Password  string
}

type webserverConfig struct {
	Port int
}

type Info struct {
	Version   string   `json:"Version"`
	Standards []string `json:"Standards"`
}

type keyConfig struct {
	EntropyLengthBits int
}

type keys struct {
	EncryptionAccountDerivationPath string
	SigningAccountDerivationPath    string
}

type IpfsPeerPinConfig struct {
	PeerID  string   `json:"PeerID"`
	FileIDs []string `json:"FileIDs"`
}

type IpfsConfig struct {
	PeerPins  []IpfsPeerPinConfig `json:"PeerPins"`
	SdnEpmCid map[string]string   `json:"SdnEpmCid"` // Maps PeerID to their EPM CID, including the current node's
}

// AppConfig holds the entire application configuration with namespaces
type AppConfig struct {
	Datastore datastoreConfig
	Webserver webserverConfig
	KeyConfig keyConfig
	Keys      keys
	Info      Info
	Folders   folderConfig
	IPFS      IpfsConfig
}

// Conf is the exported variable that will hold all the configuration settings
var Conf AppConfig

// Init initializes the global configuration
func Init() {
	once.Do(func() {

		//TODO option
		os.Setenv("IPFS_LOGGING", "panic")

		pluginsLoaded.Do(func() {
			plugins, err := loader.NewPluginLoader(filepath.Join("", "plugins"))
			if err != nil {
				fmt.Printf("error loading plugins: %s\n", err)
				return
			}

			if err := plugins.Initialize(); err != nil {
				fmt.Printf("error initializing plugins: %s\n", err)
				return
			}

			if err := plugins.Inject(); err != nil {
				fmt.Printf("error injecting plugins: %s\n", err)
				return
			}
		})

		err := Conf.LoadConfigFromFile()
		if err != nil {
			log.Printf("Failed to load configuration from file, proceeding with defaults.")
			// Proceed with default and command-line configurations
		}

		// Parse the version from manifest.json
		var rawManifest map[string]interface{}
		data, err := versionFile.ReadFile("manifest.json")
		if err != nil {
			log.Fatalf("Failed to read version file: %v", err)
		}
		if err := json.Unmarshal(data, &rawManifest); err != nil {
			log.Fatalf("Failed to parse manifest file: %v", err)
		}

		versionInfo := Info{
			Version: rawManifest["version"].(string),
		}

		if standards, ok := rawManifest["STANDARDS"].(map[string]interface{}); ok {
			for standard := range standards {
				versionInfo.Standards = append(versionInfo.Standards, standard)
			}
		}

		Conf.Info = versionInfo

		// Webserver settings
		var webserverPortStr string
		flag.StringVar(&webserverPortStr, "webserver.port", "8080", "Port for the webserver to listen on")

		// Datastore settings
		flag.StringVar(&Conf.Datastore.Directory, "datastore.directory", "", "Directory for the datastore")
		flag.StringVar(&Conf.Datastore.Password, "datastore.password", "", "Password for the datastore encryption")

		// Parse command-line flags
		flag.Parse()

		if dir, exists := os.LookupEnv("SPACE_DATA_NETWORK_DATASTORE_DIRECTORY"); exists {
			fmt.Println("SPACE_DATA_NETWORK_DATASTORE_DIRECTORY", dir)
			// Check if the directory exists
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				// Directory does not exist, clear the environment variable
				err := os.Unsetenv("SPACE_DATA_NETWORK_DATASTORE_DIRECTORY")
				if err != nil {
					log.Printf("Error unsetting environment variable: %v", err)
					return // Early return on error
				}

				// Inform the user that the provided directory does not exist and the default will be used
				log.Printf("The provided directory '%s' does not exist. Defaulting to home directory.\n", dir)

				// Set the default directory to the user's home directory
				Conf.Datastore.Directory = setDefaultDatastoreDirectory()
			} else {
				// If the directory exists, use it
				Conf.Datastore.Directory = dir
			}
		} else {
			// No environment variable provided; use default
			Conf.Datastore.Directory = setDefaultDatastoreDirectory()
		}

		// Override webserver port with environment variable if exists
		if portStr, exists := os.LookupEnv("SPACE_DATA_NETWORK_WEBSERVER_PORT"); exists {
			webserverPortStr = portStr
		}
		if port, err := strconv.Atoi(webserverPortStr); err == nil {
			Conf.Webserver.Port = port
		} else {
			Conf.Webserver.Port = 8080
		}

		if password, exists := os.LookupEnv("SPACE_DATA_NETWORK_DATASTORE_PASSWORD"); exists {
			Conf.Datastore.Password = password
		} else {
			hostname, _ := os.Hostname()
			homeDir, _ := os.UserHomeDir()
			cpu := runtime.GOARCH
			osType := runtime.GOOS
			input := fmt.Sprintf("%s:%s:%s", hostname, cpu, osType)
			salt := []byte(homeDir)
			Conf.Datastore.Password = string(argon2.IDKey([]byte(input), salt, 1, 64*1024, 4, 32))

		}

		Conf.Keys.SigningAccountDerivationPath = "m/44'/60'/0'/0/0'"
		Conf.Keys.EncryptionAccountDerivationPath = "m/44'/60'/0'/1/0'"

		Conf.KeyConfig.EntropyLengthBits = 256

		// SETUP IPFS ROOT

		rootDir := filepath.Join(Conf.Datastore.Directory, "ipns_home")
		Conf.Folders.RootFolder = rootDir
		if _, err := os.Stat(rootDir); os.IsNotExist(err) {
			if err := os.MkdirAll(rootDir, 0755); err != nil {
				log.Fatalf("Failed to create 'root' directory: %v", err)
			}
		}

		outgoingDirectory := filepath.Join(Conf.Datastore.Directory, "outgoing")
		Conf.Folders.OutgoingFolder = outgoingDirectory
		if _, err := os.Stat(outgoingDirectory); os.IsNotExist(err) {
			if err := os.MkdirAll(outgoingDirectory, 0755); err != nil {
				log.Fatalf("Failed to create 'root' directory: %v", err)
			}
		}

		err = Conf.SaveConfigToFile()
		if err != nil {
			log.Fatalf("Failed to save configuration to file: %v", err)
		}
	})
}

// UpdateEpmCidForPeer updates or adds the EPM CID for a given PeerID, including the current node's
func (c *AppConfig) UpdateEpmCidForPeer(peerID string, cid string) {
	if c.IPFS.SdnEpmCid == nil {
		c.IPFS.SdnEpmCid = make(map[string]string)
	}
	c.IPFS.SdnEpmCid[peerID] = cid
	err := c.SaveConfigToFile()
	if err != nil {
		log.Fatalf("Failed to save configuration after updating EPM CID for peer: %v", err)
	}
}

// GetEpmCidForPeer retrieves the EPM CID associated with a given PeerID, including the current node's
func (c *AppConfig) GetEpmCidForPeer(peerID string) (string, bool) {
	cid, exists := c.IPFS.SdnEpmCid[peerID]
	return cid, exists
}

// LoadConfigFromFile loads the configuration settings from a JSON file
func (c *AppConfig) LoadConfigFromFile() error {
	configFilePath := filepath.Join(c.Datastore.Directory, "config.json")
	data, err := os.ReadFile(configFilePath)
	if err != nil {
		return fmt.Errorf("could not read configuration file: %w", err)
	}
	if err := json.Unmarshal(data, c); err != nil {
		return fmt.Errorf("could not unmarshal configuration data: %w", err)
	}
	return nil
}

// SaveConfigToFile saves the current configuration settings to a JSON file, excluding sensitive information like the datastore password
func (c *AppConfig) SaveConfigToFile() error {
	// Clone the current AppConfig object
	clonedConfig := *c

	// Remove the password from the cloned object to avoid saving it to disk
	clonedConfig.Datastore.Password = ""

	// Marshal the cloned configuration data to JSON, excluding the password
	data, err := json.MarshalIndent(clonedConfig, "", "  ")
	if err != nil {
		return fmt.Errorf("could not marshal configuration data: %w", err)
	}

	// Define the path for the configuration file
	configFilePath := filepath.Join(c.Datastore.Directory, "config.json")

	// Write the configuration data to the file
	if err := os.WriteFile(configFilePath, data, 0644); err != nil {
		return fmt.Errorf("could not write configuration file: %w", err)
	}

	return nil
}

func setDefaultDatastoreDirectory() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		panic(fmt.Sprintf("Error getting user home directory: %v", err))
	}

	return filepath.Join(homeDir, ".spacedatanetwork")
}
