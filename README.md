# Space Data Network

A Peer-to-Peer Network for Collaborative Space Data Exchange, based on [LibP2P](https://libp2p.io) and utilizing [Google Flatbuffers](https://flatbuffers.dev/) through schemas maintained at the [Space Data Standards](https://spacedatastandards.org) project.

## CLI Options

The application supports several CLI options that allow you to control its behavior and access various functionalities:

-help: Display a detailed help message outlining all the available CLI options.

-version: Display the current version of the Space Data Network application.

-create-server-epm: Create a server Entity Profile Message (EPM), a unique identifier for your node within the network.

-output-server-epm: Output the server's Entity Profile Message (EPM) to the console. Use in conjunction with -qr to output as a QR code.

-qr: When used with -output-server-epm, outputs the server EPM as a QR code for easy sharing and scanning.

-env-docs: Display documentation for environment variables that the application uses for configuration.

## Environment Variables

The application can be configured using the following environment variables:

- `SPACE_DATA_NETWORK_DATASTORE_PASSWORD`: Used to access the application's keystore. This is a critical security parameter, and it's recommended to set this in production environments. If not set, the application will use a default password, which is not recommended for production use.

- `SPACE_DATA_NETWORK_DATASTORE_DIRECTORY`: Specifies the filesystem path for the secure LevelDB storage for the node's keystore. If not explicitly set via this environment variable, the application defaults to using a directory named .spacedatanetwork located in the user's home directory (e.g., ~/.spacedatanetwork). This path is critical for ensuring that the node's keystore is stored securely and persistently, and it's advisable to set this path in production environments to a secure, backed-up location.

- `SPACE_DATA_NETWORK_WEBSERVER_PORT`: Port for the webserver to listen on.

- `SPACE_DATA_NETWORK_CPUS`: Number of CPUs to give to the webserver

- `SPACE_DATA_NETWORK_ETHEREUM_DERIVATION_PATH`: BIP32 / BIP44 path to use for account. Defaults to: `m/44'/60'/0'/0/0`.

## License

This software is provided under the terms of the Software License Agreement contained in the [LICENSE](LICENSE) file in the root directory of this project. By downloading, installing, or using this software, you are agreeing to be bound by the terms of this agreement. Please read the license carefully before using the software.
