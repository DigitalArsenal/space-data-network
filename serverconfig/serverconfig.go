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

// Embed the index.html file
//
//go:embed assets/index.html
var indexHTML embed.FS

var once sync.Once

type datastoreConfig struct {
	Directory string
	Password  string
}

type webserverConfig struct {
	Port int
}

type Info struct {
	Version string `json:"version"`
	// Other properties from manifest.json can be added here.
}

type keyConfig struct {
	EntropyLengthBits int
}

type keys struct {
	EncryptionAccountDerivationPath string
	SigningAccountDerivationPath    string
}

// AppConfig holds the entire application configuration with namespaces
type AppConfig struct {
	Datastore datastoreConfig
	Webserver webserverConfig
	KeyConfig keyConfig
	Keys      keys
	Info      Info
}

// Conf is the exported variable that will hold all the configuration settings
var Conf AppConfig

// Init initializes the global configuration
func Init() {
	once.Do(func() {

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

		// Parse the version from manifest.json
		var versionInfo Info
		data, err := versionFile.ReadFile("manifest.json")
		if err != nil {
			log.Fatalf("Failed to read version file: %v", err)
		}
		if err := json.Unmarshal(data, &versionInfo); err != nil {
			log.Fatalf("Failed to parse version info: %v", err)
		}
		Conf.Info = versionInfo

		// Webserver settings
		var webserverPortStr string
		flag.StringVar(&webserverPortStr, "webserver.port", "4000", "Port for the webserver to listen on")

		// Datastore settings
		flag.StringVar(&Conf.Datastore.Directory, "datastore.directory", "", "Directory for the datastore")
		flag.StringVar(&Conf.Datastore.Password, "datastore.password", "", "Password for the datastore encryption")

		// Parse command-line flags
		flag.Parse()

		// Override webserver port with environment variable if exists
		if portStr, exists := os.LookupEnv("SPACE_DATA_NETWORK_WEBSERVER_PORT"); exists {
			webserverPortStr = portStr
		}
		if port, err := strconv.Atoi(webserverPortStr); err == nil {
			Conf.Webserver.Port = port
		} else {
			Conf.Webserver.Port = 1969 // Default port if conversion fails
		}

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

		rootDir := filepath.Join(Conf.Datastore.Directory, "root")
		if _, err := os.Stat(rootDir); os.IsNotExist(err) {
			if err := os.MkdirAll(rootDir, 0755); err != nil {
				log.Fatalf("Failed to create 'root' directory: %v", err)
			}

			// Extract 'index.html' from the embedded file system and write it to the 'root' directory
			indexContent, err := indexHTML.ReadFile("assets/index.html") // Ensure the path matches the embed directive
			if err != nil {
				log.Fatalf("Failed to read embedded 'index.html': %v", err)
			}

			indexPath := filepath.Join(rootDir, "index.html")
			if err := os.WriteFile(indexPath, indexContent, 0644); err != nil {
				log.Fatalf("Failed to create 'index.html' in the 'root' directory: %v", err)
			}
		}
	})
}

func setDefaultDatastoreDirectory() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		panic(fmt.Sprintf("Error getting user home directory: %v", err))
	}

	return filepath.Join(homeDir, ".spacedatanetwork")
}
