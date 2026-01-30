# Contributing to Space Data Network

Thank you for your interest in contributing to the Space Data Network! This document provides guidelines and information for contributors.

## Code of Conduct

Please be respectful and constructive in all interactions. We're building infrastructure for the space community, and collaboration is essential.

## Getting Started

### Prerequisites

- Go 1.21+
- Node.js 18+
- Git

### Development Setup

```bash
# Clone the repository
git clone https://github.com/DigitalArsenal/go-space-data-network.git
cd go-space-data-network

# Install Go dependencies
cd sdn-server
go mod download

# Install JavaScript dependencies
cd ../sdn-js
npm install

# Run tests
cd ../sdn-server && go test ./...
cd ../sdn-js && npm test
```

## How to Contribute

### Reporting Issues

- Search existing issues before creating a new one
- Use the issue templates when available
- Include reproduction steps for bugs
- Provide system information (OS, versions, etc.)

### Pull Requests

1. **Fork the repository** and create your branch from `main`
2. **Write tests** for new functionality
3. **Follow the code style** of the existing codebase
4. **Update documentation** if needed
5. **Write clear commit messages**
6. **Create a pull request** with a clear description

### Commit Messages

Follow conventional commits format:

```
type(scope): description

[optional body]

[optional footer]
```

Types:
- `feat`: New feature
- `fix`: Bug fix
- `docs`: Documentation only
- `style`: Code style (formatting, etc.)
- `refactor`: Code refactoring
- `test`: Adding tests
- `chore`: Maintenance tasks

Examples:
```
feat(pubsub): add support for CDM schema subscriptions
fix(storage): handle database connection timeout
docs(readme): update installation instructions
```

## Project Structure

```
go-space-data-network/
├── sdn-server/           # Go server implementation
│   ├── cmd/              # Command-line applications
│   ├── internal/         # Internal packages
│   └── deploy/           # Deployment configs
├── sdn-js/               # TypeScript/JavaScript SDK
│   ├── src/              # Source code
│   └── tests/            # Tests
├── schemas/              # FlatBuffer schema submodule
├── docs/                 # Documentation website
└── scripts/              # Build and utility scripts
```

## Development Guidelines

### Go Code

- Follow [Effective Go](https://golang.org/doc/effective_go.html)
- Use `go fmt` before committing
- Run `go vet` and `golint`
- Write table-driven tests where appropriate

### TypeScript Code

- Follow the existing ESLint configuration
- Use TypeScript strict mode
- Document public APIs with JSDoc

### Testing

- Write unit tests for new functionality
- Integration tests for cross-component features
- Test edge cases and error conditions

```bash
# Run all Go tests
cd sdn-server && go test ./...

# Run with coverage
go test -cover ./...

# Run JavaScript tests
cd sdn-js && npm test
```

## Areas for Contribution

### Good First Issues

Look for issues labeled `good first issue` - these are suitable for newcomers.

### Priority Areas

- **Data Ingestion Plugins** - WASM plugins for converting data formats
- **Additional Schema Support** - Implementing more Space Data Standards
- **Documentation** - Improving guides and API docs
- **Testing** - Increasing test coverage
- **Performance** - Optimizations and benchmarking

### Future Work

See [Agents.md](./Agents.md) for the full implementation roadmap.

## Questions?

- Open a [GitHub Discussion](https://github.com/DigitalArsenal/go-space-data-network/discussions)
- Review existing documentation at [digitalarsenal.github.io/space-data-network](https://digitalarsenal.github.io/space-data-network/)

## License

By contributing, you agree that your contributions will be licensed under the MIT License.
