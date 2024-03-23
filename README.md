# Space Data Network

A Peer-to-Peer Network for Collaborative Space Data Exchange, based on [LibP2P](https://libp2p.io) and utilizing [Google Flatbuffers](https://flatbuffers.dev/) through schemas maintained at the [Space Data Standards](https://spacedatastandards.org) project.

## Environment Variables

The application can be configured using the following environment variables:

- `SPACE_DATA_NETWORK_DATASTORE_PASSWORD`: Used to access the application's keystore. This is a critical security parameter, and it's recommended to set this in production environments. If not set, the application will use a default password, which is not recommended for production use.

- `SPACE_DATA_NETWORK_DATASTORE_DIRECTORY`: Specifies the filesystem path for the secure LevelDB storage for the node's keystore. If not explicitly set via this environment variable, the application defaults to using a directory named .spacedatanetwork located in the user's home directory (e.g., ~/.spacedatanetwork). This path is critical for ensuring that the node's keystore is stored securely and persistently, and it's advisable to set this path in production environments to a secure, backed-up location.

- `SPACE_DATA_NETWORK_WEBSERVER_PORT`: Port for the webserver to listen on.

- `SPACE_DATA_NETWORK_CPUS`: Number of CPUs to give to the webserver

- `SPACE_DATA_NETWORK_ETHEREUM_DERIVATION_PATH`: BIP32 / BIP44 path to use for account.  Defaults to: `m/44'/60'/0'/0/0`.  
