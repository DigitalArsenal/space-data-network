# Space Data Network

A Peer-to-Peer Network for Collaborative Space Data Exchange, based on [LibP2P](https://libp2p.io) and utilizing [Google Flatbuffers](https://flatbuffers.dev/) through schemas maintained at the [Space Data Standards](https://spacedatastandards.org) project.

## Environment Variables

The application can be configured using the following environment variables:

- `SPACE_DATA_NETWORK_DATASTORE_PASSWORD`: Used to access the application's keystore. This is a critical security parameter, and it's recommended to set this in production environments. If not set, the application will use a default password, which is not recommended for production use.

- `SPACE_DATA_NETWORK_DATASTORE_DIRECTORY`: Specifies the filesystem path for the secure LevelDB storage for the node's keystore. If not explicitly set via this environment variable, the application defaults to using a directory named .spacedatanetwork located in the user's home directory (e.g., ~/.spacedatanetwork). This path is critical for ensuring that the node's keystore is stored securely and persistently, and it's advisable to set this path in production environments to a secure, backed-up location.

- `SPACE_DATA_NETWORK_WEBSERVER_PORT`: Port for the webserver to listen on.

- `SPACE_DATA_NETWORK_CPUS`: Number of CPUs to give to the webserver

- `SPACE_DATA_NETWORK_ETHEREUM_DERIVATION_PATH`: BIP32 / BIP44 path to use for account. Defaults to: `m/44'/60'/0'/0/0`.

### Setting Environment Variables

#### For Development

Environment variables can be set in various ways depending on your operating environment.

##### Using a `.env` File

For development purposes, you might set them in a `.env` file with tools like `dotenv`.

Create a `.env` file in the root directory of your project and add the following line:

```env
SPACE_DATA_NETWORK_DATASTORE_PASSWORD=your_secure_password
```

Replace `your_secure_password` with your actual keystore password.

##### Setting Variables in the Shell

In Unix-based systems, you can export the environment variable in your shell before running the application:

```bash
export SPACE_DATA_NETWORK_DATASTORE_PASSWORD=your_secure_password
```

In Windows Command Prompt, you can set the environment variable like this:

```cmd
set KEYSTORE_PASSWORD=your_secure_password
```

#### For Docker

When running your application in a Docker container, you can pass environment variables using the `-e` or `--env` flag with the `docker run` command.

```bash
docker run -e SPACE_DATA_NETWORK_DATASTORE_PASSWORD=your_secure_password your_docker_image_name
```

Alternatively, you can use an `env_file` in your `docker-compose.yml` if you're using Docker Compose:

```yaml
version: "3"
services:
  your_service_name:
    image: your_docker_image_name
    env_file:
      - .env
```

Ensure the `.env` file is in the same directory as your `docker-compose.yml` or specify the correct path to it.

> **Note**: Ensure that you never expose sensitive environment variables like `SPACE_DATA_NETWORK_DATASTORE_PASSWORD` in public code repositories, Docker images, or shared documents. Always keep such information secure.

## Getting Started

### Prerequisites

### Installing

## Running the Tests

## Deployment

## Development

Install Air for development: `go install github.com/cosmtrek/air@latest`

## Contributing

Please read [CONTRIBUTING.md](./CONTRIBUTING.md) for details on our code of conduct, and the process for submitting pull requests to us.

## License

This project is licensed under the GNU Affero General Public License v3.0.

You are free to:

Use the software for any purpose.
Study how the software works and make modifications.
Share copies of the original software with others.
Distribute copies of your modified versions to others.
However, if you distribute copies or modified versions of this software, or if you make it available as a service over a network, you must also share the source code under the same license.

For more information, please refer to the full [license text](https://www.gnu.org/licenses/agpl-3.0.en.html).
