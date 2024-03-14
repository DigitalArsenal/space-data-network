package config

import (
	"flag"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

var once sync.Once

type datastoreConfig struct {
	Directory              string
	Password               string
	EthereumDerivationPath string
}

type webserverConfig struct {
	Port int
}

// AppConfig holds the entire application configuration with namespaces
type AppConfig struct {
	Datastore datastoreConfig
	Webserver webserverConfig
}

// Conf is the exported variable that will hold all the configuration settings
var Conf AppConfig

// Init initializes the global configuration
func Init() {
	once.Do(func() {
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

		// Override datastore settings with environment variables if they exist
		if dir, exists := os.LookupEnv("SPACE_DATA_NETWORK_DATASTORE_DIRECTORY"); exists {
			Conf.Datastore.Directory = dir
		} else {
			// Use the user's home directory if no environment variable or custom path is provided
			homeDir, err := os.UserHomeDir()
			if err != nil {
				log.Printf("Error getting user home directory: %v", err)
				return // Early return on error
			}

			// Set the default directory to a specific folder in the user's home directory
			defaultDir := filepath.Join(homeDir, ".spacedatanetwork")
			Conf.Datastore.Directory = defaultDir

			// Ensure the base directory exists
			if _, err := os.Stat(Conf.Datastore.Directory); os.IsNotExist(err) {
				if err := os.MkdirAll(Conf.Datastore.Directory, 0700); err != nil {
					log.Printf("Error creating directory '%s': %v", Conf.Datastore.Directory, err)
					return // Early return on error
				}
			}
		}

		if password, exists := os.LookupEnv("SPACE_DATA_NETWORK_DATASTORE_PASSWORD"); exists {
			Conf.Datastore.Password = password
		}

		if derivationPath, exists := os.LookupEnv("SPACE_DATA_NETWORK_ETHEREUM_DERIVATION_PATH"); exists {
			Conf.Datastore.EthereumDerivationPath = derivationPath
		} else {
			// Default to m/44'/60'/0'/0'/0 if not found
			Conf.Datastore.EthereumDerivationPath = "m/44'/60'/0'/0'/0"
		}
	})
}
